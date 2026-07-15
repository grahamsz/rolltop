import { useCallback, useEffect, useState } from "react";
import type { IdentityPGPPrivateKey, MailIdentity, User } from "../../../../frontend/src/types";
import type { Toast } from "../../../../frontend/src/appTypes";
import { Icon } from "../../../../frontend/src/components/Icon";
import { SettingsError, SettingsLoading } from "../../../../frontend/src/features/settings/SettingsUI";
import { messageFromError } from "../../../../frontend/src/lib/errors";
import { displayDateTime } from "../../../../frontend/src/lib/format";
import { hydrateBrowserPGPPrivateKeys } from "../storage/browserPGPKeys";
import type { ClientSidePGPPlugin } from "../types";

type PGPPrivateKeyStorage = "browser" | "server";

export function IdentityPGPSettings({
  csrf,
  user,
  identities,
  identityDraft,
  updateIdentityDraft,
  markIdentitySecurityReady,
  pgpPlugin,
  addToast
}: {
  csrf: string;
  user: User;
  identities: MailIdentity[];
  identityDraft: MailIdentity;
  updateIdentityDraft: (patch: Partial<MailIdentity>) => void;
  markIdentitySecurityReady: (identityID: number) => void;
  pgpPlugin: ClientSidePGPPlugin;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [keys, setKeys] = useState<IdentityPGPPrivateKey[]>([]);
  const [keysLoading, setKeysLoading] = useState(true);
  const [keysError, setKeysError] = useState("");
  const [storage, setStorage] = useState<PGPPrivateKeyStorage>("browser");
  const [importOpen, setImportOpen] = useState(false);
  const [generateOpen, setGenerateOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [generating, setGenerating] = useState(false);

  const loadKeys = useCallback(async () => {
    const data = await pgpPlugin.privateKeys();
    return hydrateBrowserPGPPrivateKeys(user.id, data.keys || []);
  }, [pgpPlugin, user.id]);

  useEffect(() => {
    let cancelled = false;
    setKeysLoading(true);
    setKeysError("");
    void loadKeys()
      .then((nextKeys) => {
        if (!cancelled) setKeys(nextKeys);
      })
      .catch((err) => {
        if (!cancelled) setKeysError(messageFromError(err));
      })
      .finally(() => {
        if (!cancelled) setKeysLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [loadKeys]);

  async function retryLoadKeys() {
    setKeysLoading(true);
    setKeysError("");
    try {
      setKeys(await loadKeys());
    } catch (err) {
      setKeysError(messageFromError(err));
    } finally {
      setKeysLoading(false);
    }
  }

  function passphraseValues() {
    return [user.email, user.name, identityDraft.email, identityDraft.display_name, identityDraft.email.split("@")[0] || "", identityDraft.email.split("@")[1] || ""];
  }

  async function importKey(armored: string) {
    if (!identityDraft.id) {
      throw new Error("Save the identity before adding a PGP key.");
    }
    setSaving(true);
    try {
      const parsed = await pgpPlugin.privateKeyRecordFromArmoredSource(armored);
      const matchingIdentity = identities.find((identity) => pgpPlugin.pgpUserIDsMatchEmail(parsed.user_ids, identity.email));
      if (!matchingIdentity) {
        const keyEmails = pgpPlugin.pgpUserIDEmails(parsed.user_ids);
        const detail = keyEmails.length > 0 ? ` It lists ${keyEmails.join(", ")}.` : "";
        throw new Error(`This private key is not for one of your profile email addresses.${detail}`);
      }
      if (matchingIdentity.id !== identityDraft.id) {
        throw new Error(`This private key is for ${matchingIdentity.email}. Select that identity before importing it.`);
      }
      const parsedFingerprint = normalizedPGPIdentifier(parsed.fingerprint);
      const parsedKeyID = normalizedPGPIdentifier(parsed.key_id);
      const duplicate = keys.find((key) =>
        (parsedFingerprint && normalizedPGPIdentifier(key.fingerprint) === parsedFingerprint) ||
        (!parsedFingerprint && parsedKeyID && normalizedPGPIdentifier(key.key_id) === parsedKeyID)
      );
      if (duplicate) {
        const duplicateIdentity = identities.find((identity) => identity.id === duplicate.identity_id);
        if (duplicate.private_key_storage === "browser" && !duplicate.private_key_armored?.trim()) {
          await pgpPlugin.saveBrowserPGPPrivateKey(user.id, duplicate, parsed.private_key_armored || "");
          setKeys((current) => current.map((key) => key.id === duplicate.id ? { ...key, private_key_armored: parsed.private_key_armored || "" } : key));
          setImportOpen(false);
          addToast("Browser copy restored.");
          return;
        }
        throw new Error(`This private key is already saved${duplicateIdentity?.email ? ` for ${duplicateIdentity.email}` : ""}.`);
      }
      const saved = await pgpPlugin.savePrivateKey(csrf, {
        ...parsed,
        identity_id: identityDraft.id,
        label: identityDraft.email || firstPGPUserID(parsed.user_ids) || parsed.label || "PGP key",
        private_key_armored: storage === "server" ? parsed.private_key_armored : "",
        private_key_storage: storage,
        is_active_signing: true,
        is_active_encryption: true,
        is_decrypt_only: false
      });
      if (storage === "browser") {
        try {
          await pgpPlugin.saveBrowserPGPPrivateKey(user.id, saved.key, parsed.private_key_armored || "");
        } catch (err) {
          if (saved.key.id) await pgpPlugin.deletePrivateKey(csrf, saved.key.id).catch(() => undefined);
          throw err;
        }
      }
      const firstIdentityKey = !keys.some((key) => key.identity_id === identityDraft.id);
      setKeys((current) => [...current.filter((key) => key.id !== saved.key.id), saved.key]);
      if (firstIdentityKey && saved.key.is_active_encryption && !saved.key.is_decrypt_only) {
        markIdentitySecurityReady(identityDraft.id);
      }
      setImportOpen(false);
      addToast(storage === "browser" ? "PGP private key imported in this browser." : "PGP private key imported.");
    } catch (err) {
      const message = messageFromError(err);
      addToast(message, "error");
      throw new Error(message);
    } finally {
      setSaving(false);
    }
  }

  async function generateKey(passphrase: string) {
    if (!identityDraft.id) {
      addToast("Save the identity before generating a PGP key.", "error");
      return;
    }
    const issues = pgpPlugin.pgpPassphraseIssues(passphrase, passphraseValues());
    if (issues.length > 0) {
      addToast(issues[0], "error");
      return;
    }
    setGenerating(true);
    try {
      const generated = await pgpPlugin.generatePrivateKey(identityDraft.display_name, identityDraft.email, passphrase);
      const saved = await pgpPlugin.savePrivateKey(csrf, {
        ...generated,
        identity_id: identityDraft.id,
        label: generated.label || identityDraft.email || "PGP key",
        private_key_armored: storage === "server" ? generated.private_key_armored : "",
        private_key_storage: storage,
        is_active_signing: true,
        is_active_encryption: true,
        is_decrypt_only: false
      });
      if (storage === "browser") {
        try {
          await pgpPlugin.saveBrowserPGPPrivateKey(user.id, saved.key, generated.private_key_armored || "");
        } catch (err) {
          if (saved.key.id) await pgpPlugin.deletePrivateKey(csrf, saved.key.id).catch(() => undefined);
          throw err;
        }
      }
      const firstIdentityKey = !keys.some((key) => key.identity_id === identityDraft.id);
      setKeys((current) => [...current.filter((key) => key.id !== saved.key.id), saved.key]);
      if (firstIdentityKey && saved.key.is_active_encryption && !saved.key.is_decrypt_only) {
        markIdentitySecurityReady(identityDraft.id);
      }
      setGenerateOpen(false);
      addToast(storage === "browser" ? "PGP private key generated and saved in this browser." : "PGP private key generated in this browser.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setGenerating(false);
    }
  }

  async function deleteKey(id: number) {
    const key = keys.find((item) => item.id === id);
    const storageLabel = key?.private_key_storage === "browser" ? "browser private key and server public-key metadata" : "PGP private key from rolltop";
    if (!window.confirm(`Remove this ${storageLabel}? Export it first if this is your only copy.`)) return;
    try {
      await pgpPlugin.deletePrivateKey(csrf, id);
      if (key?.private_key_storage === "browser") {
        await pgpPlugin.deleteBrowserPGPPrivateKey(user.id, id).catch(() => undefined);
      }
      setKeys((current) => current.filter((item) => item.id !== id));
      addToast("PGP private key removed.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function exportKey(key: IdentityPGPPrivateKey, kind: "private" | "public" | "revocation") {
    let data = kind === "private" ? key.private_key_armored : kind === "public" ? key.public_key_armored : key.revocation_certificate;
    if (kind === "private" && key.private_key_storage === "browser" && !data?.trim() && key.id) {
      try {
        data = await pgpPlugin.loadBrowserPGPPrivateKey(user.id, key.id);
      } catch (err) {
        addToast(messageFromError(err), "error");
        return;
      }
    }
    if (!data) {
      addToast(key.private_key_storage === "browser" ? "This private key is not saved in this browser." : "No key material available to export.", "error");
      return;
    }
    if (kind === "private" && !window.confirm([
      "Export this PGP private key?",
      "",
      "Do not send your private key or passphrase to anyone. Anyone with both can decrypt mail encrypted to you and sign mail as you.",
      "",
      "Only save it somewhere you control."
    ].join("\n"))) return;
    if (kind === "revocation" && !window.confirm([
      "Export this revocation certificate?",
      "",
      "This is the public kill switch for the key. Publish it only if the private key is lost, compromised, or retired."
    ].join("\n"))) return;
    const suffix = kind === "private" ? "private" : kind === "public" ? "public" : "publishable-revocation-certificate";
    const blob = new Blob([data], { type: "application/pgp-keys" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${identityDraft.email || "pgp-key"}-${suffix}.asc`;
    a.click();
    URL.revokeObjectURL(url);
  }

  const identityKeys = identityDraft.id ? keys.filter((key) => key.identity_id === identityDraft.id) : [];
  const KeyImportModal = pgpPlugin.KeyImportModal;
  const KeyGenerateModal = pgpPlugin.KeyGenerateModal;

  return (
    <section className="identity-pgp-settings">
      <div className="panel-headline">
        <div>
          <h3>PGP keys</h3>
          <div className="muted">Public keys are saved on the rolltop server so Autocrypt and public-key attachments work from every browser. Private keys can stay in this browser only, or be stored as a server-encrypted copy for unlock/export on other browsers. Your PGP passphrase, key unlock, and message decryption stay in this browser.</div>
        </div>
      </div>
      {!identityDraft.id ? <div className="notice subtle">Save this identity before adding PGP keys.</div> : null}
      <label className="identity-primary identity-autocrypt-toggle">
        <input type="checkbox" checked={identityDraft.autocrypt_enabled ?? true} onChange={(event) => updateIdentityDraft({ autocrypt_enabled: event.target.checked })} />
        Advertise public key with Autocrypt
      </label>
      <div className="identity-pgp-key-list" aria-busy={keysLoading}>
        {keysLoading ? <SettingsLoading compact label="Loading PGP keys..." /> : null}
        {!keysLoading && keysError ? <SettingsError compact message={keysError} onRetry={() => void retryLoadKeys()} /> : null}
        {!keysLoading && !keysError && identityKeys.length === 0 ? <div className="muted">No PGP private keys saved for this identity.</div> : null}
        {!keysLoading && !keysError ? identityKeys.map((key) => (
          <div className="identity-pgp-key-row" key={key.id || key.fingerprint}>
            <Icon name="lock" />
            <span>
              <strong>{key.label || key.fingerprint || "PGP key"}</strong>
              <small>{[
                shortPGPValue(key.fingerprint || key.key_id),
                firstPGPUserID(key.user_ids),
                key.private_key_storage === "browser" ? (key.private_key_armored?.trim() ? "Private key in this browser" : "Browser copy missing here") : "Private key server-stored",
                key.created_at ? `Imported ${displayDateTime(key.created_at, user)}` : ""
              ].filter(Boolean).join(" · ")}</small>
            </span>
            <div className="identity-pgp-key-actions">
              <details className="message-menu identity-pgp-key-menu">
                <summary className="icon-action" title="PGP key actions" aria-label="PGP key actions"><Icon name="more_vert" /></summary>
                <div className="message-menu-panel identity-pgp-key-menu-panel">
                  <button type="button" onClick={() => void exportKey(key, "public")}><Icon name="signature" /><span><strong>Export public key</strong><small>Share this so others can encrypt mail to you and verify your signatures.</small></span></button>
                  <button type="button" onClick={() => void exportKey(key, "private")}><Icon name="lock" /><span><strong>Export private key</strong><small>Danger: never send this key or its passphrase to anyone.</small></span></button>
                  {key.revocation_certificate ? <button type="button" onClick={() => void exportKey(key, "revocation")}><Icon name="report" /><span><strong>Download publishable revocation certificate</strong><small>Publish this only if the key is lost, compromised, or retired.</small></span></button> : null}
                  {key.id ? <button className="danger" type="button" onClick={() => void deleteKey(key.id || 0)}><Icon name="delete" /><span><strong>Remove saved private key</strong><small>Deletes this rolltop server copy; it does not revoke the key.</small></span></button> : null}
                </div>
              </details>
            </div>
          </div>
        )) : null}
      </div>
      {!keysLoading && !keysError ? (
        <>
          <div className="identity-pgp-storage-choice">
            <strong>Private key storage for new keys</strong>
            <label><input type="radio" checked={storage === "browser"} onChange={() => setStorage("browser")} /><span><strong>This browser only</strong><small>Best server compromise: rolltop saves the public key, while this browser keeps the private key. Other browsers must import the same private key before they can decrypt or sign.</small></span></label>
            <label><input type="radio" checked={storage === "server"} onChange={() => setStorage("server")} /><span><strong>Server-encrypted copy</strong><small>More convenient across browsers. The server stores the armored private key encrypted with the rolltop master key, and your PGP passphrase is still required in the browser.</small></span></label>
          </div>
          <div className="identity-pgp-grid">
            <section className="identity-pgp-action-card"><h4>Import private key</h4><p>Bring in an existing ASCII-armored private key from a file or pasted text.</p><button className="secondary" type="button" disabled={!identityDraft.id || saving} onClick={() => setImportOpen(true)}>{saving ? "Importing..." : "Import key"}</button></section>
            <section className="identity-pgp-action-card"><h4>Generate private key</h4><p>Create a new passphrase-protected key in this browser using the storage choice above.</p><button className="secondary" type="button" disabled={!identityDraft.id || generating} onClick={() => setGenerateOpen(true)}>{generating ? "Generating..." : "Generate key"}</button></section>
          </div>
        </>
      ) : null}
      {importOpen ? <KeyImportModal title="Import private key" description={storage === "browser" ? "Paste, drop, or choose a passphrase-protected ASCII-armored PGP private key. rolltop saves the public key on the server and keeps the private key in this browser only." : "Paste, drop, or choose a passphrase-protected ASCII-armored PGP private key. rolltop stores a server-encrypted private-key copy for unlock/export in your browsers."} placeholder="-----BEGIN PGP PRIVATE KEY BLOCK-----" busy={saving} onCancel={() => { if (!saving) setImportOpen(false); }} onImport={(armored) => importKey(armored)} /> : null}
      {generateOpen ? <KeyGenerateModal email={identityDraft.email} busy={generating} validatePassphrase={(passphrase) => pgpPlugin.pgpPassphraseIssues(passphrase, passphraseValues())} onCancel={() => { if (!generating) setGenerateOpen(false); }} onGenerate={(passphrase) => generateKey(passphrase)} /> : null}
    </section>
  );
}

function shortPGPValue(value: string): string {
  const clean = value.replace(/\s+/g, "");
  if (clean.length <= 16) return clean;
  return `${clean.slice(0, 8)}...${clean.slice(-8)}`;
}

function normalizedPGPIdentifier(value: string): string {
  return value.replace(/[\s:]/g, "").toUpperCase();
}

function firstPGPUserID(value: string): string {
  return value.split(/\r?\n/).map((item) => item.trim()).find(Boolean) || "";
}
