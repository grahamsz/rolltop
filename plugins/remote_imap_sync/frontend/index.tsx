// File overview: Runtime settings UI for one-way remote IMAP migration routines.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import type { Toast } from "../../../frontend/src/appTypes";
import { Icon } from "../../../frontend/src/components/Icon";
import { displayDateTime } from "../../../frontend/src/lib/format";
import type { Mailbox, User } from "../../../frontend/src/types";
import "./styles.css";

const apiBase = "/api/plugins/remote_imap_sync";

type SettingsContext = {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  navigate: (url: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
};

type SourceSecurity = "tls" | "plain";

type RemoteSource = {
  provider: string;
  host: string;
  port: number;
  security: SourceSecurity | string;
  username: string;
  mailbox: string;
  has_password: boolean;
};

type RemoteDestination = {
  account_id: number;
  account_label: string;
  account_email: string;
  mailbox_id: number;
  mailbox_name: string;
};

type RemoteRun = {
  id: number;
  routine_id: number;
  trigger: string;
  status: string;
  seen: number;
  scanned: number;
  transferred: number;
  skipped: number;
  total: number;
  current_uid: number;
  error: string;
  started_at: string;
  completed_at: string;
};

type RemoteRoutine = {
  id: number;
  name: string;
  enabled: boolean;
  source: RemoteSource;
  destination: RemoteDestination;
  after_date: string;
  state: string;
  last_error: string;
  last_started_at: string;
  last_completed_at: string;
  last_activity_at: string;
  next_retry_at: string;
  transferred_total: number;
  skipped_total: number;
  active_run: RemoteRun | null;
  latest_run: RemoteRun | null;
};

type RemoteFolder = {
  id: number;
  name: string;
  role: string;
};

type DestinationAccount = {
  id: number;
  label: string;
  email: string;
  folders: RemoteFolder[];
};

type SourceFolder = {
  name: string;
  delimiter: string;
  attributes: string[];
  selectable: boolean;
};

type RoutineDraft = {
  id: number;
  name: string;
  enabled: boolean;
  source_provider: string;
  source_host: string;
  source_port: string;
  source_security: SourceSecurity;
  source_username: string;
  source_password: string;
  source_mailbox: string;
  has_password: boolean;
  destination_account_id: number;
  destination_mailbox_id: number;
  after_date: string;
};

type RemoteRunWire = Omit<Partial<RemoteRun>, "started_at" | "completed_at"> & {
  started_at?: string | number;
  completed_at?: string | number;
};

type RemoteRoutineWire = Omit<Partial<RemoteRoutine>, "last_started_at" | "last_completed_at" | "last_activity_at" | "next_retry_at" | "active_run" | "latest_run"> & {
  source_provider?: string;
  source_host?: string;
  source_port?: number;
  source_username?: string;
  source_use_tls?: boolean;
  use_tls?: boolean;
  source_mailbox?: string;
  has_password?: boolean;
  destination_account_id?: number;
  destination_mailbox_id?: number;
  destination_account_label?: string;
  destination_account_email?: string;
  destination_mailbox_name?: string;
  last_started_at?: string | number;
  last_completed_at?: string | number;
  last_success_at?: string | number;
  last_activity_at?: string | number;
  next_retry_at?: string | number;
  last_started?: string | number;
  last_completed?: string | number;
  last_activity?: string | number;
  next_retry?: string | number;
  active_run?: RemoteRunWire | null;
  latest_run?: RemoteRunWire | null;
};

type DestinationWire = Partial<DestinationAccount> & {
  account_id?: number;
  account_label?: string;
  account_email?: string;
  mailboxes?: RemoteFolder[];
};

type RoutinesResponse = {
  routines?: RemoteRoutineWire[];
  destinations?: DestinationWire[];
};

type DiscoverResponse = {
  mailboxes?: Array<SourceFolder | string>;
  folders?: Array<SourceFolder | string>;
  capabilities?: {
    idle?: boolean;
    uidplus?: boolean;
  };
};

function numberOr(value: number | undefined, fallback = 0) {
  return Number.isFinite(value) ? Number(value) : fallback;
}

function dateString(value: unknown) {
  if (typeof value === "number") {
    if (!Number.isFinite(value) || value <= 0) return "";
    return new Date(value < 1_000_000_000_000 ? value * 1000 : value).toISOString();
  }
  if (typeof value !== "string") return "";
  const trimmed = value.trim();
  if (!trimmed) return "";
  if (/^\d+$/.test(trimmed)) return dateString(Number(trimmed));
  return trimmed;
}

function normalizeRun(value?: RemoteRunWire | null): RemoteRun | null {
  if (!value || !value.id) return null;
  return {
    id: numberOr(value.id),
    routine_id: numberOr(value.routine_id),
    trigger: value.trigger || "",
    status: value.status || "",
    seen: numberOr(value.seen, numberOr(value.scanned)),
    scanned: numberOr(value.scanned, numberOr(value.seen)),
    transferred: numberOr(value.transferred),
    skipped: numberOr(value.skipped),
    total: numberOr(value.total),
    current_uid: numberOr(value.current_uid),
    error: value.error || "",
    started_at: dateString(value.started_at),
    completed_at: dateString(value.completed_at)
  };
}

function normalizeRoutine(value: RemoteRoutineWire): RemoteRoutine {
  const source = value.source || ({} as RemoteSource);
  const destination = value.destination || ({} as RemoteDestination);
  const useTLS = value.source_use_tls ?? value.use_tls ?? source.security !== "plain";
  return {
    id: numberOr(value.id),
    name: value.name || "IMAP sync",
    enabled: Boolean(value.enabled),
    source: {
      provider: source.provider || value.source_provider || "custom",
      host: source.host || value.source_host || "",
      port: numberOr(source.port, numberOr(value.source_port, useTLS ? 993 : 143)),
      security: source.security || (useTLS ? "tls" : "plain"),
      username: source.username || value.source_username || "",
      mailbox: source.mailbox || value.source_mailbox || "",
      has_password: Boolean(source.has_password ?? value.has_password)
    },
    destination: {
      account_id: numberOr(destination.account_id, numberOr(value.destination_account_id)),
      account_label: destination.account_label || value.destination_account_label || "",
      account_email: destination.account_email || value.destination_account_email || "",
      mailbox_id: numberOr(destination.mailbox_id, numberOr(value.destination_mailbox_id)),
      mailbox_name: destination.mailbox_name || value.destination_mailbox_name || ""
    },
    after_date: value.after_date || "",
    state: value.state || (value.enabled ? "watching" : "paused"),
    last_error: value.last_error || "",
    last_started_at: dateString(value.last_started_at || value.last_started),
    last_completed_at: dateString(value.last_completed_at || value.last_success_at || value.last_completed),
    last_activity_at: dateString(value.last_activity_at || value.last_activity),
    next_retry_at: dateString(value.next_retry_at || value.next_retry),
    transferred_total: numberOr(value.transferred_total),
    skipped_total: numberOr(value.skipped_total),
    active_run: normalizeRun(value.active_run),
    latest_run: normalizeRun(value.latest_run)
  };
}

function normalizeDestinations(values: DestinationWire[] | undefined, mailboxes: Mailbox[]) {
  if (values && values.length > 0) {
    return values.map((value) => ({
      id: numberOr(value.id, numberOr(value.account_id)),
      label: value.label || value.account_label || value.email || value.account_email || "IMAP account",
      email: value.email || value.account_email || "",
      folders: value.folders || value.mailboxes || []
    })).filter((value) => value.id > 0);
  }
  const accounts = new Map<number, DestinationAccount>();
  mailboxes.forEach((mailbox) => {
    let account = accounts.get(mailbox.account_id);
    if (!account) {
      account = {
        id: mailbox.account_id,
        label: mailbox.account_label || mailbox.account_email || `Account ${mailbox.account_id}`,
        email: mailbox.account_email || "",
        folders: []
      };
      accounts.set(mailbox.account_id, account);
    }
    account.folders.push({ id: mailbox.id, name: mailbox.name, role: mailbox.role || "" });
  });
  return [...accounts.values()];
}

function blankDraft(destinations: DestinationAccount[]): RoutineDraft {
  const account = destinations[0];
  const inbox = account?.folders.find((folder) => folder.role === "inbox") || account?.folders[0];
  return {
    id: 0,
    name: "",
    enabled: true,
    source_provider: "gmail",
    source_host: "imap.gmail.com",
    source_port: "993",
    source_security: "tls",
    source_username: "",
    source_password: "",
    source_mailbox: "",
    has_password: false,
    destination_account_id: account?.id || 0,
    destination_mailbox_id: inbox?.id || 0,
    after_date: ""
  };
}

function draftFromRoutine(routine: RemoteRoutine): RoutineDraft {
  return {
    id: routine.id,
    name: routine.name,
    enabled: routine.enabled,
    source_provider: routine.source.provider || (routine.source.host === "imap.gmail.com" ? "gmail" : "custom"),
    source_host: routine.source.host,
    source_port: String(routine.source.port || 993),
    source_security: routine.source.security === "plain" ? "plain" : "tls",
    source_username: routine.source.username,
    source_password: "",
    source_mailbox: routine.source.mailbox,
    has_password: routine.source.has_password,
    destination_account_id: routine.destination.account_id,
    destination_mailbox_id: routine.destination.mailbox_id,
    after_date: routine.after_date
  };
}

function normalizedFolders(values: Array<SourceFolder | string> | undefined) {
  return (values || []).map((value) => typeof value === "string"
    ? { name: value, delimiter: "/", attributes: [], selectable: true }
    : { ...value, selectable: value.selectable !== false }
  ).filter((folder) => folder.name && folder.selectable);
}

class APIError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

async function requestJSON<T>(url: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(url, {
    credentials: "same-origin",
    ...init,
    headers: {
      Accept: "application/json",
      ...(init.body ? { "Content-Type": "application/json" } : {}),
      ...(init.headers || {})
    }
  });
  const text = await response.text();
  let data: Record<string, unknown> = {};
  if (text) {
    try {
      data = JSON.parse(text) as Record<string, unknown>;
    } catch {
      if (!response.ok) throw new APIError(response.status, response.statusText || "Request failed");
      throw new APIError(response.status, "The server returned an invalid response.");
    }
  }
  if (!response.ok) {
    const message = typeof data.error === "string"
      ? data.error
      : typeof data.message === "string" ? data.message : response.statusText || "Request failed";
    throw new APIError(response.status, message);
  }
  return data as T;
}

function messageFromError(error: unknown) {
  return error instanceof Error ? error.message : "Request failed.";
}

function stateLabel(state: string) {
  const normalized = state.trim().toLowerCase();
  switch (normalized) {
    case "connecting": return "Connecting";
    case "watching": return "Watching";
    case "queued": return "Queued";
    case "syncing":
    case "running": return "Syncing";
    case "retrying": return "Retrying";
    case "error":
    case "failed": return "Needs attention";
    case "paused":
    case "disabled": return "Paused";
    default: return normalized ? normalized.replaceAll("_", " ") : "Paused";
  }
}

function stateTone(state: string) {
  switch (state.trim().toLowerCase()) {
    case "watching": return "live";
    case "connecting":
    case "queued":
    case "syncing":
    case "running": return "active";
    case "retrying": return "warning";
    case "error":
    case "failed": return "error";
    default: return "paused";
  }
}

function activeState(state: string) {
  return ["connecting", "queued", "syncing", "running"].includes(state.trim().toLowerCase());
}

function destinationLabel(destination: RemoteDestination) {
  const account = destination.account_label || destination.account_email || "IMAP account";
  return `${account} / ${destination.mailbox_name || "Folder"}`;
}

function lastActivityLabel(routine: RemoteRoutine, user: User) {
  const value = routine.last_activity_at || routine.last_completed_at || routine.latest_run?.completed_at || "";
  if (!value) return "No successful transfers yet";
  return `Last activity ${displayDateTime(value, user)}`;
}

function runProgress(routine: RemoteRoutine) {
  const run = routine.active_run || (activeState(routine.state) ? routine.latest_run : null);
  if (!run) return null;
  const scanned = run.scanned || run.seen;
  const percent = run.total > 0 ? Math.min(100, Math.round((scanned / run.total) * 100)) : 0;
  return { run, scanned, percent };
}

function RemoteIMAPSyncSummary({ navigate }: Pick<SettingsContext, "navigate">) {
  const [routines, setRoutines] = useState<RemoteRoutine[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void requestJSON<RoutinesResponse>(`${apiBase}/routines`)
      .then((data) => {
        if (cancelled) return;
        setRoutines((data.routines || []).map(normalizeRoutine));
        setLoaded(true);
      })
      .catch(() => {
        if (!cancelled) {
          setLoadError(true);
          setLoaded(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const live = routines.filter((routine) => routine.enabled && ["live", "active"].includes(stateTone(routine.state))).length;
  const errors = routines.filter((routine) => Boolean(routine.last_error)).length;
  const countLabel = !loaded
    ? "Loading routines"
    : loadError
      ? "Unable to load routines"
    : routines.length === 0
      ? "No routines configured"
      : `${routines.length} ${routines.length === 1 ? "routine" : "routines"} · ${live} live`;

  return (
    <section className="panel account-list-panel remote-imap-sync-summary">
      <div className="panel-headline">
        <div>
          <h2>Remote IMAP sync</h2>
          <div className="muted">One-way mailbox migration into a connected Rolltop account.</div>
        </div>
        <button className="secondary" type="button" onClick={() => navigate("/settings/account/remote-imap-sync")}>
          <Icon name="sync" />Manage
        </button>
      </div>
      <button className="server-row" type="button" onClick={() => navigate("/settings/account/remote-imap-sync")}>
        <span className="server-row-icon"><Icon name="sync" /></span>
        <strong>IMAP migration</strong>
        <small>{countLabel}</small>
        {errors > 0 ? <span className="remote-imap-sync-summary-error"><Icon name="report" />{errors} need attention</span> : null}
      </button>
    </section>
  );
}

export function RemoteIMAPSyncSettings({ csrf, user, mailboxes, navigate, addToast }: SettingsContext) {
  const [routines, setRoutines] = useState<RemoteRoutine[]>([]);
  const [destinations, setDestinations] = useState<DestinationAccount[]>([]);
  const [draft, setDraft] = useState<RoutineDraft>(() => blankDraft([]));
  const [sourceFolders, setSourceFolders] = useState<SourceFolder[]>([]);
  const [sourceCapabilities, setSourceCapabilities] = useState<DiscoverResponse["capabilities"]>(undefined);
  const [loading, setLoading] = useState(true);
  const [busyAction, setBusyAction] = useState("");
  const editorRef = useRef<HTMLElement | null>(null);

  const load = useCallback(async (quiet = false) => {
    if (!quiet) setLoading(true);
    try {
      const data = await requestJSON<RoutinesResponse>(`${apiBase}/routines`);
      const nextRoutines = (data.routines || []).map(normalizeRoutine);
      const nextDestinations = normalizeDestinations(data.destinations, mailboxes);
      setRoutines(nextRoutines);
      setDestinations(nextDestinations);
      setDraft((current) => {
        if (current.id || current.destination_account_id || nextDestinations.length === 0) return current;
        return blankDraft(nextDestinations);
      });
    } catch (error) {
      if (!quiet) addToast(messageFromError(error), "error");
    } finally {
      if (!quiet) setLoading(false);
    }
  }, [addToast, mailboxes]);

  useEffect(() => {
    void load();
  }, [load]);

  const pollingFast = routines.some((routine) => activeState(routine.state) || routine.state === "retrying");
  useEffect(() => {
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void load(true);
    }, pollingFast ? 2000 : 15000);
    return () => window.clearInterval(timer);
  }, [load, pollingFast]);

  const selectedDestination = useMemo(
    () => destinations.find((account) => account.id === draft.destination_account_id) || null,
    [destinations, draft.destination_account_id]
  );
  const selectedDestinationFolders = selectedDestination?.folders || [];

  function updateDraft(patch: Partial<RoutineDraft>) {
    setDraft((current) => ({ ...current, ...patch }));
  }

  function scrollToEditor() {
    window.requestAnimationFrame(() => editorRef.current?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }

  function newRoutine() {
    setDraft(blankDraft(destinations));
    setSourceFolders([]);
    setSourceCapabilities(undefined);
    scrollToEditor();
  }

  function editRoutine(routine: RemoteRoutine) {
    setDraft(draftFromRoutine(routine));
    setSourceFolders(routine.source.mailbox ? [{
      name: routine.source.mailbox,
      delimiter: "/",
      attributes: [],
      selectable: true
    }] : []);
    setSourceCapabilities(undefined);
    scrollToEditor();
  }

  function changeProvider(provider: string) {
    if (provider === "gmail") {
      updateDraft({
        source_provider: provider,
        source_host: "imap.gmail.com",
        source_port: "993",
        source_security: "tls",
        source_mailbox: ""
      });
    } else {
      updateDraft({ source_provider: provider, source_mailbox: "" });
    }
    setSourceFolders([]);
    setSourceCapabilities(undefined);
  }

  function changeDestinationAccount(accountID: number) {
    const account = destinations.find((item) => item.id === accountID);
    const folder = account?.folders.find((item) => item.role === "inbox") || account?.folders[0];
    updateDraft({ destination_account_id: accountID, destination_mailbox_id: folder?.id || 0 });
  }

  function sourcePayload(includePassword = true) {
    return {
      provider: draft.source_provider,
      host: draft.source_host.trim(),
      port: Number(draft.source_port),
      security: draft.source_security,
      use_tls: draft.source_security === "tls",
      username: draft.source_username.trim(),
      ...(includePassword && draft.source_password ? { password: draft.source_password } : {}),
      mailbox: draft.source_mailbox.trim()
    };
  }

  async function discoverSource() {
    if (!draft.source_host.trim() || !draft.source_username.trim() || !Number(draft.source_port)) {
      addToast("Enter the source server and username first.", "error");
      return;
    }
    if (!draft.has_password && !draft.source_password) {
      addToast("Enter the source password first.", "error");
      return;
    }
    setBusyAction("discover");
    try {
      const data = await requestJSON<DiscoverResponse>(`${apiBase}/source/discover`, {
        method: "POST",
        headers: { "X-CSRF-Token": csrf },
        body: JSON.stringify({
          ...(draft.id ? { routine_id: draft.id } : {}),
          source: sourcePayload()
        })
      });
      const folders = normalizedFolders(data.mailboxes || data.folders);
      setSourceFolders(folders);
      setSourceCapabilities(data.capabilities);
      if (folders.length === 0) {
        addToast("The source connected, but no selectable folders were returned.", "error");
      } else {
        const selected = folders.some((folder) => folder.name === draft.source_mailbox)
          ? draft.source_mailbox
          : folders.find((folder) => folder.name.toLowerCase() === "inbox")?.name || folders[0].name;
        updateDraft({ source_mailbox: selected });
        addToast(`${folders.length} source ${folders.length === 1 ? "folder" : "folders"} loaded.`, "success");
      }
    } catch (error) {
      addToast(messageFromError(error), "error");
    } finally {
      setBusyAction("");
    }
  }

  function validateDraft() {
    if (!draft.name.trim()) return "Enter a routine name.";
    if (!draft.source_host.trim() || !draft.source_username.trim()) return "Complete the source connection.";
    const port = Number(draft.source_port);
    if (!Number.isInteger(port) || port <= 0 || port > 65535) return "Enter a valid source port.";
    if (!draft.has_password && !draft.source_password) return "Enter the source password.";
    if (!draft.source_mailbox.trim()) return "Choose a source folder.";
    if (!draft.destination_account_id || !draft.destination_mailbox_id) return "Choose a connected destination folder.";
    return "";
  }

  function structuralDraftChanged(original: RemoteRoutine) {
    return original.source.provider !== draft.source_provider ||
      original.source.host.trim().toLowerCase() !== draft.source_host.trim().toLowerCase() ||
      original.source.port !== Number(draft.source_port) ||
      original.source.username !== draft.source_username.trim() ||
      (original.source.security !== "plain") !== (draft.source_security === "tls") ||
      original.source.mailbox !== draft.source_mailbox.trim() ||
      original.destination.account_id !== draft.destination_account_id ||
      original.destination.mailbox_id !== draft.destination_mailbox_id ||
      original.after_date !== draft.after_date;
  }

  async function saveRoutine(event: FormEvent) {
    event.preventDefault();
    const validation = validateDraft();
    if (validation) {
      addToast(validation, "error");
      return;
    }
    const original = draft.id ? routines.find((routine) => routine.id === draft.id) : null;
    if (original && original.transferred_total > 0 && structuralDraftChanged(original) && !window.confirm(
      "Change this routine's source, destination, or date range? Its migration checkpoint will reset and the destination may receive messages that were already copied."
    )) return;
    setBusyAction("save");
    try {
      const body = {
        name: draft.name.trim(),
        enabled: draft.enabled,
        source: sourcePayload(),
        destination: {
          account_id: draft.destination_account_id,
          mailbox_id: draft.destination_mailbox_id
        },
        after_date: draft.after_date
      };
      const data = await requestJSON<{ routine?: RemoteRoutineWire }>(draft.id
        ? `${apiBase}/routines/${draft.id}`
        : `${apiBase}/routines`, {
        method: draft.id ? "PUT" : "POST",
        headers: { "X-CSRF-Token": csrf },
        body: JSON.stringify(body)
      });
      addToast(draft.id ? "Sync routine saved." : "Sync routine created.", "success");
      if (data.routine) {
        const saved = normalizeRoutine(data.routine);
        setDraft(draftFromRoutine(saved));
      } else {
        setDraft((current) => ({ ...current, source_password: "", has_password: true }));
      }
      await load(true);
    } catch (error) {
      addToast(messageFromError(error), "error");
    } finally {
      setBusyAction("");
    }
  }

  async function setRoutineEnabled(routine: RemoteRoutine, enabled: boolean) {
    const action = `enabled:${routine.id}`;
    setBusyAction(action);
    setRoutines((current) => current.map((item) => item.id === routine.id
      ? { ...item, enabled, state: enabled ? "connecting" : "paused" }
      : item));
    try {
      await requestJSON(`${apiBase}/routines/${routine.id}/enabled`, {
        method: "POST",
        headers: { "X-CSRF-Token": csrf },
        body: JSON.stringify({ enabled })
      });
      addToast(enabled ? "Sync routine enabled." : "Sync routine paused.", "success");
      await load(true);
    } catch (error) {
      await load(true);
      addToast(messageFromError(error), "error");
    } finally {
      setBusyAction("");
    }
  }

  async function runRoutine(routine: RemoteRoutine) {
    const action = `run:${routine.id}`;
    setBusyAction(action);
    try {
      await requestJSON(`${apiBase}/routines/${routine.id}/run`, {
        method: "POST",
        headers: { "X-CSRF-Token": csrf },
        body: "{}"
      });
      setRoutines((current) => current.map((item) => item.id === routine.id ? { ...item, state: "queued" } : item));
      addToast("IMAP sync queued.", "success");
      await load(true);
    } catch (error) {
      addToast(messageFromError(error), "error");
    } finally {
      setBusyAction("");
    }
  }

  async function deleteRoutine(routine: RemoteRoutine) {
    if (!window.confirm(`Delete ${routine.name}? This stops the routine but does not delete mail from either server.`)) return;
    const action = `delete:${routine.id}`;
    setBusyAction(action);
    try {
      await requestJSON(`${apiBase}/routines/${routine.id}`, {
        method: "DELETE",
        headers: { "X-CSRF-Token": csrf }
      });
      if (draft.id === routine.id) newRoutine();
      setRoutines((current) => current.filter((item) => item.id !== routine.id));
      addToast("Sync routine deleted.", "success");
      await load(true);
    } catch (error) {
      addToast(messageFromError(error), "error");
    } finally {
      setBusyAction("");
    }
  }

  return (
    <section className="remote-imap-sync-settings">
      <div className="content-head remote-imap-sync-page-head">
        <div className="list-head-main">
          <button className="icon-button" type="button" onClick={() => navigate("/settings/account")} title="Back to settings" aria-label="Back to settings">
            <Icon name="arrow_back" />
          </button>
          <div>
            <h1>Remote IMAP sync</h1>
            <span className="label-pill">{routines.length.toLocaleString()}</span>
          </div>
        </div>
        <button type="button" onClick={newRoutine}><Icon name="add" />Add routine</button>
      </div>

      {loading ? <div className="panel muted">Loading sync routines...</div> : null}

      <section className="panel remote-imap-sync-list-panel" aria-live="polite">
        <div className="panel-headline">
          <div>
            <h2>Routines</h2>
            <div className="muted">Enabled routines reconcile periodically and listen for new source mail.</div>
          </div>
        </div>
        <div className="remote-imap-sync-routine-list">
          {!loading && routines.length === 0 ? (
            <div className="remote-imap-sync-empty">
              <Icon name="sync" />
              <div><strong>No sync routines</strong><span>Add a source folder and choose where its mail should arrive.</span></div>
            </div>
          ) : null}
          {routines.map((routine) => {
            const progress = runProgress(routine);
            const tone = stateTone(routine.state);
            return (
              <article className={`remote-imap-sync-routine ${tone}`} key={routine.id}>
                <label className="remote-imap-sync-switch" title={routine.enabled ? "Pause routine" : "Enable routine"}>
                  <input
                    type="checkbox"
                    checked={routine.enabled}
                    disabled={busyAction === `enabled:${routine.id}`}
                    onChange={(event) => void setRoutineEnabled(routine, event.target.checked)}
                  />
                  <span aria-hidden="true" />
                  <em>{routine.enabled ? "Enabled" : "Paused"}</em>
                </label>
                <div className="remote-imap-sync-routine-main">
                  <div className="remote-imap-sync-routine-title">
                    <strong>{routine.name}</strong>
                    <span className={`remote-imap-sync-state ${tone}`}>{activeState(routine.state) ? <Icon name="sync" /> : null}{stateLabel(routine.state)}</span>
                  </div>
                  <div className="remote-imap-sync-route">
                    <span>{routine.source.username || routine.source.host} / {routine.source.mailbox}</span>
                    <Icon name="arrow_forward" />
                    <span>{destinationLabel(routine.destination)}</span>
                  </div>
                  <div className="remote-imap-sync-routine-meta">
                    <span>{lastActivityLabel(routine, user)}</span>
                    <span>{routine.transferred_total.toLocaleString()} transferred</span>
                    {routine.after_date ? <span>Since {routine.after_date}</span> : null}
                  </div>
                  {progress ? (
                    <div className="remote-imap-sync-progress">
                      <div className={progress.run.total > 0 ? "" : "indeterminate"}>
                        <span style={progress.run.total > 0 ? { width: `${progress.percent}%` } : undefined} />
                      </div>
                      <small>{progress.scanned.toLocaleString()} scanned · {progress.run.transferred.toLocaleString()} transferred</small>
                    </div>
                  ) : null}
                  {routine.last_error ? (
                    <div className="remote-imap-sync-row-error">
                      <Icon name="report" />
                      <span>{routine.last_error}{routine.next_retry_at ? ` · Retry ${displayDateTime(routine.next_retry_at, user)}` : ""}</span>
                    </div>
                  ) : null}
                </div>
                <div className="remote-imap-sync-row-actions">
                  <button
                    className="secondary"
                    type="button"
                    disabled={!routine.enabled || activeState(routine.state) || busyAction === `run:${routine.id}`}
                    onClick={() => void runRoutine(routine)}
                    title={routine.enabled ? "Run now" : "Enable this routine before running it"}
                  >
                    <Icon name="sync" />Run now
                  </button>
                  <button className="icon-button" type="button" onClick={() => editRoutine(routine)} title={`Edit ${routine.name}`} aria-label={`Edit ${routine.name}`}>
                    <Icon name="edit" />
                  </button>
                  <button
                    className="icon-button danger"
                    type="button"
                    disabled={busyAction === `delete:${routine.id}`}
                    onClick={() => void deleteRoutine(routine)}
                    title={`Delete ${routine.name}`}
                    aria-label={`Delete ${routine.name}`}
                  >
                    <Icon name="delete" />
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      </section>

      <section className="panel remote-imap-sync-editor" ref={editorRef}>
        <div className="panel-headline">
          <div>
            <h2>{draft.id ? `Edit ${draft.name || "routine"}` : "New routine"}</h2>
            <div className="muted">Mail is copied one way. Nothing is deleted or moved on the source server.</div>
          </div>
          {draft.id ? <button className="secondary" type="button" onClick={newRoutine}><Icon name="add" />New</button> : null}
        </div>

        <form onSubmit={saveRoutine}>
          <fieldset>
            <legend>Routine</legend>
            <div className="remote-imap-sync-form-grid routine">
              <label>
                <span className="settings-field-label">Name</span>
                <input value={draft.name} onChange={(event) => updateDraft({ name: event.target.value })} placeholder="Gmail Inbox to MXRoute" required />
              </label>
              <label className="remote-imap-sync-enabled-field">
                <input type="checkbox" checked={draft.enabled} onChange={(event) => updateDraft({ enabled: event.target.checked })} />
                <span>Enable after saving</span>
              </label>
            </div>
          </fieldset>

          <fieldset>
            <legend>Source</legend>
            <div className="remote-imap-sync-form-grid source">
              <label>
                <span className="settings-field-label">Provider</span>
                <select value={draft.source_provider} onChange={(event) => changeProvider(event.target.value)}>
                  <option value="gmail">Gmail</option>
                  <option value="custom">Other IMAP</option>
                </select>
              </label>
              {draft.source_provider === "custom" ? (
                <>
                  <label>
                    <span className="settings-field-label">Host</span>
                    <input value={draft.source_host} onChange={(event) => updateDraft({ source_host: event.target.value })} placeholder="imap.example.com" required />
                  </label>
                  <label>
                    <span className="settings-field-label">Port</span>
                    <input type="number" min="1" max="65535" value={draft.source_port} onChange={(event) => updateDraft({ source_port: event.target.value })} required />
                  </label>
                  <label>
                    <span className="settings-field-label">Security</span>
                    <select value={draft.source_security} onChange={(event) => updateDraft({ source_security: event.target.value as SourceSecurity })}>
                      <option value="tls">TLS</option>
                      <option value="plain">No TLS (local servers only)</option>
                    </select>
                  </label>
                </>
              ) : null}
              <label>
                <span className="settings-field-label">Username</span>
                <input type={draft.source_provider === "gmail" ? "email" : "text"} value={draft.source_username} onChange={(event) => updateDraft({ source_username: event.target.value })} placeholder={draft.source_provider === "gmail" ? "you@gmail.com" : "IMAP username"} autoComplete="username" required />
              </label>
              <label>
                <span className="settings-field-label">{draft.source_provider === "gmail" ? "App password" : "Password"}</span>
                <input
                  type="password"
                  value={draft.source_password}
                  onChange={(event) => updateDraft({ source_password: event.target.value })}
                  placeholder={draft.has_password ? "Leave blank to keep saved password" : ""}
                  autoComplete="new-password"
                  required={!draft.has_password}
                />
                {draft.has_password && !draft.source_password ? <small className="remote-imap-sync-password-saved"><Icon name="lock" />Password saved</small> : null}
              </label>
              <label className="remote-imap-sync-folder-field">
                <span className="settings-field-label">Source folder</span>
                <input
                  list="remote-imap-sync-source-folders"
                  value={draft.source_mailbox}
                  onChange={(event) => updateDraft({ source_mailbox: event.target.value })}
                  placeholder="Test the source to load folders"
                  required
                />
                <datalist id="remote-imap-sync-source-folders">
                  {sourceFolders.map((folder) => <option value={folder.name} key={folder.name} />)}
                </datalist>
              </label>
              <div className="remote-imap-sync-test-field">
                <button className="secondary" type="button" disabled={busyAction === "discover"} onClick={() => void discoverSource()}>
                  <Icon name="sync" />{busyAction === "discover" ? "Testing..." : "Test & load folders"}
                </button>
                {sourceCapabilities ? (
                  <small className={sourceCapabilities.idle ? "available" : "unavailable"}>
                    {sourceCapabilities.idle ? "IDLE available" : "IDLE unavailable; periodic sync will be used"}
                  </small>
                ) : null}
              </div>
            </div>
          </fieldset>

          <fieldset>
            <legend>Destination and range</legend>
            <div className="remote-imap-sync-form-grid destination">
              <label>
                <span className="settings-field-label">Rolltop account</span>
                <select value={draft.destination_account_id || ""} onChange={(event) => changeDestinationAccount(Number(event.target.value))} required>
                  <option value="" disabled>Choose an account</option>
                  {destinations.map((account) => (
                    <option value={account.id} key={account.id}>{account.label}{account.email && account.email !== account.label ? ` · ${account.email}` : ""}</option>
                  ))}
                </select>
              </label>
              <label>
                <span className="settings-field-label">Destination folder</span>
                <select value={draft.destination_mailbox_id || ""} onChange={(event) => updateDraft({ destination_mailbox_id: Number(event.target.value) })} required>
                  <option value="" disabled>{selectedDestination ? "Choose a folder" : "Choose an account first"}</option>
                  {selectedDestinationFolders.map((folder) => <option value={folder.id} key={folder.id}>{folder.name}</option>)}
                </select>
                {selectedDestination && selectedDestinationFolders.length === 0 ? <small className="remote-imap-sync-field-error">Sync this Rolltop account once to discover its folders.</small> : null}
              </label>
              <label>
                <span className="settings-field-label">Messages on or after</span>
                <input type="date" value={draft.after_date} onChange={(event) => updateDraft({ after_date: event.target.value })} />
                <small>Leave blank to copy all messages in the source folder.</small>
              </label>
            </div>
          </fieldset>

          <div className="actions remote-imap-sync-form-actions">
            <button type="submit" disabled={busyAction === "save" || destinations.length === 0}>
              <Icon name="sync" />{busyAction === "save" ? "Saving..." : draft.id ? "Save routine" : "Create routine"}
            </button>
            {draft.id ? <button className="secondary" type="button" onClick={() => {
              const original = routines.find((item) => item.id === draft.id);
              if (original) editRoutine(original);
            }}>Reset changes</button> : null}
          </div>
        </form>
      </section>
    </section>
  );
}

export default {
  accountSettingsRoutes: [
    {
      path: "/settings/account/remote-imap-sync",
      render: (context: SettingsContext) => <RemoteIMAPSyncSettings {...context} />
    }
  ],
  renderAccountSettingsSummary: (context: SettingsContext) => <RemoteIMAPSyncSummary navigate={context.navigate} />
};
