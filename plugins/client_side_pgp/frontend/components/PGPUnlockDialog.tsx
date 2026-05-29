import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { messageFromError } from "../../../../frontend/src/lib/errors";
import type { IdentityPGPPrivateKey } from "../../../../frontend/src/types";
import { pgpPrivateKeys } from "../api/keys";
import { matchingPGPPrivateKeyIDForRecipients, pgpUserIDsMatchEmail, unlockPrivateKey } from "../crypto/pgp";
import { hydrateBrowserPGPPrivateKeys } from "../storage/browserPGPKeys";
import type { PGPUnlockDialogProps } from "../types";

export function PGPUnlockDialog({
  userID,
  identityID,
  recipientKeyIDs,
  fallbackEmail,
  onClose,
  onUnlocked,
  addToast
}: PGPUnlockDialogProps) {
  const [keys, setKeys] = useState<IdentityPGPPrivateKey[]>([]);
  const [selectedID, setSelectedID] = useState(0);
  const [passphrase, setPassphrase] = useState("");
  const [durationMinutes, setDurationMinutes] = useState(30);
  const [loading, setLoading] = useState(true);
  const [unlocking, setUnlocking] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    pgpPrivateKeys()
      .then((data) => {
        if (cancelled) return;
        return hydrateBrowserPGPPrivateKeys(userID, data.keys || []);
      })
      .then((list) => {
        if (cancelled || !list) return;
        const preferred = identityID ? list.find((key) => key.identity_id === identityID) : null;
        const preferredByEmail = fallbackEmail
          ? list.find((key) => pgpUserIDsMatchEmail(key.user_ids || "", fallbackEmail))
          : null;
        setKeys(list);
        if (recipientKeyIDs.length === 0) {
          setSelectedID(preferred?.id || preferredByEmail?.id || list[0]?.id || 0);
          return;
        }
        return matchingPGPPrivateKeyIDForRecipients(list, recipientKeyIDs).then((matchedID) => {
          if (cancelled) return;
          setSelectedID(matchedID || preferred?.id || preferredByEmail?.id || list[0]?.id || 0);
        });
      })
      .catch((err) => {
        if (!cancelled) setError(messageFromError(err));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [fallbackEmail, identityID, recipientKeyIDs, userID]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const key = keys.find((item) => item.id === selectedID);
    if (!key) return;
    setUnlocking(true);
    setError("");
    try {
      if (key.private_key_storage === "browser" && !key.private_key_armored?.trim()) {
        throw new Error("This private key is saved in another browser. Import it here, or save a server-encrypted copy from the browser that has it.");
      }
      const unlocked = await unlockPrivateKey(key, passphrase);
      onUnlocked({ unlockedUntil: Date.now() + durationMinutes * 60_000, keys: [unlocked] });
      addToast("PGP key unlocked.");
      onClose();
    } catch (err) {
      setError(messageFromError(err));
    } finally {
      setUnlocking(false);
    }
  }

  return (
    <div className="pgp-unlock-backdrop" role="presentation" onClick={onClose}>
      <form className="pgp-unlock-dialog" role="dialog" aria-label="Unlock PGP key" onSubmit={submit} onClick={(event) => event.stopPropagation()}>
        <div className="pgp-unlock-head">
          <strong>Unlock PGP key</strong>
          <button className="ghost" type="button" title="Close" onClick={onClose}>Close</button>
        </div>
        {loading ? <div className="muted">Loading keys...</div> : null}
        {error ? <div className="error">{error}</div> : null}
        {!loading && keys.length === 0 ? <div className="muted">Add a PGP private key on an identity first.</div> : null}
        {keys.length > 0 ? (
          <>
            <div className="notice subtle">Server-stored keys are sent here for unlock. Browser-only keys unlock only in browsers where you saved the private key. Your PGP passphrase is used only in this browser and is not sent to the server.</div>
            <label>
              Key
              <select value={selectedID} onChange={(event) => setSelectedID(Number(event.target.value))}>
                {keys.map((key) => <option key={key.id} value={key.id}>{key.label || key.fingerprint}{key.private_key_storage === "browser" && !key.private_key_armored ? " (not in this browser)" : ""}</option>)}
              </select>
            </label>
            <label>
              Passphrase
              <input type="password" value={passphrase} autoFocus onChange={(event) => setPassphrase(event.target.value)} />
            </label>
            <label>
              Keep unlocked
              <select value={durationMinutes} onChange={(event) => setDurationMinutes(Number(event.target.value))}>
                <option value={15}>15 minutes</option>
                <option value={30}>30 minutes</option>
                <option value={60}>1 hour</option>
              </select>
            </label>
            <button disabled={unlocking || !passphrase}>{unlocking ? "Unlocking..." : "Unlock"}</button>
          </>
        ) : null}
      </form>
    </div>
  );
}
