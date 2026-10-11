import { useEffect, useRef, useState } from "react";
import { toASCII } from "punycode/";
import { api } from "../../api";
import type { DiscoveredMailSettings } from "../../types";
import { messageFromError } from "../../lib/errors";

export function MailServerDiscovery({ csrf, email, kind, onApply }: {
  csrf: string;
  email: string;
  kind: "imap" | "smtp" | "both";
  onApply: (settings: DiscoveredMailSettings) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<DiscoveredMailSettings | null>(null);
  const [message, setMessage] = useState("");
  const requestID = useRef(0);
  const currentEmail = useRef(email);
  currentEmail.current = email;

  useEffect(() => {
    requestID.current++;
    setBusy(false);
    setResult(null);
    setMessage("");
    return () => { requestID.current++; };
  }, [email, kind]);

  async function discover() {
    const id = ++requestID.current;
    const requestedEmail = email;
    setBusy(true);
    setMessage("");
    setResult(null);
    try {
      const address = email.trim();
      const at = address.lastIndexOf("@");
      const normalized = at >= 0 ? address.slice(0, at + 1) + toASCII(address.slice(at + 1)) : address;
      const found = await api.discoverMailSettings(csrf, normalized);
      if (id !== requestID.current || requestedEmail !== currentEmail.current) return;
      const settings = {
        imap: kind === "smtp" ? undefined : found.imap,
        smtp: kind === "imap" ? undefined : found.smtp
      };
      if (!settings.imap && !settings.smtp) {
        setMessage("No compatible secure server settings found. You can enter the settings manually.");
      } else {
        setResult(settings);
      }
    } catch (err) {
      if (id === requestID.current) setMessage(messageFromError(err));
    } finally {
      if (id === requestID.current) setBusy(false);
    }
  }

  return <div className="mail-server-discovery">
    <button className="secondary" type="button" disabled={busy || !email.trim()} onClick={discover}>
      {busy ? "Finding server settings…" : "Find server settings"}
    </button>
    <p className="muted">Looks up your email provider’s settings. Your email address may be sent to its discovery service; passwords are never sent.</p>
    <div aria-live="polite">
      {message && <p>{message}</p>}
      {result && <>
        {(["imap", "smtp"] as const).map((protocol) => {
          const server = result[protocol];
          return server ? <p key={protocol}>
            <strong>{protocol === "imap" ? "Incoming IMAP" : "Outgoing SMTP"}</strong><br />
            {server.host}:{server.port} · {server.security === "tls" ? "TLS" : "STARTTLS"}<br />
            Username: {server.username}<br /><span className="muted">{server.source}</span>
          </p> : null;
        })}
        {kind === "both" && (!result.imap || !result.smtp) && <p>Only {result.imap ? "incoming" : "outgoing"} settings were found. Enter the remaining settings manually.</p>}
        <button className="secondary" type="button" onClick={() => {
          onApply(result);
          setResult(null);
          setMessage("Settings filled in. Review them and save when ready.");
        }}>Use these settings</button>
      </>}
    </div>
  </div>;
}
