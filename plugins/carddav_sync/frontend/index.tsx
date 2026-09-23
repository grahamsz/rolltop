// File overview: Runtime settings UI for generic CardDAV contact sync routines.

import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import type { Toast } from "../../../frontend/src/appTypes";
import { Icon } from "../../../frontend/src/components/Icon";
import { SettingsEmpty, SettingsError, SettingsLoading, SettingsPage } from "../../../frontend/src/features/settings/SettingsUI";
import { displayDateTime } from "../../../frontend/src/lib/format";
import type { AccountSettingsRuntimePlugin } from "../../../frontend/src/plugins/runtime";
import type { Mailbox, User } from "../../../frontend/src/types";
import "./styles.css";

const apiBase = "/api/plugins/carddav_sync";

type SettingsContext = {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  navigate: (url: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
};

type CardDAVRun = {
  id: number;
  routine_id: number;
  trigger: string;
  status: string;
  scanned: number;
  added: number;
  updated: number;
  deleted: number;
  error: string;
  started_at: number;
  completed_at: number;
  created_at: number;
};

type CardDAVRoutine = {
  id: number;
  name: string;
  enabled: boolean;
  server_url: string;
  username: string;
  has_password: boolean;
  addressbook_url: string;
  poll_interval_minutes: number;
  state: string;
  last_error: string;
  synced_total: number;
  last_started_at: number;
  last_completed_at: number;
  last_run: CardDAVRun | null;
};

type CardDAVAddressBook = {
  url: string;
  display_name: string;
};

type RoutineDraft = {
  id: number;
  name: string;
  enabled: boolean;
  server_url: string;
  username: string;
  password: string;
  has_password: boolean;
  addressbook_url: string;
  poll_interval_minutes: string;
};

function numberOr(value: unknown, fallback = 0): number {
  const n = typeof value === "string" ? Number(value) : value;
  return typeof n === "number" && Number.isFinite(n) ? n : fallback;
}

function dateString(value: unknown): string {
  const n = numberOr(value);
  if (!n) return "";
  return new Date(n < 1_000_000_000_000 ? n * 1000 : n).toISOString();
}

function normalizeRun(value: any): CardDAVRun | null {
  if (!value || !value.id) return null;
  return {
    id: numberOr(value.id),
    routine_id: numberOr(value.routine_id),
    trigger: value.trigger || "",
    status: value.status || "",
    scanned: numberOr(value.scanned),
    added: numberOr(value.added),
    updated: numberOr(value.updated),
    deleted: numberOr(value.deleted),
    error: value.error || "",
    started_at: numberOr(value.started_at),
    completed_at: numberOr(value.completed_at),
    created_at: numberOr(value.created_at)
  };
}

function normalizeRoutine(value: any): CardDAVRoutine {
  return {
    id: numberOr(value.id),
    name: value.name || "CardDAV sync",
    enabled: Boolean(value.enabled),
    server_url: value.server_url || "",
    username: value.username || "",
    has_password: Boolean(value.has_password),
    addressbook_url: value.addressbook_url || "",
    poll_interval_minutes: numberOr(value.poll_interval_minutes, 15),
    state: value.state || "",
    last_error: value.last_error || "",
    synced_total: numberOr(value.synced_total),
    last_started_at: numberOr(value.last_started_at),
    last_completed_at: numberOr(value.last_completed_at),
    last_run: normalizeRun(value.last_run)
  };
}

function blankDraft(): RoutineDraft {
  return {
    id: 0,
    name: "",
    enabled: true,
    server_url: "https://",
    username: "",
    password: "",
    has_password: false,
    addressbook_url: "",
    poll_interval_minutes: "15"
  };
}

function stateLabel(state: string): string {
  switch (state) {
    case "syncing": return "Syncing";
    case "watching": return "Watching";
    case "queued": return "Queued";
    case "retrying": return "Retrying";
    case "error": return "Error";
    case "paused": return "Paused";
    case "needs_credentials": return "Needs credentials";
    default: return state || "Unknown";
  }
}

function CardDAVSyncSettings({ csrf, user, mailboxes, navigate, addToast }: SettingsContext) {
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [routines, setRoutines] = useState<CardDAVRoutine[]>([]);
  const [draft, setDraft] = useState<RoutineDraft | null>(null);
  const [addressBooks, setAddressBooks] = useState<CardDAVAddressBook[]>([]);
  const [discoverError, setDiscoverError] = useState("");
  const [formError, setFormError] = useState("");
  const [expandedRuns, setExpandedRuns] = useState<Record<number, CardDAVRun[]>>({});
  const [busyAction, setBusyAction] = useState("");

  const request = useCallback(async (path: string, init: RequestInit = {}) => {
    const response = await fetch(`${apiBase}${path}`, {
      ...init,
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf, ...(init.headers || {}) }
    });
    let body: any = null;
    try {
      body = await response.json();
    } catch {
      body = null;
    }
    if (!response.ok) {
      throw new Error(body?.error || `Request failed (${response.status})`);
    }
    return body;
  }, [csrf]);

  const loadRoutines = useCallback(async () => {
    setLoading(true);
    setLoadError("");
    try {
      const body = await request("/routines", { headers: { "X-CSRF-Token": csrf } });
      setRoutines((body.routines || []).map(normalizeRoutine));
    } catch (error) {
      setLoadError(error instanceof Error ? error.message : "Could not load CardDAV routines.");
    } finally {
      setLoading(false);
    }
  }, [request, csrf]);

  useEffect(() => {
    void loadRoutines();
  }, [loadRoutines]);

  const updateDraft = (patch: Partial<RoutineDraft>) => {
    setDraft((current) => (current ? { ...current, ...patch } : current));
    setFormError("");
  };

  const startNew = () => {
    setDraft(blankDraft());
    setAddressBooks([]);
    setDiscoverError("");
    setFormError("");
  };

  const editRoutine = (routine: CardDAVRoutine) => {
    setDraft({
      id: routine.id,
      name: routine.name,
      enabled: routine.enabled,
      server_url: routine.server_url,
      username: routine.username,
      password: "",
      has_password: routine.has_password,
      addressbook_url: routine.addressbook_url,
      poll_interval_minutes: String(routine.poll_interval_minutes || 15)
    });
    setAddressBooks([]);
    setDiscoverError("");
    setFormError("");
  };

  const discover = async () => {
    if (!draft) return;
    setBusyAction("discover");
    setDiscoverError("");
    try {
      const body = await request("/routines/discover", {
        method: "POST",
        body: JSON.stringify({
          server_url: draft.server_url,
          username: draft.username,
          password: draft.password
        })
      });
      const books: CardDAVAddressBook[] = (body.address_books || []).map((book: any) => ({
        url: String(book.url || ""),
        display_name: String(book.display_name || book.url || "")
      }));
      setAddressBooks(books);
      if (books.length === 1) {
        updateDraft({ addressbook_url: books[0].url });
      }
      if (books.length === 0) {
        setDiscoverError("No address books found on this server.");
      }
    } catch (error) {
      setDiscoverError(error instanceof Error ? error.message : "Discovery failed.");
    } finally {
      setBusyAction("");
    }
  };

  const saveRoutine = async (event: FormEvent) => {
    event.preventDefault();
    if (!draft) return;
    const pollMinutes = Number(draft.poll_interval_minutes);
    if (!draft.addressbook_url) {
      setFormError("Discover the server first and choose an address book.");
      return;
    }
    if (!Number.isFinite(pollMinutes) || pollMinutes < 5 || pollMinutes > 1440) {
      setFormError("Poll interval must be between 5 and 1440 minutes.");
      return;
    }
    setBusyAction("save");
    setFormError("");
    try {
      const payload = {
        name: draft.name.trim(),
        enabled: draft.enabled,
        server_url: draft.server_url.trim(),
        username: draft.username.trim(),
        password: draft.password,
        addressbook_url: draft.addressbook_url.trim(),
        poll_interval_minutes: Math.round(pollMinutes)
      };
      const body = draft.id
        ? await request(`/routines/${draft.id}`, { method: "PUT", body: JSON.stringify(payload) })
        : await request("/routines", { method: "POST", body: JSON.stringify(payload) });
      const saved = normalizeRoutine(body.routine);
      setRoutines((current) => draft.id
        ? current.map((item) => (item.id === saved.id ? saved : item))
        : [...current, saved]);
      setDraft(null);
      addToast(draft.id ? "CardDAV routine saved." : "CardDAV routine created.", "success");
    } catch (error) {
      setFormError(error instanceof Error ? error.message : "Could not save the routine.");
    } finally {
      setBusyAction("");
    }
  };

  const deleteRoutine = async (id: number) => {
    if (!window.confirm("Delete this CardDAV routine? Contacts already synced stay in Rolltop.")) return;
    setBusyAction(`delete-${id}`);
    try {
      await request(`/routines/${id}`, { method: "DELETE" });
      setRoutines((current) => current.filter((item) => item.id !== id));
      addToast("CardDAV routine deleted.", "success");
    } catch (error) {
      addToast(error instanceof Error ? error.message : "Could not delete the routine.", "error");
    } finally {
      setBusyAction("");
    }
  };

  const toggleEnabled = async (routine: CardDAVRoutine) => {
    setBusyAction(`toggle-${routine.id}`);
    try {
      const body = await request(`/routines/${routine.id}`, {
        method: "PUT",
        body: JSON.stringify({
          name: routine.name,
          enabled: !routine.enabled,
          server_url: routine.server_url,
          username: routine.username,
          password: "",
          addressbook_url: routine.addressbook_url,
          poll_interval_minutes: routine.poll_interval_minutes
        })
      });
      const saved = normalizeRoutine(body.routine);
      setRoutines((current) => current.map((item) => (item.id === saved.id ? saved : item)));
    } catch (error) {
      addToast(error instanceof Error ? error.message : "Could not update the routine.", "error");
    } finally {
      setBusyAction("");
    }
  };

  const triggerSync = async (routine: CardDAVRoutine) => {
    setBusyAction(`sync-${routine.id}`);
    try {
      await request(`/routines/${routine.id}/sync`, { method: "POST" });
      addToast("Sync queued.", "success");
    } catch (error) {
      addToast(error instanceof Error ? error.message : "Could not start the sync.", "error");
    } finally {
      setBusyAction("");
    }
  };

  const testRoutine = async (routine: CardDAVRoutine) => {
    setBusyAction(`test-${routine.id}`);
    try {
      const body = await request(`/routines/${routine.id}/test`, { method: "POST" });
      const count = (body.address_books || []).length;
      addToast(count ? `Connected. Found ${count} address book${count === 1 ? "" : "s"}.` : "Connected, but no address books were found.", "success");
    } catch (error) {
      addToast(error instanceof Error ? error.message : "Connection test failed.", "error");
    } finally {
      setBusyAction("");
    }
  };

  const toggleRuns = async (routine: CardDAVRoutine) => {
    if (expandedRuns[routine.id]) {
      setExpandedRuns((current) => {
        const next = { ...current };
        delete next[routine.id];
        return next;
      });
      return;
    }
    setBusyAction(`runs-${routine.id}`);
    try {
      const body = await request(`/routines/${routine.id}/runs`, { headers: { "X-CSRF-Token": csrf } });
      setExpandedRuns((current) => ({
        ...current,
        [routine.id]: (body.runs || []).map(normalizeRun).filter(Boolean) as CardDAVRun[]
      }));
    } catch (error) {
      addToast(error instanceof Error ? error.message : "Could not load run history.", "error");
    } finally {
      setBusyAction("");
    }
  };

  return (
    <SettingsPage title="CardDAV sync" description="Pull contacts from any CardDAV address book into Rolltop.">
      {loading ? <SettingsLoading /> : null}
      {loadError && !loading ? <SettingsError message={loadError} onRetry={() => void loadRoutines()} /> : null}
      {!loading && !loadError ? (
        <>
          <section className="carddav-sync-section">
            <div className="carddav-sync-section-head">
              <h2>Routines</h2>
              <button type="button" className="secondary" onClick={startNew}>
                <Icon name="plus" />New routine
              </button>
            </div>
            {routines.length === 0 ? (
              <SettingsEmpty title="No CardDAV routines yet" message="Add a routine to start pulling contacts from a CardDAV server such as Nextcloud." />
            ) : (
              <ul className="carddav-sync-routine-list">
                {routines.map((routine) => (
                  <li key={routine.id} className="carddav-sync-routine">
                    <div className="carddav-sync-routine-main">
                      <div className="carddav-sync-routine-title">
                        <strong>{routine.name}</strong>
                        <span className={`carddav-sync-state carddav-sync-state-${routine.state || "unknown"}`}>{stateLabel(routine.state)}</span>
                        {!routine.enabled ? <span className="carddav-sync-state carddav-sync-state-disabled">Disabled</span> : null}
                      </div>
                      <div className="carddav-sync-routine-meta">
                        <span>{routine.username}@{routine.server_url.replace(/^https?:\/\//, "")}</span>
                        <span>{routine.synced_total} contacts synced</span>
                        {routine.last_completed_at ? <span>Last sync {displayDateTime(dateString(routine.last_completed_at), user)}</span> : <span>Never synced</span>}
                      </div>
                      {routine.last_error ? <p className="carddav-sync-error"><Icon name="alert" />{routine.last_error}</p> : null}
                      {expandedRuns[routine.id] ? (
                        <div className="carddav-sync-runs">
                          {expandedRuns[routine.id].length === 0 ? (
                            <p className="carddav-sync-runs-empty">No sync runs yet.</p>
                          ) : (
                            <table className="carddav-sync-runs-table">
                              <thead>
                                <tr>
                                  <th>Started</th>
                                  <th>Trigger</th>
                                  <th>Status</th>
                                  <th>Added</th>
                                  <th>Updated</th>
                                  <th>Deleted</th>
                                </tr>
                              </thead>
                              <tbody>
                                {expandedRuns[routine.id].map((run) => (
                                  <tr key={run.id}>
                                    <td>{run.started_at ? displayDateTime(dateString(run.started_at), user) : "—"}</td>
                                    <td>{run.trigger || "—"}</td>
                                    <td>{run.status}{run.error ? ` · ${run.error}` : ""}</td>
                                    <td>{run.added}</td>
                                    <td>{run.updated}</td>
                                    <td>{run.deleted}</td>
                                  </tr>
                                ))}
                              </tbody>
                            </table>
                          )}
                        </div>
                      ) : null}
                    </div>
                    <div className="carddav-sync-routine-actions">
                      <button type="button" className="secondary" disabled={busyAction === `sync-${routine.id}` || !routine.enabled} onClick={() => void triggerSync(routine)} title={routine.enabled ? "Sync now" : "Enable the routine first"}>
                        <Icon name="sync" />{busyAction === `sync-${routine.id}` ? "Queuing..." : "Sync now"}
                      </button>
                      <button type="button" className="secondary" disabled={busyAction === `runs-${routine.id}`} onClick={() => void toggleRuns(routine)}>
                        {expandedRuns[routine.id] ? "Hide history" : "History"}
                      </button>
                      <button type="button" className="secondary" disabled={busyAction === `test-${routine.id}`} onClick={() => void testRoutine(routine)}>
                        {busyAction === `test-${routine.id}` ? "Testing..." : "Test"}
                      </button>
                      <button type="button" className="secondary" onClick={() => editRoutine(routine)}>Edit</button>
                      <button type="button" className="secondary" disabled={busyAction === `toggle-${routine.id}`} onClick={() => void toggleEnabled(routine)}>
                        {routine.enabled ? "Disable" : "Enable"}
                      </button>
                      <button type="button" className="secondary danger" disabled={busyAction === `delete-${routine.id}`} onClick={() => void deleteRoutine(routine.id)}>
                        {busyAction === `delete-${routine.id}` ? "Deleting..." : "Delete"}
                      </button>
                    </div>
                  </li>
                ))}
              </ul>
            )}
          </section>

          {draft ? (
            <section className="carddav-sync-section">
              <h2>{draft.id ? "Edit routine" : "New routine"}</h2>
              <form className="carddav-sync-form" onSubmit={(event) => void saveRoutine(event)}>
                {formError ? <p className="carddav-sync-error"><Icon name="alert" />{formError}</p> : null}
                <fieldset>
                  <legend>Connection</legend>
                  <div className="carddav-sync-form-grid">
                    <label>
                      <span className="settings-field-label">Name</span>
                      <input type="text" value={draft.name} onChange={(event) => updateDraft({ name: event.target.value })} placeholder="Nextcloud contacts" required maxLength={100} />
                    </label>
                    <label>
                      <span className="settings-field-label">Server URL</span>
                      <input type="url" value={draft.server_url} onChange={(event) => updateDraft({ server_url: event.target.value })} placeholder="https://cloud.example.com" required />
                      <small>The base URL of the CardDAV server. HTTPS is required except on localhost.</small>
                    </label>
                    <label>
                      <span className="settings-field-label">Username</span>
                      <input type="text" value={draft.username} onChange={(event) => updateDraft({ username: event.target.value })} autoComplete="username" required />
                    </label>
                    <label>
                      <span className="settings-field-label">Password or app password</span>
                      <input type="password" value={draft.password} onChange={(event) => updateDraft({ password: event.target.value })} autoComplete="new-password" required={!draft.id && !draft.has_password} placeholder={draft.has_password ? "Leave blank to keep the saved password" : ""} />
                      {draft.has_password && !draft.password ? <small className="carddav-sync-password-saved"><Icon name="lock" />Password saved</small> : null}
                    </label>
                    <div className="carddav-sync-discover-field">
                      <button className="secondary" type="button" disabled={busyAction === "discover" || !draft.server_url || !draft.username || !draft.password} onClick={() => void discover()}>
                        <Icon name="sync" />{busyAction === "discover" ? "Discovering..." : "Discover address books"}
                      </button>
                      {discoverError ? <small className="carddav-sync-field-error">{discoverError}</small> : null}
                    </div>
                    <label>
                      <span className="settings-field-label">Address book</span>
                      <select value={draft.addressbook_url} onChange={(event) => updateDraft({ addressbook_url: event.target.value })} required disabled={addressBooks.length === 0}>
                        <option value="" disabled>{addressBooks.length ? "Choose an address book" : "Discover the server first"}</option>
                        {addressBooks.map((book) => (
                          <option value={book.url} key={book.url}>{book.display_name}</option>
                        ))}
                        {draft.id && draft.addressbook_url && !addressBooks.some((book) => book.url === draft.addressbook_url) ? (
                          <option value={draft.addressbook_url}>Current: {draft.addressbook_url}</option>
                        ) : null}
                      </select>
                    </label>
                  </div>
                </fieldset>

                <fieldset>
                  <legend>Schedule</legend>
                  <div className="carddav-sync-form-grid">
                    <label>
                      <span className="settings-field-label">Poll interval (minutes)</span>
                      <input type="number" min={5} max={1440} value={draft.poll_interval_minutes} onChange={(event) => updateDraft({ poll_interval_minutes: event.target.value })} required />
                      <small>How often Rolltop checks the address book for changes.</small>
                    </label>
                    <label className="carddav-sync-checkbox">
                      <input type="checkbox" checked={draft.enabled} onChange={(event) => updateDraft({ enabled: event.target.checked })} />
                      <span>Enabled</span>
                    </label>
                  </div>
                </fieldset>

                <div className="actions carddav-sync-form-actions">
                  <button type="submit" disabled={busyAction === "save"}>
                    <Icon name="sync" />{busyAction === "save" ? "Saving..." : draft.id ? "Save routine" : "Create routine"}
                  </button>
                  <button className="secondary" type="button" onClick={() => setDraft(null)}>Cancel</button>
                </div>
              </form>
            </section>
          ) : null}
        </>
      ) : null}
    </SettingsPage>
  );
}

export default {
  accountSettingsRoutes: [
    {
      path: "/settings/account/plugins/carddav-sync",
      title: "CardDAV sync",
      label: "CardDAV sync",
      description: "Pull contacts from any CardDAV address book into Rolltop.",
      icon: "sync",
      section: "plugins",
      render: (context: SettingsContext) => <CardDAVSyncSettings {...context} />
    }
  ]
} satisfies AccountSettingsRuntimePlugin;
