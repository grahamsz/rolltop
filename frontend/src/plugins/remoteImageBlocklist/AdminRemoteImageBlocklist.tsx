import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { api } from "../../api";
import type { AddToast } from "../../appTypes";
import { messageFromError } from "../../lib/errors";

export function AdminRemoteImageBlocklist({
  csrf,
  addToast,
  enabled
}: {
  csrf: string;
  addToast: AddToast;
  enabled: boolean;
}) {
  const [patterns, setPatterns] = useState("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    setLoading(true);
    api
      .remoteImageBlocklist()
      .then((data) => {
        if (!cancelled) setPatterns((data.patterns || []).join("\n"));
      })
      .catch((err) => {
        if (!cancelled) addToast(messageFromError(err), "error");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [addToast, enabled]);

  async function save(event: FormEvent) {
    event.preventDefault();
    const nextPatterns = patterns
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith("#"));
    setLoading(true);
    try {
      const data = await api.saveRemoteImageBlocklist(csrf, nextPatterns);
      setPatterns((data.patterns || []).join("\n"));
      addToast("Remote image blocklist saved.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setLoading(false);
    }
  }

  if (!enabled) return null;
  return (
    <form className="panel remote-blocklist" onSubmit={save}>
      <h2>Remote image blocklist</h2>
      <div className="muted">
        One regular expression per line. Matching remote image URLs are removed when remote assets are shown.
      </div>
      <textarea
        rows={8}
        spellCheck={false}
        value={patterns}
        disabled={loading}
        onChange={(event) => setPatterns(event.target.value)}
      />
      <div className="actions">
        <button disabled={loading}>Save blocklist</button>
      </div>
    </form>
  );
}
