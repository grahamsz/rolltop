import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./Icon";
import { messageFromError } from "../lib/errors";

type PGPKeyGenerateModalProps = {
  email: string;
  busy?: boolean;
  validatePassphrase: (passphrase: string) => string[];
  onGenerate: (passphrase: string) => Promise<void> | void;
  onCancel: () => void;
};

export function PGPKeyGenerateModal({
  email,
  busy = false,
  validatePassphrase,
  onGenerate,
  onCancel
}: PGPKeyGenerateModalProps) {
  const [passphrase, setPassphrase] = useState("");
  const [confirm, setConfirm] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const [error, setError] = useState("");
  const issues = useMemo(() => validatePassphrase(passphrase), [passphrase, validatePassphrase]);
  const mismatch = confirm.length > 0 && passphrase !== confirm;
  const visibleIssues = submitted || passphrase.length > 0 ? issues : [];

  async function submit(event: FormEvent) {
    event.preventDefault();
    setSubmitted(true);
    setError("");
    if (passphrase !== confirm) {
      setError("PGP passphrases do not match.");
      return;
    }
    if (issues.length > 0 || busy) return;
    try {
      await onGenerate(passphrase);
    } catch (err) {
      setError(messageFromError(err));
    }
  }

  const dialog = (
    <div className="confirm-backdrop pgp-import-backdrop" role="presentation" onClick={onCancel}>
      <form className="confirm-dialog pgp-generate-dialog" role="dialog" aria-modal="true" aria-label="Generate PGP key" onClick={(event) => event.stopPropagation()} onSubmit={(event) => void submit(event)}>
        <div className="pgp-import-heading">
          <div>
            <h2>Generate PGP key</h2>
            <p>
              This passphrase protects the new private key for {email || "this identity"}. rolltop stores a server-encrypted copy and sends it
              back to this browser for unlock/export. Do not reuse your rolltop password.
            </p>
          </div>
          <button className="icon-action" type="button" title="Close" aria-label="Close" onClick={onCancel}>
            <Icon name="close" />
          </button>
        </div>
        <div className="pgp-passphrase-fields">
          <label>Passphrase<input type="password" value={passphrase} autoComplete="new-password" onChange={(event) => setPassphrase(event.target.value)} /></label>
          <label>Confirm passphrase<input type="password" value={confirm} autoComplete="new-password" onChange={(event) => setConfirm(event.target.value)} /></label>
        </div>
        <div className="pgp-passphrase-rules">
          <strong>Use a new passphrase that you can remember.</strong>
          <span>Minimum 14 characters. Avoid your name, email, domain, obvious words, and your rolltop password.</span>
          {visibleIssues.length > 0 ? (
            <ul>
              {visibleIssues.map((issue) => <li key={issue}>{issue}</li>)}
            </ul>
          ) : null}
          {mismatch ? <span className="pgp-passphrase-warning">Passphrases do not match.</span> : null}
        </div>
        {error ? <div className="notice error">{error}</div> : null}
        <div className="pgp-import-actions">
          <button className="secondary" type="button" onClick={onCancel}>Cancel</button>
          <button type="submit" disabled={busy || !passphrase || !confirm || issues.length > 0 || passphrase !== confirm}>{busy ? "Generating..." : "Generate key"}</button>
        </div>
      </form>
    </div>
  );
  return typeof document === "undefined" ? dialog : createPortal(dialog, document.body);
}
