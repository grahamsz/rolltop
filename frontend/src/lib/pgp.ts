// File overview: Browser PGP helpers. Heavy crypto/sanitizer libraries are
// dynamically imported so they are emitted as lazy chunks when the plugin is used.

import type { ContactPGPKey, IdentityPGPPrivateKey } from "../types";
import type { UnlockedPGPKey } from "../appTypes";

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
  signed: boolean;
  signatureStatus: PGPSignatureStatus;
  signatureDetail: string;
  signerKeyID?: string;
  signatureHashAlgorithm?: string;
  signaturePublicKeyAlgorithm?: string;
  encryptionKeyIDs?: string[];
  symmetricAlgorithm?: string;
  autocryptGossip?: AutocryptGossipKey[];
};

type PublicKeyLike = Awaited<ReturnType<OpenPGPModule["readKey"]>>;

export type AutocryptGossipKey = {
  email: string;
  publicKeyArmored: string;
};

export async function unlockPrivateKey(key: IdentityPGPPrivateKey, passphrase: string): Promise<UnlockedPGPKey> {
  const openpgp = await loadOpenPGP();
  const privateKey = await openpgp.readPrivateKey({ armoredKey: key.private_key_armored || "" });
  if (isPrivateKeyDecrypted(privateKey)) {
    throw new Error("This private key is not passphrase-protected. Export it with a passphrase before importing it.");
  }
  const unlocked = await openpgp.decryptKey({ privateKey, passphrase });
  return {
    id: key.id || 0,
    identity_id: key.identity_id,
    label: key.label || key.fingerprint || "PGP key",
    fingerprint: key.fingerprint,
    key_id: key.key_id || keyIDFromKey(privateKey),
    algorithm: keyAlgorithmSummary(privateKey),
    public_key_armored: key.public_key_armored,
    privateKey: unlocked
  };
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

export async function publicKeyRecordFromArmored(publicKeyArmored: string, email = ""): Promise<ContactPGPKey> {
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
    is_preferred: false
  };
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
  return `${headers.join("\n")}\n\n${payload}`;
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
  const armored = extractArmoredMessage(source);
  if (!armored) throw pgpBlockNotFoundError(source);
  const verificationKeys = await readVerificationKeys(openpgp, verificationKeyArmors);
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
  let symmetricAlgorithm = "";
  try {
    const sessionKeys = await openpgp.decryptSessionKeys({
      message,
      decryptionKeys: keys.map((key) => key.privateKey as never)
    });
    symmetricAlgorithm = sessionKeys[0]?.algorithm || "";
  } catch {
    symmetricAlgorithm = "";
  }
  const result = await openpgp.decrypt({
    message,
    decryptionKeys: keys.map((key) => key.privateKey as never),
    verificationKeys: verificationKeys.length > 0 ? verificationKeys as never : undefined,
    format: "utf8"
  });
  const signature = await verificationState(openpgp, result.signatures);
  const openedText = stripAutocryptGossipHeaders(String(result.data || ""));
  return {
    text: openedText.text,
    encrypted: true,
    signed: signature.signatureStatus !== "none",
    encryptionKeyIDs,
    symmetricAlgorithm,
    autocryptGossip: openedText.gossip,
    ...signature
  };
}

export async function decryptedHTMLDoc(content: string): Promise<string> {
  const html = looksLikeHTML(content) ? await sanitizeHTML(content) : `<div class="plaintext">${plainTextToHTML(content)}</div>`;
  const csp = "default-src 'none'; img-src 'self' data: cid:; style-src 'unsafe-inline'; font-src data:";
  return `<!doctype html><html><head><meta charset="utf-8"><base target="_blank"><meta name="referrer" content="no-referrer"><meta http-equiv="Content-Security-Policy" content="${csp}"><style>html,body{margin:0;padding:0;background:#fff;color:#1f2328;font:14px/1.55 Arial,sans-serif;overflow:hidden}body{padding:18px}a{color:#245f80;text-decoration:none;border-bottom:1px solid #9cc5d8}.plaintext{white-space:pre-wrap;overflow-wrap:anywhere}pre{white-space:pre-wrap;overflow-wrap:anywhere}table{max-width:100%}img{max-width:100%;height:auto}html[data-mailmirror-theme="classic_dark"],html[data-mailmirror-theme="classic_dark"] body{background:#151f1c!important;color:#e6eee9!important;color-scheme:dark}html[data-mailmirror-theme="classic_dark"] body :where(div,p,span,blockquote,pre,td,th,li){background:transparent!important;color:inherit!important;border-color:rgba(174,190,183,.28)!important}html[data-mailmirror-theme="classic_dark"] a{color:#8bd4c8!important;border-bottom-color:rgba(139,212,200,.5)!important}html[data-mailmirror-theme="matrix"],html[data-mailmirror-theme="matrix"] body{background:#06130d!important;color:#dcffe9!important;color-scheme:dark}html[data-mailmirror-theme="matrix"] body :where(div,p,span,blockquote,pre,td,th,li){background:transparent!important;color:inherit!important;border-color:rgba(74,222,128,.24)!important}html[data-mailmirror-theme="matrix"] a{color:#7dffbf!important;border-bottom-color:rgba(125,255,191,.5)!important}</style></head><body>${html}</body></html>`;
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

function assertPrivateKeyImportText(value: string) {
  const trimmed = value.trim();
  if (/-----BEGIN PGP PUBLIC KEY BLOCK-----/i.test(trimmed)) {
    throw new Error("This is a public key. Import a passphrase-protected PGP private key here.");
  }
  if (!/-----BEGIN PGP PRIVATE KEY BLOCK-----/i.test(trimmed)) {
    throw new Error("Paste an ASCII-armored PGP private key that starts with -----BEGIN PGP PRIVATE KEY BLOCK-----.");
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
    return new Error("PGP/MIME encrypted messages are detected, but this opener currently supports inline armored PGP messages only.");
  }
  if (lower.includes("multipart/signed") || lower.includes("application/pgp-signature")) {
    return new Error("Detached PGP/MIME signatures are detected, but this opener currently supports inline clear-signed PGP messages only.");
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
  return { text: normalized.slice(splitAt + 2), gossip };
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

function pgpErrorMessage(err: unknown): string {
  return err instanceof Error && err.message ? err.message : "The PGP signature could not be verified.";
}

async function sanitizeHTML(html: string): Promise<string> {
  const mod = await import("dompurify");
  const DOMPurify = mod.default;
  return DOMPurify.sanitize(html, {
    FORBID_TAGS: ["script", "style", "form", "iframe", "object", "embed"],
    FORBID_ATTR: ["onerror", "onload", "onclick", "onmouseover", "style"],
    ALLOWED_URI_REGEXP: /^(?:(?:https?|mailto|cid):|[^a-z]|[a-z+.-]+(?:[^a-z+.-:]|$))/i
  });
}

function looksLikeHTML(value: string): boolean {
  return /<\s*(?:!doctype|html|body|div|p|table|span|br|strong|em|a)\b/i.test(value);
}

function plainTextToHTML(value: string): string {
  return escapeHTML(value).replace(/\n/g, "<br>");
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
