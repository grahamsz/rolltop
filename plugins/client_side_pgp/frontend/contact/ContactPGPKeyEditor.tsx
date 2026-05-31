import { useCallback, useEffect, useMemo, useState } from "react";
import type { ContactEmail, ContactPGPKey } from "../../../../frontend/src/types";
import type { Toast } from "../../../../frontend/src/appTypes";
import { Icon } from "../../../../frontend/src/components/Icon";
import { messageFromError } from "../../../../frontend/src/lib/errors";
import type { ClientSidePGPPlugin } from "../types";

export function ContactPGPKeyEditor({
  csrf,
  contactID,
  emails,
  pgpPlugin,
  addToast
}: {
  csrf: string;
  contactID: number;
  emails: ContactEmail[];
  pgpPlugin: ClientSidePGPPlugin;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const emailChoices = useMemo(() => emails.map((item) => item.email.trim()).filter(Boolean), [emails]);
  const [keys, setKeys] = useState<ContactPGPKey[]>([]);
  const [loading, setLoading] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [adding, setAdding] = useState(false);

  const loadKeys = useCallback(async () => {
    if (emailChoices.length === 0) {
      setKeys([]);
      return;
    }
    setLoading(true);
    try {
      const data = await pgpPlugin.publicKeys(emailChoices, true);
      setKeys(data.keys || []);
    } finally {
      setLoading(false);
    }
  }, [emailChoices, pgpPlugin]);

  useEffect(() => {
    void loadKeys().catch((err) => addToast(messageFromError(err), "error"));
  }, [addToast, loadKeys]);

  async function addKey(armored: string) {
    if (!contactID) throw new Error("Save this contact before importing a PGP public key.");
    if (emailChoices.length === 0) throw new Error("Add a contact email before importing a PGP public key.");
    if (!armored.trim()) throw new Error("Paste an ASCII-armored PGP public key first.");
    setAdding(true);
    try {
      const parsed = await pgpPlugin.publicKeyRecordFromArmored(armored);
      const parsedEmails = new Set(pgpPlugin.pgpUserIDEmails(parsed.user_ids).map(normalizeEmailForMatch));
      const matchingEmail = emailChoices.find((candidate) => parsedEmails.has(normalizeEmailForMatch(candidate))) || "";
      if (!matchingEmail) {
        throw new Error(`This public key does not list any of this contact's email addresses (${emailChoices.join(", ")}).`);
      }
      const validated = await pgpPlugin.publicKeyRecordFromArmored(armored, matchingEmail, "manual", matchingEmail);
      const parsedFingerprint = normalizedPGPIdentifier(parsed.fingerprint);
      const parsedKeyID = normalizedPGPIdentifier(parsed.key_id);
      const duplicate = keys.find((key) =>
        (parsedFingerprint && normalizedPGPIdentifier(key.fingerprint) === parsedFingerprint) ||
        (!parsedFingerprint && parsedKeyID && normalizedPGPIdentifier(key.key_id) === parsedKeyID)
      );
      if (duplicate) {
        throw new Error(`This public key is already saved for ${duplicate.email || matchingEmail}.`);
      }
      const hasPreferredForEmail = keys.some((key) => normalizeEmailForMatch(key.email) === normalizeEmailForMatch(matchingEmail) && key.is_preferred);
      const saved = await pgpPlugin.savePublicKey(csrf, { ...validated, contact_id: contactID, email: matchingEmail, is_preferred: !hasPreferredForEmail });
      setKeys((current) => [...current.filter((key) => key.id !== saved.key.id), saved.key]);
      setImportOpen(false);
      addToast("PGP public key added.");
    } catch (err) {
      const message = messageFromError(err);
      addToast(message, "error");
      throw new Error(message);
    } finally {
      setAdding(false);
    }
  }

  async function preferKey(index: number) {
    const selected = keys[index];
    if (!selected) return;
    try {
      const saved = await pgpPlugin.savePublicKey(csrf, { ...selected, is_preferred: true });
      setKeys((current) => current.map((key) => ({
        ...key,
        is_preferred: key.email.toLowerCase() === saved.key.email.toLowerCase() ? key.id === saved.key.id : key.is_preferred
      })));
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function removeKey(index: number) {
    const selected = keys[index];
    if (!selected?.id) return;
    try {
      await pgpPlugin.deletePublicKey(csrf, selected.id);
      setKeys((current) => removeAt(current, index));
      addToast("PGP public key removed.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  const KeyImportModal = pgpPlugin.KeyImportModal;

  return (
    <section className="contact-section contact-pgp-section">
      <div>
        <h2>PGP public keys</h2>
      </div>
      {loading ? <div className="muted">Loading PGP public keys...</div> : null}
      {!loading && keys.length === 0 ? <div className="muted">No PGP public keys saved for this contact.</div> : null}
      <div className="contact-pgp-key-list">
        {keys.map((key, index) => (
          <div className="contact-pgp-key-row" key={`${key.fingerprint || key.key_id || index}:${index}`}>
            <Icon name="lock" />
            <div className="contact-pgp-key-main">
              <div className="contact-pgp-key-title">
                <strong>{keyDisplayLabel(key)}</strong>
              </div>
              <div className="contact-pgp-key-meta">
                {keyFingerprintLabel(key) ? <span>{keyFingerprintLabel(key)}</span> : null}
                {firstKeyUserID(key.user_ids) ? <span>{firstKeyUserID(key.user_ids)}</span> : null}
                {keySourceLabel(key) ? <span className="contact-pgp-key-source">{keySourceLabel(key)}</span> : null}
              </div>
            </div>
            <div className="contact-pgp-key-controls">
              <label className="primary-toggle"><input type="radio" checked={key.is_preferred} onChange={() => void preferKey(index)} /> Preferred</label>
              <button className="ghost icon-only" type="button" title="Remove" onClick={() => void removeKey(index)}><Icon name="close" /></button>
            </div>
          </div>
        ))}
      </div>
      <div className="contact-pgp-import">
        <button className="secondary" type="button" disabled={adding || !contactID || emailChoices.length === 0} onClick={() => setImportOpen(true)}>
          {adding ? "Reading key..." : "Import public key"}
        </button>
        {!contactID ? <span className="muted">Save this contact first.</span> : emailChoices.length === 0 ? <span className="muted">Add at least one email first.</span> : <span className="muted">The key must list one of this contact's email addresses.</span>}
      </div>
      {importOpen ? (
        <KeyImportModal
          title="Import public key"
          description="Paste, drop, or choose an ASCII-armored public key. Rolltop will match it against this contact's email addresses."
          placeholder="-----BEGIN PGP PUBLIC KEY BLOCK-----"
          importLabel="Add key"
          busy={adding}
          onCancel={() => { if (!adding) setImportOpen(false); }}
          onImport={(armored) => addKey(armored)}
        />
      ) : null}
    </section>
  );
}

function keyDisplayLabel(key: ContactPGPKey): string {
  const label = (key.label || "").trim();
  const fingerprint = shortFingerprint(key.fingerprint || key.key_id);
  return label || fingerprint || "PGP key";
}

function keyFingerprintLabel(key: ContactPGPKey): string {
  return shortFingerprint(key.fingerprint || key.key_id);
}

function keySourceLabel(key: ContactPGPKey): string {
  const kind = (key.source_kind || "").trim().toLowerCase();
  const detail = (key.source_detail || "").trim();
  if (!kind && !detail) return "";
  const label = kind ? kind.replace(/[-_]+/g, " ") : "source";
  return detail ? `${label}: ${detail}` : label;
}

function shortFingerprint(value: string): string {
  const clean = value.replace(/\s+/g, "");
  if (clean.length <= 16) return clean;
  return `${clean.slice(0, 8)}...${clean.slice(-8)}`;
}

function normalizedPGPIdentifier(value: string): string {
  return value.replace(/[\s:]/g, "").toUpperCase();
}

function normalizeEmailForMatch(value: string): string {
  return value.trim().toLowerCase();
}

function firstKeyUserID(value: string): string {
  return value.split(/\r?\n/).map((item) => item.trim()).find(Boolean) || "";
}

function removeAt<T>(items: T[], index: number): T[] {
  return items.filter((_item, itemIndex) => itemIndex !== index);
}
