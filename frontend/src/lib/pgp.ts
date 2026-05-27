// File overview: Browser-only PGP helpers. Heavy crypto/sanitizer libraries are
// dynamically imported so they are emitted as lazy chunks when the plugin is used.

import type { ContactPGPKey, IdentityPGPPrivateKey } from "../types";
import type { UnlockedPGPKey } from "../appTypes";

type OpenPGPModule = typeof import("openpgp");

type GeneratedKey = {
  privateKey: string;
  publicKey: string;
  revocationCertificate?: string;
};

export async function unlockPrivateKey(key: IdentityPGPPrivateKey, passphrase: string): Promise<UnlockedPGPKey> {
  const openpgp = await loadOpenPGP();
  const privateKey = await openpgp.readPrivateKey({ armoredKey: key.private_key_armored || "" });
  const unlocked = await openpgp.decryptKey({ privateKey, passphrase });
  return {
    id: key.id || 0,
    identity_id: key.identity_id,
    label: key.label || key.fingerprint || "PGP key",
    fingerprint: key.fingerprint,
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

export async function publicKeyRecordFromArmored(publicKeyArmored: string, email = ""): Promise<ContactPGPKey> {
  const openpgp = await loadOpenPGP();
  const publicKey = await openpgp.readKey({ armoredKey: publicKeyArmored });
  const userIDs = userIDsFromKey(publicKey);
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
  const encryptionKeys = await Promise.all(recipientKeys.map((key) => openpgp.readKey({ armoredKey: key.public_key_armored })));
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

export async function decryptPGPSource(source: string, keys: UnlockedPGPKey[]): Promise<string> {
  const openpgp = await loadOpenPGP();
  const armored = extractArmoredMessage(source);
  if (!armored) throw new Error("No PGP message block was found.");
  if (armored.includes("BEGIN PGP SIGNED MESSAGE")) {
    const cleartext = await openpgp.readCleartextMessage({ cleartextMessage: armored });
    return cleartext.getText();
  }
  const message = await openpgp.readMessage({ armoredMessage: armored });
  const result = await openpgp.decrypt({
    message,
    decryptionKeys: keys.map((key) => key.privateKey as never),
    format: "utf8"
  });
  return String(result.data || "");
}

export async function decryptedHTMLDoc(content: string): Promise<string> {
  const html = looksLikeHTML(content) ? await sanitizeHTML(content) : `<div class="plaintext">${plainTextToHTML(content)}</div>`;
  return `<!doctype html><html><head><meta charset="utf-8"><style>body{font:14px/1.5 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#151f2e;background:transparent;margin:0}a{color:#9f5538}.plaintext{white-space:pre-wrap}</style></head><body>${html}</body></html>`;
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
  const privateKey = await openpgp.readPrivateKey({ armoredKey: privateKeyArmored });
  let publicArmored = publicKeyArmored.trim();
  if (!publicArmored) {
    publicArmored = privateKey.toPublic().armor();
  }
  const userIDs = userIDsFromKey(privateKey);
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
  const normalized = source.replace(/\r\n/g, "\n");
  const signed = normalized.match(/-----BEGIN PGP SIGNED MESSAGE-----[\s\S]*?-----END PGP SIGNATURE-----/);
  if (signed) return signed[0];
  const encrypted = normalized.match(/-----BEGIN PGP MESSAGE-----[\s\S]*?-----END PGP MESSAGE-----/);
  return encrypted ? encrypted[0] : "";
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

async function loadOpenPGP(): Promise<OpenPGPModule> {
  return import("openpgp");
}
