// File overview: Browser PGP helpers. Heavy crypto/sanitizer libraries are
// dynamically imported so they are emitted as lazy chunks when the plugin is used.

import type { PGPUnlockState, UnlockedPGPKey } from "../../../../frontend/src/appTypes";
import type { ContactPGPKey, IdentityPGPPrivateKey } from "../../../../frontend/src/types";

type OpenPGPModule = typeof import("openpgp");

type GeneratedKey = {
  privateKey: string;
  publicKey: string;
  revocationCertificate?: string;
};

export type PGPSignatureStatus = "none" | "verified" | "unverified" | "invalid";

export type PGPMessageOpenResult = {
  text: string;
  encrypted: boolean;
  pgpMime?: boolean;
  signed: boolean;
  signatureStatus: PGPSignatureStatus;
  signatureDetail: string;
  signerKeyID?: string;
  signatureHashAlgorithm?: string;
  signaturePublicKeyAlgorithm?: string;
  encryptionKeyIDs?: string[];
  symmetricAlgorithm?: string;
  protectedSubject?: string;
  autocryptGossip?: AutocryptGossipKey[];
};

type PublicKeyLike = Awaited<ReturnType<OpenPGPModule["readKey"]>>;

export type SerializedUnlockedPGPKey = Omit<UnlockedPGPKey, "privateKey"> & {
  private_key_armored: string;
};

export type SerializedPGPUnlockState = {
  unlockedUntil: number;
  keys: SerializedUnlockedPGPKey[];
};

export type AutocryptGossipKey = {
  email: string;
  publicKeyArmored: string;
};

export type PGPMIMEAttachmentInput = {
  filename: string;
  contentType: string;
  contentID?: string;
  inline?: boolean;
  data: Uint8Array;
};

export type DecryptedMIMEAttachment = {
  id: string;
  filename: string;
  contentType: string;
  contentID: string;
  inline: boolean;
  size: number;
  objectURL: string;
};

export async function unlockPrivateKey(key: IdentityPGPPrivateKey, passphrase: string): Promise<UnlockedPGPKey> {
  const openpgp = await loadOpenPGP();
  const privateKey = await openpgp.readPrivateKey({ armoredKey: key.private_key_armored || "" });
  if (isPrivateKeyDecrypted(privateKey)) {
    throw new Error("This private key is not passphrase-protected. Export it with a passphrase before importing it.");
  }
  const unlocked = await openpgp.decryptKey({ privateKey, passphrase });
  const encryptionKeyID = await keyEncryptionKeyID(unlocked);
  return {
    id: key.id || 0,
    identity_id: key.identity_id,
    label: key.label || key.fingerprint || "PGP key",
    fingerprint: key.fingerprint,
    key_id: key.key_id || keyIDFromKey(privateKey),
    encryption_key_id: encryptionKeyID,
    algorithm: keyAlgorithmSummary(privateKey),
    public_key_armored: key.public_key_armored,
    privateKey: unlocked
  };
}

export async function serializePGPUnlockState(state: PGPUnlockState): Promise<SerializedPGPUnlockState> {
  if (!state.unlockedUntil || state.unlockedUntil <= Date.now()) return { unlockedUntil: 0, keys: [] };
  const keys: SerializedUnlockedPGPKey[] = [];
  for (const key of state.keys) {
    const privateKey = key.privateKey as { armor?: () => string | Promise<string> };
    const privateKeyArmored = await Promise.resolve(privateKey.armor?.() || "");
    if (!privateKeyArmored.trim()) continue;
    keys.push({
      id: key.id,
      identity_id: key.identity_id,
      label: key.label,
      fingerprint: key.fingerprint,
      public_key_armored: key.public_key_armored,
      algorithm: key.algorithm,
      key_id: key.key_id,
      encryption_key_id: key.encryption_key_id,
      private_key_armored: privateKeyArmored
    });
  }
  return { unlockedUntil: keys.length > 0 ? state.unlockedUntil : 0, keys };
}

export async function restorePGPUnlockState(state: SerializedPGPUnlockState): Promise<PGPUnlockState> {
  if (!state.unlockedUntil || state.unlockedUntil <= Date.now()) return { unlockedUntil: 0, keys: [] };
  const openpgp = await loadOpenPGP();
  const keys: UnlockedPGPKey[] = [];
  for (const item of state.keys || []) {
    try {
      const privateKey = await openpgp.readPrivateKey({ armoredKey: item.private_key_armored || "" });
      keys.push({
        id: item.id,
        identity_id: item.identity_id,
        label: item.label,
        fingerprint: item.fingerprint,
        public_key_armored: item.public_key_armored,
        algorithm: item.algorithm,
        key_id: item.key_id || keyIDFromKey(privateKey),
        encryption_key_id: item.encryption_key_id || await keyEncryptionKeyID(privateKey),
        privateKey
      });
    } catch {
      // Ignore malformed or expired worker state; the tab can prompt again.
    }
  }
  return { unlockedUntil: keys.length > 0 ? state.unlockedUntil : 0, keys };
}

export async function encryptionRecipientKeyIDsFromSource(source: string): Promise<string[]> {
  const armored = extractArmoredMessage(source);
  if (!armored) return [];
  const openpgp = await loadOpenPGP();
  const message = await openpgp.readMessage({ armoredMessage: armored });
  return keyIDsFromObjects(message.getEncryptionKeyIDs?.());
}

export async function matchingPGPPrivateKeyIDForRecipients(keys: IdentityPGPPrivateKey[], recipientKeyIDs: string[]): Promise<number> {
  const recipients = normalizedPGPKeyIDSet(recipientKeyIDs);
  if (recipients.size === 0) return 0;
  for (const key of keys) {
    const candidateIDs = await identityPGPPrivateKeyIDs(key);
    if (candidateIDs.some((id) => recipients.has(id))) {
      return key.id || 0;
    }
  }
  return 0;
}

export async function generatePrivateKey(name: string, email: string, passphrase: string): Promise<IdentityPGPPrivateKey> {
  const openpgp = await loadOpenPGP();
  const generated = await openpgp.generateKey({
    type: "ecc",
    curve: "curve25519Legacy",
    userIDs: [{ name: name || email, email }],
    passphrase,
    format: "armored"
  }) as GeneratedKey;
  return privateKeyRecordFromArmored(openpgp, generated.privateKey, generated.publicKey, generated.revocationCertificate || "", email);
}

export async function privateKeyRecordFromArmoredSource(privateKeyArmored: string, publicKeyArmored = "", email = ""): Promise<IdentityPGPPrivateKey> {
  const openpgp = await loadOpenPGP();
  return privateKeyRecordFromArmored(openpgp, privateKeyArmored, publicKeyArmored, "", email);
}

export function pgpUserIDsMatchEmail(userIDs: string, email: string): boolean {
  return userIDMatchesEmail(userIDs.split(/\r?\n/), email);
}

export function pgpUserIDEmails(userIDs: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  userIDs.split(/\r?\n/).forEach((userID) => {
    const email = normalizedEmailAddress(userID);
    if (!email.includes("@") || seen.has(email)) return;
    seen.add(email);
    out.push(email);
  });
  return out;
}

export async function publicKeyRecordFromArmored(
  publicKeyArmored: string,
  email = "",
  sourceKind = "manual",
  sourceDetail = ""
): Promise<ContactPGPKey> {
  assertPublicKeyImportText(publicKeyArmored);
  const openpgp = await loadOpenPGP();
  const publicKey = await openpgp.readKey({ armoredKey: publicKeyArmored });
  const userIDs = userIDsFromKey(publicKey);
  if (email && !userIDMatchesEmail(userIDs, email)) {
    throw new Error(`This public key does not list ${email} in its User IDs.`);
  }
  return {
    email,
    label: email || keyIDFromKey(publicKey) || "PGP key",
    fingerprint: fingerprintFromKey(publicKey),
    key_id: keyIDFromKey(publicKey),
    user_ids: userIDs.join("\n"),
    public_key_armored: publicKeyArmored.trim(),
    source_kind: sourceKind || "manual",
    source_detail: sourceDetail,
    is_preferred: false
  };
}

export async function autocryptKeyRecordFromMessageSource(source: string, senderEmail = ""): Promise<ContactPGPKey | null> {
  const expectedEmail = normalizedEmailAddress(senderEmail);
  for (const value of rawHeaderValues(source, "Autocrypt")) {
    const parsed = parseAutocryptHeaderValue(value);
    if (!parsed) continue;
    if (expectedEmail && normalizedEmailAddress(parsed.email) !== expectedEmail) continue;
    const record = await publicKeyRecordFromArmored(parsed.publicKeyArmored, parsed.email, "autocrypt", parsed.email);
    return {
      ...record,
      email: parsed.email,
      label: parsed.email || record.label,
      is_preferred: true
    };
  }
  return null;
}

export async function encryptMessageText(text: string, recipientKeys: ContactPGPKey[], signingKey?: UnlockedPGPKey): Promise<string> {
  const openpgp = await loadOpenPGP();
  const encryptionKeys: PublicKeyLike[] = [];
  const unsuitable: string[] = [];
  for (const key of recipientKeys) {
    try {
      const publicKey = await openpgp.readKey({ armoredKey: key.public_key_armored });
      await publicKey.getEncryptionKey();
      encryptionKeys.push(publicKey);
    } catch {
      unsuitable.push(key.email || key.label || key.key_id || "a recipient");
    }
  }
  if (unsuitable.length > 0) {
    throw new Error(`PGP encryption is on, but these recipients do not have suitable encryption keys: ${unsuitable.join(", ")}.`);
  }
  const message = await openpgp.createMessage({ text });
  return openpgp.encrypt({
    message,
    encryptionKeys,
    signingKeys: signingKey?.privateKey ? [signingKey.privateKey as never] : undefined
  }) as Promise<string>;
}

export async function signMessageText(text: string, signingKey: UnlockedPGPKey): Promise<string> {
  const openpgp = await loadOpenPGP();
  const message = await openpgp.createCleartextMessage({ text });
  return openpgp.sign({ message, signingKeys: signingKey.privateKey as never }) as Promise<string>;
}

export async function signPGPMIMEEntity(entity: string, signingKey: UnlockedPGPKey): Promise<string> {
  const openpgp = await loadOpenPGP();
  const message = await openpgp.createMessage({ binary: new TextEncoder().encode(ensureTrailingCRLF(normalizeCRLFText(entity))) });
  return openpgp.sign({
    message,
    signingKeys: signingKey.privateKey as never,
    detached: true,
    format: "armored"
  } as never) as Promise<string>;
}

export function pgpMIMEEntityFromBody(text: string, html: string, attachments: PGPMIMEAttachmentInput[] = []): string {
  const bodyEntity = pgpMIMEBodyEntity(text, html);
  if (attachments.length === 0) return bodyEntity;
  const boundary = `rolltop-pgp-mixed-${randomMIMEBoundaryToken()}`;
  const parts = [
    `Content-Type: multipart/mixed; boundary="${boundary}"`,
    "",
    `--${boundary}`,
    bodyEntity.trimEnd(),
    ...attachments.flatMap((attachment) => [
      `--${boundary}`,
      pgpMIMEAttachmentEntity(attachment).trimEnd()
    ]),
    `--${boundary}--`,
    ""
  ];
  return ensureTrailingCRLF(parts.join("\r\n"));
}

function pgpMIMEBodyEntity(text: string, html: string): string {
  if (html.trim()) {
    const boundary = `rolltop-pgp-alt-${randomMIMEBoundaryToken()}`;
    return ensureTrailingCRLF([
      `Content-Type: multipart/alternative; boundary="${boundary}"`,
      "",
      `--${boundary}`,
      `Content-Type: text/plain; charset="utf-8"`,
      "Content-Transfer-Encoding: 8bit",
      "",
      normalizeCRLFText(text || ""),
      `--${boundary}`,
      `Content-Type: text/html; charset="utf-8"`,
      "Content-Transfer-Encoding: 8bit",
      "",
      normalizeCRLFText(html),
      `--${boundary}--`,
      ""
    ].join("\r\n"));
  }
  return ensureTrailingCRLF([
    `Content-Type: text/plain; charset="utf-8"`,
    "Content-Transfer-Encoding: 8bit",
    "",
    normalizeCRLFText(text || "")
  ].join("\r\n"));
}

function pgpMIMEAttachmentEntity(attachment: PGPMIMEAttachmentInput): string {
  const filename = sanitizeMIMEFilename(attachment.filename || "attachment");
  const contentType = sanitizeContentType(attachment.contentType || "application/octet-stream");
  const disposition = attachment.inline ? "inline" : "attachment";
  const lines = [
    `Content-Type: ${contentType}; name="${escapeMIMEQuotedValue(filename)}"`,
    `Content-Transfer-Encoding: base64`
  ];
  if (attachment.contentID?.trim()) {
    lines.push(`Content-ID: <${sanitizeContentID(attachment.contentID)}>`);
  }
  lines.push(`Content-Disposition: ${disposition}; filename="${escapeMIMEQuotedValue(filename)}"`);
  lines.push("", base64Lines(attachment.data));
  return ensureTrailingCRLF(lines.join("\r\n"));
}

export function addAutocryptGossipHeaders(payload: string, keys: ContactPGPKey[]): string {
  const seen = new Set<string>();
  const headers: string[] = [];
  for (const key of keys) {
    const email = normalizedEmailAddress(key.email || key.user_ids || key.label);
    if (!email || seen.has(email)) continue;
    const keyData = keyDataFromArmoredPublicKey(key.public_key_armored);
    if (!keyData) continue;
    seen.add(email);
    headers.push(`Autocrypt-Gossip: addr=${email}; prefer-encrypt=mutual; keydata=${keyData}`);
  }
  if (headers.length === 0) return payload;
  const foldedHeaders = headers.map(foldMIMEHeaderLine).join("\r\n");
  const normalizedPayload = normalizeCRLFText(payload);
  const splitAt = normalizedPayload.indexOf("\r\n\r\n");
  if (splitAt >= 0 && /^[A-Za-z0-9-]+:/m.test(normalizedPayload.slice(0, splitAt))) {
    return `${foldedHeaders}\r\n${normalizedPayload}`;
  }
  return `${foldedHeaders}\r\n\r\n${normalizedPayload}`;
}

function foldMIMEHeaderLine(line: string): string {
  const normalized = line.replace(/[\r\n]+/g, " ").trim();
  const maxLength = 78;
  if (normalized.length <= maxLength) return normalized;
  const lines: string[] = [];
  let remaining = normalized;
  while (remaining.length > maxLength) {
    let breakAt = remaining.lastIndexOf(";", maxLength);
    if (breakAt <= 0) breakAt = remaining.lastIndexOf(" ", maxLength);
    if (breakAt <= 0) breakAt = maxLength;
    lines.push(remaining.slice(0, breakAt + (remaining[breakAt] === ";" ? 1 : 0)).trimEnd());
    remaining = remaining.slice(breakAt + (remaining[breakAt] === ";" ? 1 : 0)).trimStart();
  }
  if (remaining) lines.push(remaining);
  return lines.map((part, index) => index === 0 ? part : ` ${part}`).join("\r\n");
}

export async function encryptionKeyRecordsForRecipients(recipientEmails: string[], candidateKeys: ContactPGPKey[]): Promise<ContactPGPKey[]> {
  const openpgp = await loadOpenPGP();
  const selected: ContactPGPKey[] = [];
  const missing: string[] = [];
  const unsuitable: string[] = [];
  for (const recipientEmail of recipientEmails) {
    const normalized = normalizedEmailAddress(recipientEmail);
    const candidates = candidateKeys.filter((key) => normalizedEmailAddress(key.email) === normalized);
    if (candidates.length === 0) {
      missing.push(recipientEmail);
      continue;
    }
    let usable: ContactPGPKey | null = null;
    for (const key of candidates) {
      if (await publicKeyCanEncrypt(openpgp, key.public_key_armored)) {
        usable = key;
        break;
      }
    }
    if (usable) selected.push(usable);
    else unsuitable.push(recipientEmail);
  }
  if (missing.length > 0) {
    throw new Error(`PGP encryption is on, but these To/Cc/Bcc recipients do not have saved public keys: ${missing.join(", ")}.`);
  }
  if (unsuitable.length > 0) {
    throw new Error(`PGP encryption is on, but these To/Cc/Bcc recipients do not have suitable encryption keys: ${unsuitable.join(", ")}.`);
  }
  return selected;
}

export async function decryptPGPSource(source: string, keys: UnlockedPGPKey[], verificationKeyArmors: string[] = []): Promise<PGPMessageOpenResult> {
  const openpgp = await loadOpenPGP();
  const verificationKeys = await readVerificationKeys(openpgp, verificationKeyArmors);
  const pgpMimeSigned = extractPGPMIMESigned(source);
  if (pgpMimeSigned) {
    if (verificationKeys.length === 0) {
      return {
        text: pgpMimeSigned.signedEntity,
        encrypted: false,
        pgpMime: true,
        signed: true,
        signatureStatus: "unverified",
        signatureDetail: "No public key is available in this browser to verify the signature."
      };
    }
    try {
      const signaturePacket = await openpgp.readSignature({ armoredSignature: pgpMimeSigned.signatureArmored });
      const message = await openpgp.createMessage({ binary: new TextEncoder().encode(pgpMimeSigned.signedEntity) });
      const verified = await openpgp.verify({
        message,
        signature: signaturePacket,
        verificationKeys: verificationKeys as never,
        format: "binary"
      });
      const signature = await verificationState(openpgp, verified.signatures);
      return {
        text: pgpMimeSigned.signedEntity,
        encrypted: false,
        pgpMime: true,
        signed: true,
        ...signature
      };
    } catch (err) {
      return {
        text: pgpMimeSigned.signedEntity,
        encrypted: false,
        pgpMime: true,
        signed: true,
        signatureStatus: "invalid",
        signatureDetail: pgpErrorMessage(err)
      };
    }
  }

  const armored = extractArmoredMessage(source);
  if (!armored) throw pgpBlockNotFoundError(source);
  if (armored.includes("BEGIN PGP SIGNED MESSAGE")) {
    const cleartext = await openpgp.readCleartextMessage({ cleartextMessage: armored });
    if (verificationKeys.length === 0) {
      return {
        text: cleartext.getText(),
        encrypted: false,
        signed: true,
        signatureStatus: "unverified",
        signatureDetail: "No public key is available in this browser to verify the signature."
      };
    }
    try {
      const verified = await openpgp.verify({
        message: cleartext,
        verificationKeys: verificationKeys as never,
        format: "utf8"
      });
      const signature = await verificationState(openpgp, verified.signatures);
      const openedText = stripAutocryptGossipHeaders(String(verified.data || cleartext.getText()));
      return {
        text: openedText.text,
        encrypted: false,
        signed: true,
        ...signature,
        autocryptGossip: openedText.gossip
      };
    } catch (err) {
      return {
        text: cleartext.getText(),
        encrypted: false,
        signed: true,
        signatureStatus: "invalid",
        signatureDetail: pgpErrorMessage(err)
      };
    }
  }
  const message = await openpgp.readMessage({ armoredMessage: armored });
  const encryptionKeyIDs = keyIDsFromObjects(message.getEncryptionKeyIDs?.());
  const unlockedKeyIDs = await unlockedPGPKeyIDs(keys);
  let symmetricAlgorithm = "";
  try {
    // OpenPGP.js mutates encrypted session-key packets during decryptSessionKeys.
    // Parse a throwaway message so the actual decrypt below still has intact packets.
    const sessionMessage = await openpgp.readMessage({ armoredMessage: armored });
    const sessionKeys = await openpgp.decryptSessionKeys({
      message: sessionMessage,
      decryptionKeys: keys.map((key) => key.privateKey as never)
    });
    symmetricAlgorithm = sessionKeys[0]?.algorithm || "";
  } catch {
    symmetricAlgorithm = "";
  }
  let result: { data?: unknown; signatures?: unknown };
  try {
    result = await openpgp.decrypt({
      message,
      decryptionKeys: keys.map((key) => key.privateKey as never),
      verificationKeys: verificationKeys.length > 0 ? verificationKeys as never : undefined,
      format: "utf8"
    });
  } catch (err) {
    const detail = encryptionKeyIDs.length > 0 && unlockedKeyIDs.length > 0
      ? ` The encrypted message lists recipient key ${formatKeyIDList(encryptionKeyIDs)}; the unlocked Rolltop key ${unlockedKeyIDs.length === 1 ? "is" : "IDs are"} ${formatKeyIDList(unlockedKeyIDs)}.`
      : "";
    throw new Error(`Could not decrypt this PGP message.${detail} ${pgpErrorMessage(err)}`.trim());
  }
  const signature = await verificationState(openpgp, result.signatures);
  const openedText = stripAutocryptGossipHeaders(String(result.data || ""));
  const protectedSubject = protectedSubjectFromMIME(openedText.text);
  return {
    text: openedText.text,
    encrypted: true,
    pgpMime: isPGPMIMEEncryptedSource(source),
    signed: signature.signatureStatus !== "none",
    encryptionKeyIDs,
    symmetricAlgorithm,
    protectedSubject,
    autocryptGossip: openedText.gossip,
    ...signature
  };
}

export async function decryptedHTMLDoc(content: string, attachments: DecryptedMIMEAttachment[] = []): Promise<string> {
  const decoded = decodedMIMEEntityForDisplay(content);
  const cidURLs = cidURLMap(attachments);
  const display = decoded.html ? replaceCIDReferences(decoded.html, cidURLs) : decoded.text || content;
  const html = (decoded.html || looksLikeHTML(display)) ? await sanitizeHTML(display) : `<div class="plaintext">${plainTextToHTML(display)}</div>`;
  const csp = "default-src 'none'; img-src 'self' data: blob: cid:; media-src 'self' data: blob: cid:; style-src 'unsafe-inline'; font-src data:";
  return `<!doctype html><html><head><meta charset="utf-8"><base target="_blank"><meta name="referrer" content="no-referrer"><meta http-equiv="Content-Security-Policy" content="${csp}"><style>html,body{margin:0;padding:0;background:#fff;color:#1f2328;font:14px/1.55 Arial,sans-serif;overflow:hidden}body{padding:18px}a{color:#245f80;text-decoration:none;border-bottom:1px solid #9cc5d8}.plaintext{white-space:pre-wrap;overflow-wrap:anywhere}pre{white-space:pre-wrap;overflow-wrap:anywhere}table{max-width:100%}img{max-width:100%;height:auto}html[data-rolltop-theme="classic_dark"],html[data-rolltop-theme="classic_dark"] body{background:#151f1c!important;color:#e6eee9!important;color-scheme:dark}html[data-rolltop-theme="classic_dark"] body :where(div,p,span,blockquote,pre,td,th,li){background:transparent!important;color:inherit!important;border-color:rgba(174,190,183,.28)!important}html[data-rolltop-theme="classic_dark"] a{color:#8bd4c8!important;border-bottom-color:rgba(139,212,200,.5)!important}html[data-rolltop-theme="matrix"],html[data-rolltop-theme="matrix"] body{background:#06130d!important;color:#dcffe9!important;color-scheme:dark}html[data-rolltop-theme="matrix"] body :where(div,p,span,blockquote,pre,td,th,li){background:transparent!important;color:inherit!important;border-color:rgba(74,222,128,.24)!important}html[data-rolltop-theme="matrix"] a{color:#7dffbf!important;border-bottom-color:rgba(125,255,191,.5)!important}</style></head><body>${html}</body></html>`;
}

export function decryptedPlainText(content: string): string {
  const decoded = decodedMIMEEntityForDisplay(content);
  if (decoded.text?.trim()) return decoded.text;
  if (decoded.html?.trim()) return htmlToPlainText(decoded.html);
  return content;
}

export function decryptedMIMEAttachments(content: string): DecryptedMIMEAttachment[] {
  const decoded = decodedMIMEEntityForDisplay(content);
  return (decoded.attachments || []).map((attachment, index) => {
    const data = new ArrayBuffer(attachment.data.byteLength);
    new Uint8Array(data).set(attachment.data);
    const blob = new Blob([data], { type: attachment.contentType || "application/octet-stream" });
    return {
      id: `${attachment.contentID || attachment.filename || "attachment"}-${index}`,
      filename: attachment.filename || "attachment",
      contentType: attachment.contentType || "application/octet-stream",
      contentID: attachment.contentID,
      inline: attachment.inline,
      size: attachment.data.byteLength,
      objectURL: URL.createObjectURL(blob)
    };
  });
}

type DecodedMIMEEntity = {
  html?: string;
  text?: string;
  attachments?: DecodedMIMEAttachment[];
};

type DecodedMIMEAttachment = {
  filename: string;
  contentType: string;
  contentID: string;
  inline: boolean;
  data: Uint8Array;
};

function decodedMIMEEntityForDisplay(value: string): DecodedMIMEEntity {
  const entity = splitMIMEEntity(value);
  if (!entity) return {};
  return decodedMIMEEntity(entity.headers, entity.body);
}

function decodedMIMEEntity(headers: Record<string, string>, body: string): DecodedMIMEEntity {
  const contentType = headers["content-type"] || "";
  const mediaType = contentType.split(";")[0].trim().toLowerCase();
  if (mediaType.startsWith("multipart/")) {
    const boundary = headerParams(contentType).boundary || "";
    if (!boundary) return {};
    const out: DecodedMIMEEntity = { attachments: [] };
    for (const part of splitMultipartBody(body, boundary)) {
      const decoded = decodedMIMEEntityForDisplay(part);
      if (!out.html && decoded.html) out.html = decoded.html;
      if (!out.text && decoded.text) out.text = decoded.text;
      if (decoded.attachments?.length) out.attachments?.push(...decoded.attachments);
    }
    return out;
  }
  const attachment = decodedAttachment(headers, body, mediaType);
  if (attachment) {
    return { attachments: [attachment] };
  }
  if (mediaType === "text/html") {
    return { html: decodeMIMEBody(headers, body) };
  }
  if (mediaType === "text/plain") {
    return { text: decodeMIMEBody(headers, body) };
  }
  return {};
}

function protectedSubjectFromMIME(value: string): string {
  const entity = splitMIMEEntity(value);
  if (!entity) return "";
  return protectedSubjectFromMIMEEntity(entity.headers, entity.body, 0);
}

function protectedSubjectFromMIMEEntity(headers: Record<string, string>, body: string, depth: number): string {
  if (depth > 8) return "";
  const direct = decodeMIMEHeaderValue(headers["subject"] || "");
  if (direct) return direct;
  const contentType = headers["content-type"] || "";
  const mediaType = contentType.split(";")[0].trim().toLowerCase();
  if (mediaType === "text/rfc822-headers") {
    const subject = decodeMIMEHeaderValue(parseMIMEHeaderBlock(decodeMIMEBody(headers, body))["subject"] || "");
    if (subject) return subject;
  }
  if (mediaType === "message/rfc822" || mediaType === "message/global") {
    const nested = splitMIMEEntity(decodeMIMEBody(headers, body));
    if (nested) return protectedSubjectFromMIMEEntity(nested.headers, nested.body, depth + 1);
  }
  if (mediaType.startsWith("multipart/")) {
    const boundary = headerParams(contentType).boundary || "";
    if (!boundary) return "";
    for (const part of splitMultipartBody(body, boundary)) {
      const nested = splitMIMEEntity(part);
      if (!nested) continue;
      const subject = protectedSubjectFromMIMEEntity(nested.headers, nested.body, depth + 1);
      if (subject) return subject;
    }
  }
  return "";
}

function decodedAttachment(headers: Record<string, string>, body: string, mediaType: string): DecodedMIMEAttachment | null {
  const disposition = headers["content-disposition"] || "";
  const dispositionType = disposition.split(";")[0].trim().toLowerCase();
  const params = headerParams(disposition);
  const typeParams = headerParams(headers["content-type"] || "");
  const contentID = normalizeContentID(headers["content-id"] || "");
  const filename = params.filename || typeParams.name || "";
  const isAttachment = dispositionType === "attachment" || dispositionType === "inline" || contentID !== "" || (mediaType && !mediaType.startsWith("text/"));
  if (!isAttachment) return null;
  return {
    filename: filename || contentID || "attachment",
    contentType: mediaType || "application/octet-stream",
    contentID,
    inline: dispositionType === "inline" || contentID !== "",
    data: decodeMIMEBodyBytes(headers, body)
  };
}

function splitMIMEEntity(value: string): { headers: Record<string, string>; body: string } | null {
  const normalized = value.replace(/\r\n/g, "\n");
  const splitAt = normalized.indexOf("\n\n");
  if (splitAt < 0) return null;
  const headerBlock = normalized.slice(0, splitAt);
  if (!/^[A-Za-z0-9-]+:/m.test(headerBlock)) return null;
  const headers = parseMIMEHeaderBlock(headerBlock);
  return { headers, body: normalized.slice(splitAt + 2) };
}

function parseMIMEHeaderBlock(value: string): Record<string, string> {
  const normalized = value.replace(/\r\n/g, "\n");
  const headerBlock = normalized.split("\n\n", 1)[0] || "";
  const headers: Record<string, string> = {};
  for (const line of unfoldHeaderLines(headerBlock.split("\n"))) {
    const colon = line.indexOf(":");
    if (colon <= 0) continue;
    headers[line.slice(0, colon).trim().toLowerCase()] = line.slice(colon + 1).trim();
  }
  return headers;
}

function splitMultipartBody(body: string, boundary: string): string[] {
  const marker = `--${boundary}`;
  const parts: string[] = [];
  for (const chunk of body.split(marker).slice(1)) {
    if (chunk.startsWith("--")) continue;
    const part = chunk.replace(/^\n/, "").replace(/\n$/, "");
    if (part.trim()) parts.push(part);
  }
  return parts;
}

type PGPMIMESignedParts = {
  signedEntity: string;
  signatureArmored: string;
};

function extractPGPMIMESigned(source: string): PGPMIMESignedParts | null {
  const normalized = normalizeCRLFText(source);
  const root = splitRawMIMEEntity(normalized);
  if (!root) return null;
  const contentType = root.headers["content-type"] || "";
  const mediaType = contentType.split(";")[0].trim().toLowerCase();
  if (mediaType !== "multipart/signed") return null;
  const params = headerParams(contentType);
  if (!params.protocol?.toLowerCase().includes("application/pgp-signature")) return null;
  const boundary = params.boundary || "";
  if (!boundary) return null;
  const parts = splitRawMultipartBody(root.body, boundary);
  if (parts.length < 2) return null;
  const signatureEntity = splitRawMIMEEntity(parts[1]);
  const signatureBody = signatureEntity ? decodeMIMEBody(signatureEntity.headers, signatureEntity.body) : parts[1];
  const signatureArmored = signatureBody.match(/-----BEGIN PGP SIGNATURE-----[\s\S]*?-----END PGP SIGNATURE-----/)?.[0] || "";
  if (!signatureArmored) return null;
  return {
    signedEntity: parts[0],
    signatureArmored
  };
}

function splitRawMIMEEntity(value: string): { headers: Record<string, string>; body: string } | null {
  const splitAt = value.indexOf("\r\n\r\n");
  if (splitAt < 0) return null;
  const headerBlock = value.slice(0, splitAt);
  if (!/^[A-Za-z0-9-]+:/m.test(headerBlock)) return null;
  const headers: Record<string, string> = {};
  for (const line of unfoldHeaderLines(headerBlock.split("\r\n"))) {
    const colon = line.indexOf(":");
    if (colon <= 0) continue;
    headers[line.slice(0, colon).trim().toLowerCase()] = line.slice(colon + 1).trim();
  }
  return { headers, body: value.slice(splitAt + 4) };
}

function splitRawMultipartBody(body: string, boundary: string): string[] {
  const marker = `--${boundary}`;
  const boundaryIndexes: number[] = [];
  let offset = 0;
  while (offset < body.length) {
    const index = body.indexOf(marker, offset);
    if (index < 0) break;
    if (index === 0 || body[index - 1] === "\n") {
      boundaryIndexes.push(index);
    }
    offset = index + marker.length;
  }
  const parts: string[] = [];
  for (let i = 0; i < boundaryIndexes.length - 1; i++) {
    const boundaryAt = boundaryIndexes[i];
    const lineEnd = body.indexOf("\n", boundaryAt);
    if (lineEnd < 0) break;
    const boundaryLine = body.slice(boundaryAt, lineEnd).replace(/\r$/, "");
    if (boundaryLine === `${marker}--`) break;
    if (boundaryLine !== marker) continue;
    const start = lineEnd + 1;
    let end = boundaryIndexes[i + 1];
    if (body.slice(end - 2, end) === "\r\n") end -= 2;
    const part = body.slice(start, end);
    if (part.trim()) parts.push(part);
  }
  return parts;
}

function isPGPMIMEEncryptedSource(source: string): boolean {
  const contentType = splitRawMIMEEntity(normalizeCRLFText(source))?.headers["content-type"] || "";
  return contentType.toLowerCase().includes("multipart/encrypted") &&
    contentType.toLowerCase().includes("application/pgp-encrypted");
}

function decodeMIMEBody(headers: Record<string, string>, body: string): string {
  const encoding = (headers["content-transfer-encoding"] || "").toLowerCase();
  if (encoding.includes("quoted-printable")) return decodeQuotedPrintable(body);
  if (encoding.includes("base64")) return decodeBase64Text(body);
  return body;
}

function decodeMIMEBodyBytes(headers: Record<string, string>, body: string): Uint8Array {
  const encoding = (headers["content-transfer-encoding"] || "").toLowerCase();
  if (encoding.includes("base64")) return base64ToBytes(body);
  if (encoding.includes("quoted-printable")) return new TextEncoder().encode(decodeQuotedPrintable(body));
  return new TextEncoder().encode(body);
}

function base64ToBytes(value: string): Uint8Array {
  const compact = value.replace(/[^A-Za-z0-9+/=]/g, "");
  if (!compact) return new Uint8Array();
  try {
    const binary = atob(compact);
    const out = new Uint8Array(binary.length);
    for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
    return out;
  } catch {
    return new TextEncoder().encode(value);
  }
}

function base64Lines(value: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < value.length; i += chunk) {
    binary += String.fromCharCode(...value.slice(i, i + chunk));
  }
  return (btoa(binary).match(/.{1,76}/g) || [""]).join("\r\n");
}

function sanitizeMIMEFilename(value: string): string {
  const cleaned = value.trim().replace(/\0/g, "").replace(/[\\/\r\n"]/g, "_");
  return cleaned || "attachment";
}

function sanitizeContentType(value: string): string {
  const cleaned = value.trim().replace(/[\r\n]/g, "");
  return /^[A-Za-z0-9!#$&^_.+-]+\/[A-Za-z0-9!#$&^_.+-]+$/.test(cleaned) ? cleaned : "application/octet-stream";
}

function escapeMIMEQuotedValue(value: string): string {
  return value.replace(/\\/g, "\\\\").replace(/"/g, "\\\"");
}

function sanitizeContentID(value: string): string {
  return value.trim().replace(/^<|>$/g, "").replace(/[\r\n<>]/g, "");
}

function normalizeContentID(value: string): string {
  return sanitizeContentID(value).toLowerCase();
}

function cidURLMap(attachments: DecryptedMIMEAttachment[]): Map<string, string> {
  const out = new Map<string, string>();
  attachments.forEach((attachment) => {
    const key = normalizeContentID(attachment.contentID);
    if (key) out.set(key, attachment.objectURL);
  });
  return out;
}

function replaceCIDReferences(html: string, cidURLs: Map<string, string>): string {
  if (cidURLs.size === 0) return html;
  return html.replace(/cid:([^"'\s>)]+)/gi, (match, raw: string) => {
    const key = normalizeContentID(decodeURIComponentSafe(raw));
    return cidURLs.get(key) || match;
  });
}

function decodeURIComponentSafe(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

export function pgpPassphraseIssues(passphrase: string, identityValues: string[]): string[] {
  const issues: string[] = [];
  const normalized = passphrase.toLowerCase();
  if (passphrase.length < 14) issues.push("Use at least 14 characters.");
  if (/^(password|passphrase|rolltop|letmein|123456|qwerty)/i.test(passphrase)) issues.push("Choose a less obvious passphrase.");
  for (const value of identityValues) {
    const clean = value.trim().toLowerCase();
    if (clean && clean.length >= 4 && normalized.includes(clean)) {
      issues.push("Do not include your name, email, domain, or rolltop password.");
      break;
    }
  }
  return Array.from(new Set(issues));
}

async function privateKeyRecordFromArmored(openpgp: OpenPGPModule, privateKeyArmored: string, publicKeyArmored: string, revocationCertificate: string, email: string): Promise<IdentityPGPPrivateKey> {
  assertPrivateKeyImportText(privateKeyArmored);
  let privateKey: Awaited<ReturnType<OpenPGPModule["readPrivateKey"]>>;
  try {
    privateKey = await openpgp.readPrivateKey({ armoredKey: privateKeyArmored });
  } catch {
    throw new Error("This is not a valid ASCII-armored PGP private key.");
  }
  if (isPrivateKeyDecrypted(privateKey)) {
    throw new Error("This private key is not passphrase-protected. Export it with a passphrase before importing it.");
  }
  let publicArmored = publicKeyArmored.trim();
  if (!publicArmored) {
    publicArmored = privateKey.toPublic().armor();
  }
  const userIDs = userIDsFromKey(privateKey);
  if (email && !userIDMatchesEmail(userIDs, email)) {
    throw new Error(`This private key does not list ${email} in its User IDs.`);
  }
  return {
    identity_id: 0,
    label: email || keyIDFromKey(privateKey) || "PGP key",
    fingerprint: fingerprintFromKey(privateKey),
    key_id: keyIDFromKey(privateKey),
    user_ids: userIDs.join("\n"),
    public_key_armored: publicArmored,
    private_key_armored: privateKeyArmored.trim(),
    revocation_certificate: revocationCertificate.trim(),
    is_active_signing: true,
    is_active_encryption: true,
    is_decrypt_only: false
  };
}

function extractArmoredMessage(source: string): string {
  for (const candidate of pgpSourceCandidates(source)) {
    const normalized = candidate.replace(/\r\n/g, "\n");
    const signed = normalized.match(/-----BEGIN PGP SIGNED MESSAGE-----[\s\S]*?-----END PGP SIGNATURE-----/);
    if (signed) return signed[0];
    const encrypted = normalized.match(/-----BEGIN PGP MESSAGE-----[\s\S]*?-----END PGP MESSAGE-----/);
    if (encrypted) return encrypted[0];
  }
  return "";
}

function pgpSourceCandidates(source: string): string[] {
  const candidates = [source, decodeQuotedPrintable(source)];
  const decodedParts = decodeMIMETransferTextParts(source);
  for (const part of decodedParts) {
    candidates.push(part, decodeQuotedPrintable(part));
  }
  return Array.from(new Set(candidates.filter((value) => value.trim() !== "")));
}

function decodeMIMETransferTextParts(source: string): string[] {
  const normalized = source.replace(/\r\n/g, "\n");
  const boundaryMatches = Array.from(normalized.matchAll(/boundary="?([^";\n]+)"?/gi)).map((match) => match[1]).filter(Boolean);
  if (boundaryMatches.length === 0) {
    return [decodeMIMETransferTextPart(normalized)];
  }
  const parts: string[] = [];
  for (const boundary of boundaryMatches) {
    const marker = `--${boundary}`;
    for (const chunk of normalized.split(marker).slice(1)) {
      if (chunk.startsWith("--")) continue;
      parts.push(decodeMIMETransferTextPart(chunk.replace(/^\n/, "")));
    }
  }
  return parts;
}

function decodeMIMETransferTextPart(part: string): string {
  const splitAt = part.indexOf("\n\n");
  if (splitAt < 0) return part;
  const headers = part.slice(0, splitAt).toLowerCase();
  const body = part.slice(splitAt + 2);
  if (!headers.includes("content-transfer-encoding:")) return body;
  if (headers.includes("quoted-printable")) return decodeQuotedPrintable(body);
  if (headers.includes("base64")) return decodeBase64Text(body);
  return body;
}

function decodeQuotedPrintable(value: string): string {
  return value
    .replace(/=\r?\n/g, "")
    .replace(/=([0-9a-f]{2})/gi, (_, hex: string) => String.fromCharCode(parseInt(hex, 16)));
}

function decodeBase64Text(value: string): string {
  const compact = value.replace(/[^A-Za-z0-9+/=]/g, "");
  if (!compact) return value;
  try {
    return atob(compact);
  } catch {
    return value;
  }
}

function decodeMIMEHeaderValue(value: string): string {
  const normalized = value
    .replace(/\r?\n[ \t]+/g, " ")
    .replace(/(=\?[^?]+\?[bqBQ]\?[^?]*\?=)\s+(?==\?[^?]+\?[bqBQ]\?)/g, "$1");
  return normalized.replace(/=\?([^?]+)\?([bqBQ])\?([^?]*)\?=/g, (match, charset: string, encoding: string, encoded: string) => {
    try {
      const bytes = encoding.toLowerCase() === "b" ? base64ToBytes(encoded) : qEncodedHeaderBytes(encoded);
      return new TextDecoder(charset).decode(bytes);
    } catch {
      return match;
    }
  }).trim();
}

function qEncodedHeaderBytes(value: string): Uint8Array {
  const out: number[] = [];
  const normalized = value.replace(/_/g, " ");
  for (let i = 0; i < normalized.length; i++) {
    if (normalized[i] === "=" && /^[0-9a-f]{2}$/i.test(normalized.slice(i + 1, i + 3))) {
      out.push(parseInt(normalized.slice(i + 1, i + 3), 16));
      i += 2;
      continue;
    }
    out.push(normalized.charCodeAt(i) & 0xff);
  }
  return new Uint8Array(out);
}

function assertPrivateKeyImportText(value: string) {
  const trimmed = value.trim();
  if (/-----BEGIN PGP PUBLIC KEY BLOCK-----/i.test(trimmed)) {
    throw new Error("This is a public key. Import a passphrase-protected PGP private key here.");
  }
  if (!/-----BEGIN PGP PRIVATE KEY BLOCK-----/i.test(trimmed)) {
    throw new Error("Paste an ASCII-armored PGP private key that starts with -----BEGIN PGP PRIVATE KEY BLOCK-----.");
  }
}

function assertPublicKeyImportText(value: string) {
  const trimmed = value.trim();
  if (/-----BEGIN PGP PRIVATE KEY BLOCK-----/i.test(trimmed)) {
    throw new Error("This is a private key. Import only an ASCII-armored PGP public key here.");
  }
  if (!/-----BEGIN PGP PUBLIC KEY BLOCK-----/i.test(trimmed)) {
    throw new Error("Paste an ASCII-armored PGP public key that starts with -----BEGIN PGP PUBLIC KEY BLOCK-----.");
  }
}

async function publicKeyCanEncrypt(openpgp: OpenPGPModule, publicKeyArmored: string): Promise<boolean> {
  try {
    const publicKey = await openpgp.readKey({ armoredKey: publicKeyArmored });
    await publicKey.getEncryptionKey();
    return true;
  } catch {
    return false;
  }
}

function pgpBlockNotFoundError(source: string): Error {
  const lower = source.toLowerCase();
  if (lower.includes("multipart/encrypted") || lower.includes("application/pgp-encrypted")) {
    return new Error("PGP/MIME encrypted message detected, but no ASCII-armored encrypted payload was found.");
  }
  if (lower.includes("multipart/signed") || lower.includes("application/pgp-signature")) {
    return new Error("PGP/MIME signature detected, but the signed body or detached signature part could not be read.");
  }
  return new Error("No PGP message block was found.");
}

async function readVerificationKeys(openpgp: OpenPGPModule, armors: string[]): Promise<PublicKeyLike[]> {
  const unique = Array.from(new Set(armors.map((armor) => armor.trim()).filter(Boolean)));
  const keys: PublicKeyLike[] = [];
  for (const armoredKey of unique) {
    try {
      keys.push(await openpgp.readKey({ armoredKey }));
    } catch {
      // Bad contact keys should not prevent decrypting or opening a signed message.
    }
  }
  return keys;
}

type OpenPGPSignatureResult = {
  verified?: Promise<unknown>;
  keyID?: { toHex?: () => string };
  signature?: Promise<unknown>;
};

async function verificationState(openpgp: OpenPGPModule, signatures: unknown): Promise<Omit<PGPMessageOpenResult, "text" | "encrypted" | "signed">> {
  const items = Array.isArray(signatures) ? signatures as OpenPGPSignatureResult[] : [];
  if (items.length === 0) {
    return { signatureStatus: "none", signatureDetail: "" };
  }
  let detail = "The signature could not be verified with the available public keys.";
  let fallbackAlgorithms: Pick<PGPMessageOpenResult, "signatureHashAlgorithm" | "signaturePublicKeyAlgorithm"> = {};
  for (const signature of items) {
    const algorithms = await signatureAlgorithms(openpgp, signature.signature);
    fallbackAlgorithms = { ...fallbackAlgorithms, ...algorithms };
    if (!signature.verified) {
      continue;
    }
    try {
      await signature.verified;
      const signerKeyID = signature.keyID?.toHex?.().toUpperCase();
      return {
        signatureStatus: "verified",
        signatureDetail: signerKeyID ? `Signature verified with key ${signerKeyID}.` : "Signature verified.",
        signerKeyID,
        ...algorithms
      };
    } catch (err) {
      detail = pgpErrorMessage(err);
    }
  }
  return { signatureStatus: "invalid", signatureDetail: detail, ...fallbackAlgorithms };
}

async function signatureAlgorithms(openpgp: OpenPGPModule, signaturePromise: Promise<unknown> | undefined): Promise<Pick<PGPMessageOpenResult, "signatureHashAlgorithm" | "signaturePublicKeyAlgorithm">> {
  if (!signaturePromise) return {};
  try {
    const signature = await signaturePromise as { packets?: Array<{ hashAlgorithm?: unknown; publicKeyAlgorithm?: unknown }> };
    const packet = signature.packets?.[0];
    if (!packet) return {};
    return {
      signatureHashAlgorithm: enumName(openpgp, "hash", packet.hashAlgorithm),
      signaturePublicKeyAlgorithm: enumName(openpgp, "publicKey", packet.publicKeyAlgorithm)
    };
  } catch {
    return {};
  }
}

function keyAlgorithmSummary(key: unknown): string {
  try {
    const info = (key as { getAlgorithmInfo?: () => { algorithm?: string; bits?: number; curve?: string } }).getAlgorithmInfo?.();
    if (!info?.algorithm) return "";
    const algorithm = publicKeyAlgorithmLabel(info.algorithm);
    const size = info.curve || (info.bits ? `${info.bits} bit` : "");
    return [algorithm, size].filter(Boolean).join(" ");
  } catch {
    return "";
  }
}

function publicKeyAlgorithmLabel(value: string): string {
  const labels: Record<string, string> = {
    rsaEncryptSign: "RSA",
    rsaEncrypt: "RSA encryption",
    rsaSign: "RSA signing",
    elgamal: "ElGamal",
    dsa: "DSA",
    ecdh: "ECDH",
    ecdsa: "ECDSA",
    eddsaLegacy: "EdDSA",
    ed25519: "Ed25519",
    x25519: "X25519",
    ed448: "Ed448",
    x448: "X448"
  };
  return labels[value] || value;
}

function enumName(openpgp: OpenPGPModule, group: "hash" | "publicKey", value: unknown): string {
  if (value === null || value === undefined) return "";
  try {
    const enums = openpgp.enums as unknown as {
      read?: (type: unknown, value: unknown) => string;
      hash?: unknown;
      publicKey?: unknown;
    };
    return enums.read?.(enums[group], value) || "";
  } catch {
    return "";
  }
}

function keyIDsFromObjects(values: unknown): string[] {
  if (!Array.isArray(values)) return [];
  const out: string[] = [];
  for (const value of values) {
    const keyID = (value as { toHex?: () => string })?.toHex?.().toUpperCase();
    if (keyID) out.push(keyID);
  }
  return Array.from(new Set(out));
}

async function identityPGPPrivateKeyIDs(key: IdentityPGPPrivateKey): Promise<string[]> {
  const out = [key.key_id || "", key.fingerprint?.slice(-16) || ""];
  for (const armoredKey of [key.public_key_armored, key.private_key_armored || ""]) {
    if (!armoredKey.trim()) continue;
    try {
      const openpgp = await loadOpenPGP();
      const parsed = armoredKey.includes("BEGIN PGP PRIVATE KEY BLOCK")
        ? await openpgp.readPrivateKey({ armoredKey })
        : await openpgp.readKey({ armoredKey });
      out.push(...keyIDsFromKeyMaterial(parsed));
      out.push(await keyEncryptionKeyID(parsed));
    } catch {
      // A malformed optional key copy should not prevent matching metadata.
    }
  }
  return Array.from(normalizedPGPKeyIDSet(out));
}

function normalizedPGPKeyIDSet(values: string[]): Set<string> {
  return new Set(values.map((value) => value.replace(/\s+/g, "").toUpperCase()).filter((value) => value && !/^0+$/.test(value)));
}

async function unlockedPGPKeyIDs(keys: UnlockedPGPKey[]): Promise<string[]> {
  const out: string[] = [];
  for (const key of keys) {
    out.push(key.key_id || "", key.encryption_key_id || "", key.fingerprint?.slice(-16) || "");
    out.push(...keyIDsFromKeyMaterial(key.privateKey));
    out.push(await keyEncryptionKeyID(key.privateKey));
    out.push(...await keyDecryptionKeyIDs(key.privateKey));
  }
  return Array.from(normalizedPGPKeyIDSet(out));
}

function keyIDsFromKeyMaterial(key: unknown): string[] {
  const out = [keyIDFromKey(key)];
  try {
    const allKeys = (key as { getKeys?: () => unknown[] }).getKeys?.() || [];
    for (const item of allKeys) {
      out.push(keyIDFromKey(item));
    }
  } catch {
    // Best-effort diagnostics only.
  }
  return out;
}

async function keyEncryptionKeyID(key: unknown): Promise<string> {
  try {
    const encryptionKey = await (key as { getEncryptionKey?: () => Promise<unknown> }).getEncryptionKey?.();
    return keyIDFromKey(encryptionKey);
  } catch {
    return "";
  }
}

async function keyDecryptionKeyIDs(key: unknown): Promise<string[]> {
  try {
    const decryptionKeys = await (key as { getDecryptionKeys?: () => Promise<unknown[]> }).getDecryptionKeys?.() || [];
    return decryptionKeys.map((item) => keyIDFromKey(item));
  } catch {
    return [];
  }
}

function formatKeyIDList(values: string[]): string {
  const clean = Array.from(new Set(values.map((value) => value.replace(/\s+/g, "").toUpperCase()).filter(Boolean)));
  if (clean.length === 0) return "unknown";
  return clean.join(", ");
}

function stripAutocryptGossipHeaders(value: string): { text: string; gossip: AutocryptGossipKey[] } {
  const normalized = value.replace(/\r\n/g, "\n");
  const splitAt = normalized.indexOf("\n\n");
  if (splitAt < 0) return { text: value, gossip: [] };
  const headerBlock = normalized.slice(0, splitAt);
  if (!/^Autocrypt-Gossip:/im.test(headerBlock)) {
    return { text: value, gossip: [] };
  }
  const unfolded = unfoldHeaderLines(headerBlock.split("\n"));
  const gossip = unfolded
    .filter((line) => /^Autocrypt-Gossip:/i.test(line))
    .map((line) => parseAutocryptHeaderValue(line.replace(/^Autocrypt-Gossip:\s*/i, "")))
    .filter((item): item is AutocryptGossipKey => Boolean(item));
  const keptHeaders = unfolded.filter((line) => !/^Autocrypt-Gossip:/i.test(line));
  const body = normalized.slice(splitAt + 2);
  return { text: keptHeaders.length > 0 ? `${keptHeaders.join("\n")}\n\n${body}` : body, gossip };
}

function unfoldHeaderLines(lines: string[]): string[] {
  const out: string[] = [];
  for (const line of lines) {
    if (/^[ \t]/.test(line) && out.length > 0) {
      out[out.length - 1] += line.trim();
      continue;
    }
    out.push(line);
  }
  return out;
}

function rawHeaderValues(source: string, headerName: string): string[] {
  const normalized = source.replace(/\r\n/g, "\n");
  const splitAt = normalized.indexOf("\n\n");
  if (splitAt < 0) return [];
  const headerBlock = normalized.slice(0, splitAt);
  const prefix = headerName.toLowerCase() + ":";
  return unfoldHeaderLines(headerBlock.split("\n"))
    .filter((line) => line.toLowerCase().startsWith(prefix))
    .map((line) => line.slice(prefix.length).trim())
    .filter(Boolean);
}

function parseAutocryptHeaderValue(value: string): AutocryptGossipKey | null {
  const params = headerParams(value);
  const email = normalizedEmailAddress(params.addr || "");
  const publicKeyArmored = armoredPublicKeyFromKeyData(params.keydata || "");
  if (!email || !publicKeyArmored) return null;
  return { email, publicKeyArmored };
}

function headerParams(value: string): Record<string, string> {
  const params: Record<string, string> = {};
  const pattern = /(?:^|;)\s*([A-Za-z0-9_-]+)=("[^"]*"|[^;]*)/g;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(value)) !== null) {
    const raw = match[2].trim();
    params[match[1].toLowerCase()] = raw.startsWith("\"") && raw.endsWith("\"") ? raw.slice(1, -1) : raw;
  }
  return params;
}

function keyDataFromArmoredPublicKey(armored: string): string {
  const lines = armored.replace(/\r\n/g, "\n").split("\n");
  let inBlock = false;
  let inPayload = false;
  let payload = "";
  for (const rawLine of lines) {
    const line = rawLine.trim();
    if (/^-----BEGIN PGP PUBLIC KEY BLOCK-----$/i.test(line)) {
      inBlock = true;
      continue;
    }
    if (/^-----END PGP PUBLIC KEY BLOCK-----$/i.test(line)) break;
    if (!inBlock) continue;
    if (line === "") {
      inPayload = true;
      continue;
    }
    if (!inPayload && line.includes(":")) continue;
    if (line.startsWith("=")) continue;
    payload += line;
  }
  return normalizeBase64(payload);
}

function armoredPublicKeyFromKeyData(keyData: string): string {
  keyData = normalizeBase64(keyData);
  if (!keyData) return "";
  const lines = keyData.match(/.{1,76}/g) || [];
  return `-----BEGIN PGP PUBLIC KEY BLOCK-----\n\n${lines.join("\n")}\n-----END PGP PUBLIC KEY BLOCK-----`;
}

function normalizeBase64(value: string): string {
  const compact = value.replace(/[\s\r\n\t]/g, "");
  if (!compact) return "";
  try {
    const padded = compact + "=".repeat((4 - (compact.length % 4)) % 4);
    const binary = atob(padded);
    return btoa(binary);
  } catch {
    return "";
  }
}

function randomMIMEBoundaryToken(): string {
  const bytes = new Uint8Array(12);
  if (typeof crypto !== "undefined" && crypto.getRandomValues) {
    crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
  }
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function normalizeCRLFText(value: string): string {
  return value.replace(/\r\n/g, "\n").replace(/\r/g, "\n").replace(/\n/g, "\r\n");
}

function ensureTrailingCRLF(value: string): string {
  return value.endsWith("\r\n") ? value : `${value}\r\n`;
}

function pgpErrorMessage(err: unknown): string {
  return err instanceof Error && err.message ? err.message : "The PGP signature could not be verified.";
}

async function sanitizeHTML(html: string): Promise<string> {
  const mod = await import("dompurify");
  const DOMPurify = mod.default;
  return DOMPurify.sanitize(html, {
    FORBID_TAGS: ["script", "style", "form", "iframe", "object", "embed"],
    FORBID_ATTR: ["onerror", "onload", "onclick", "onmouseover", "style"],
    ALLOWED_URI_REGEXP: /^(?:(?:https?|mailto|cid|blob):|[^a-z]|[a-z+.-]+(?:[^a-z+.-:]|$))/i
  });
}

function looksLikeHTML(value: string): boolean {
  return /<\s*(?:!doctype|html|body|div|p|table|span|br|strong|em|a)\b/i.test(value);
}

function plainTextToHTML(value: string): string {
  return escapeHTML(value).replace(/\n/g, "<br>");
}

function htmlToPlainText(value: string): string {
  if (typeof document === "undefined") return value.replace(/<[^>]+>/g, " ");
  const template = document.createElement("template");
  template.innerHTML = value;
  return template.content.textContent || "";
}

function escapeHTML(value: string): string {
  return value.replace(/[&<>"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[char] || char));
}

function fingerprintFromKey(key: unknown): string {
  const value = (key as { getFingerprint?: () => string }).getFingerprint?.() || "";
  return value.toUpperCase();
}

function keyIDFromKey(key: unknown): string {
  const id = (key as { getKeyID?: () => { toHex?: () => string } }).getKeyID?.();
  return id?.toHex?.().toUpperCase() || "";
}

function userIDsFromKey(key: unknown): string[] {
  const users = (key as { users?: Array<{ userID?: { userID?: string } }> }).users || [];
  return users.map((user) => user.userID?.userID || "").filter(Boolean);
}

function isPrivateKeyDecrypted(key: unknown): boolean {
  try {
    return Boolean((key as { isDecrypted?: () => boolean }).isDecrypted?.());
  } catch {
    return false;
  }
}

function userIDMatchesEmail(userIDs: string[], email: string): boolean {
  const target = normalizedEmailAddress(email);
  if (!target) return false;
  return userIDs.some((userID) => normalizedEmailAddress(userID) === target);
}

function normalizedEmailAddress(value: string): string {
  const angle = value.match(/<([^>]+)>/);
  const raw = (angle ? angle[1] : value).trim();
  const match = raw.match(/[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}/i);
  return (match ? match[0] : raw).trim().toLowerCase();
}

async function loadOpenPGP(): Promise<OpenPGPModule> {
  try {
    return await import("openpgp");
  } catch {
    throw new Error("Could not load the browser PGP engine. Reload rolltop and try again.");
  }
}
