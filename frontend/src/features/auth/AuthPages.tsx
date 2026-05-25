import { useState } from "react";
import type { FormEvent } from "react";
import { api } from "../../api";
import type { Bootstrap } from "../../types";
import { messageFromError } from "../../lib/errors";

export function SetupPage({
  csrf,
  onReady,
  navigate
}: {
  csrf: string;
  onReady: () => Promise<Bootstrap | null>;
  navigate: (url: string) => void;
}) {
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.setup(csrf, { email, name, password });
      await onReady();
      navigate("/settings/account/imap/new");
    } catch (err) {
      setError(messageFromError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="auth-page">
      <div className="auth-brand">mailmirror</div>
      <form className="panel" onSubmit={submit}>
        <h1>First-run setup</h1>
        {error ? <div className="error">{error}</div> : null}
        <div className="grid">
          <div>
            <label>Email</label>
            <input type="email" value={email} onChange={(event) => setEmail(event.target.value)} required />
          </div>
          <div>
            <label>Name</label>
            <input type="text" value={name} onChange={(event) => setName(event.target.value)} />
          </div>
        </div>
        <div>
          <label>Password</label>
          <input
            type="password"
            value={password}
            minLength={12}
            onChange={(event) => setPassword(event.target.value)}
            required
          />
        </div>
        <div className="actions">
          <button disabled={busy}>{busy ? "Creating..." : "Create admin"}</button>
        </div>
      </form>
    </main>
  );
}

export function LoginPage({
  csrf,
  onReady,
  navigate
}: {
  csrf: string;
  onReady: () => Promise<Bootstrap | null>;
  navigate: (url: string) => void;
}) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api.login(csrf, { email, password });
      await onReady();
      navigate("/mail");
    } catch (err) {
      setError(messageFromError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="auth-page">
      <div className="auth-brand">mailmirror</div>
      <form className="panel" onSubmit={submit}>
        <h1>Sign in</h1>
        {error ? <div className="error">{error}</div> : null}
        <div>
          <label>Email</label>
          <input type="email" value={email} onChange={(event) => setEmail(event.target.value)} required />
        </div>
        <div>
          <label>Password</label>
          <input type="password" value={password} onChange={(event) => setPassword(event.target.value)} required />
        </div>
        <div className="actions">
          <button disabled={busy}>{busy ? "Signing in..." : "Sign in"}</button>
        </div>
      </form>
    </main>
  );
}
