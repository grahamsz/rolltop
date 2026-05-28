// File overview: Root React coordinator for bootstrap, session routing, global chrome state,
// toast lifecycle, server-sent events, browser-history navigation, and the compose overlay.

import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api } from "./api";
import type { Bootstrap, ChromeEvent, IdentityPGPPrivateKey, SyncRun } from "./types";
import type { LocationState, MessageTransferAction, MoveTarget, PGPUnlockState, Toast } from "./appTypes";
import { ToastStack } from "./components/common";
import { LogoMark } from "./components/Icon";
import { SetupPage, LoginPage, PasswordResetPage } from "./features/auth/AuthPages";
import { AppShell } from "./features/layout/AppShell";
import { ComposeOverlay } from "./features/compose/ComposeViews";
import { RouteView } from "./RouteView";
import { messageFromError } from "./lib/errors";
import { currentLocation } from "./lib/routes";
import { matchingPGPPrivateKeyIDForRecipients, restorePGPUnlockState, serializePGPUnlockState, unlockPrivateKey } from "./lib/pgp";
import type { SerializedPGPUnlockState } from "./lib/pgp";
import { hydrateBrowserPGPPrivateKeys } from "./lib/browserPGPKeys";


// Push notifications should identify the human sender when possible. This helper
// strips RFC5322 angle-address syntax and falls back to the email local part.
function displayNotificationSender(raw: string) {
  const trimmed = raw.trim();
  if (!trimmed) return "";
  const angle = trimmed.match(/^(.+?)\s*<[^>]+>$/);
  const value = (angle ? angle[1] : trimmed).replace(/^"|"$/g, "").trim();
  if (!value.includes("@")) return truncateNotificationText(value, 64);
  return truncateNotificationText(value.split("@")[0] || value, 64);
}

function truncateNotificationText(value: string, max: number) {
  const trimmed = value.trim().replace(/\s+/g, " ");
  if (trimmed.length <= max) return trimmed;
  return `${trimmed.slice(0, Math.max(0, max - 1)).trimEnd()}...`;
}

const allMailWakePrefetchAfterMS = 3 * 60 * 1000;
const notificationPreferenceKey = "rolltop.notifications.enabled";
const emptyPGPUnlockState: PGPUnlockState = { unlockedUntil: 0, keys: [] };

type PGPUnlockWorkerMessage = {
  type: "rolltop:pgp-unlock-get" | "rolltop:pgp-unlock-set";
  userID: number;
  state?: SerializedPGPUnlockState;
};

function postServiceWorkerMessage(message: PGPUnlockWorkerMessage) {
  if (!("serviceWorker" in navigator)) return;
  const controller = navigator.serviceWorker.controller;
  if (controller) {
    controller.postMessage(message);
    return;
  }
  navigator.serviceWorker.ready
    .then((registration) => registration.active?.postMessage(message))
    .catch(() => {
      // The tab-level PGP unlock state still works without the service worker.
    });
}

async function publishPGPUnlockToWorker(userID: number, state: PGPUnlockState) {
  const serialized = await serializePGPUnlockState(state);
  postServiceWorkerMessage({ type: "rolltop:pgp-unlock-set", userID, state: serialized });
}

function requestPGPUnlockFromWorker(userID: number) {
  postServiceWorkerMessage({ type: "rolltop:pgp-unlock-get", userID });
}

function notificationPreference() {
  try {
    return window.localStorage.getItem(notificationPreferenceKey) || "";
  } catch {
    return "";
  }
}

function setNotificationPreference(value: "on" | "off") {
  try {
    window.localStorage.setItem(notificationPreferenceKey, value);
  } catch {
    // Ignore unavailable browser storage; in-memory state still controls this tab.
  }
}

function initialNotificationsEnabled() {
  if (!("Notification" in window) || Notification.permission !== "granted") return false;
  return notificationPreference() !== "off";
}

/**
 * App owns process-wide browser state: bootstrap/session data, current URL,
 * top-level toasts, SSE chrome refreshes, optimistic message hiding, and the
 * compose overlay. Feature views stay below RouteView so they can be remounted
 * by URL changes without taking the shell state with them.
 */
export default function App() {
  const [location, setLocation] = useState<LocationState>(() => currentLocation());
  const [bootstrap, setBootstrap] = useState<Bootstrap | null>(null);
  const [bootError, setBootError] = useState("");
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [hiddenMessageIDs, setHiddenMessageIDs] = useState<Set<number>>(() => new Set());
  const [composeOverlayQuery, setComposeOverlayQuery] = useState<string | null>(null);
  const toastSeq = useRef(1);
  const lastNotify = useRef<{ id: number; stored: number } | null>(null);
  const lastMouseActivityAt = useRef(Date.now());
  const lastAllMailWakePrefetchAt = useRef(0);
  const [notificationsEnabled, setNotificationsEnabled] = useState(initialNotificationsEnabled);
  const [pgpUnlock, setPGPUnlock] = useState<PGPUnlockState>({ unlockedUntil: 0, keys: [] });
  const [pgpUnlockOpen, setPGPUnlockOpen] = useState(false);
  const [pgpUnlockIdentityID, setPGPUnlockIdentityID] = useState<number | null>(null);
  const [pgpUnlockRecipientKeyIDs, setPGPUnlockRecipientKeyIDs] = useState<string[]>([]);
  const pgpUnlockCallbackRef = useRef<((state: PGPUnlockState) => void) | null>(null);
  const pgpUnlockRef = useRef<PGPUnlockState>(emptyPGPUnlockState);
  const activeUserIDRef = useRef<number | null>(null);

  // Navigation is intentionally tiny and local: Go serves every SPA route, so
  // the client only needs to update history and reparse LocationState.
  const replaceRoute = useCallback((url: string) => {
    window.history.replaceState({}, "", url);
    setLocation(currentLocation());
  }, []);

  const navigate = useCallback((url: string) => {
    window.history.pushState({}, "", url);
    setLocation(currentLocation());
  }, []);

  useEffect(() => {
    const onPop = () => setLocation(currentLocation());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  // Bootstrap is the shared chrome contract: auth state, CSRF token, folder
  // counts, active sync runs, enabled plugins, and account warnings.
  const refreshBootstrap = useCallback(async () => {
    try {
      const data = await api.bootstrap();
      setBootstrap(data);
      setBootError("");
      return data;
    } catch (err) {
      setBootError(messageFromError(err));
      return null;
    }
  }, []);

  useEffect(() => {
    void refreshBootstrap();
  }, [refreshBootstrap]);

  useEffect(() => {
    const savedTheme = bootstrap?.user?.theme;
    const theme = savedTheme === "classic_dark" || savedTheme === "matrix" ? savedTheme : "classic";
    document.documentElement.dataset.theme = theme;
  }, [bootstrap?.user?.theme]);

  useEffect(() => {
    const userID = bootstrap?.user?.id || null;
    if (activeUserIDRef.current === userID) return;
    const previousUserID = activeUserIDRef.current;
    activeUserIDRef.current = userID;
    pgpUnlockRef.current = emptyPGPUnlockState;
    setPGPUnlock(emptyPGPUnlockState);
    setPGPUnlockOpen(false);
    setPGPUnlockIdentityID(null);
    pgpUnlockCallbackRef.current = null;
    if (previousUserID) void publishPGPUnlockToWorker(previousUserID, emptyPGPUnlockState);
    if (userID) requestPGPUnlockFromWorker(userID);
  }, [bootstrap?.user?.id]);

  // Once bootstrap is known, normalize the unauthenticated/authenticated routes
  // so setup/login/mail all share one source of truth.
  useEffect(() => {
    if (!bootstrap) return;
    if (!bootstrap.users_exist && location.path !== "/setup") {
      replaceRoute("/setup");
      return;
    }
    if (bootstrap.users_exist && !bootstrap.user && location.path !== "/login" && location.path !== "/reset-password") {
      replaceRoute("/login");
      return;
    }
    if (bootstrap.user && (location.path === "/" || location.path === "/login" || location.path === "/setup" || location.path === "/reset-password")) {
      replaceRoute("/mail");
    }
  }, [bootstrap, location.path, replaceRoute]);

  const removeToast = useCallback((id: number) => {
    setToasts((items) => items.filter((toast) => toast.id !== id));
  }, []);

  const addToast = useCallback(
    (message: string, kind: Toast["kind"] = "success") => {
      const id = toastSeq.current++;
      setToasts((items) => [...items, { id, kind, message }]);
      if (kind !== "loading") {
        window.setTimeout(() => removeToast(id), 4200);
      }
      return id;
    },
    [removeToast]
  );

  const updateToast = useCallback(
    (id: number, message: string, kind: Toast["kind"]) => {
      setToasts((items) => items.map((toast) => (toast.id === id ? { ...toast, message, kind } : toast)));
      if (kind !== "loading") {
        window.setTimeout(() => removeToast(id), 4200);
      }
    },
    [removeToast]
  );

  const applyPGPUnlock = useCallback((state: PGPUnlockState, broadcast = false) => {
    pgpUnlockRef.current = state;
    setPGPUnlock(state);
    if (broadcast && activeUserIDRef.current) {
      void publishPGPUnlockToWorker(activeUserIDRef.current, state);
    }
  }, []);

  const lockPGP = useCallback(() => {
    applyPGPUnlock(emptyPGPUnlockState, true);
    setPGPUnlockIdentityID(null);
    setPGPUnlockRecipientKeyIDs([]);
    pgpUnlockCallbackRef.current = null;
    addToast("PGP keys locked.");
  }, [addToast, applyPGPUnlock]);

  const openPGPUnlock = useCallback((identityID?: number, onUnlocked?: (state: PGPUnlockState) => void, recipientKeyIDs: string[] = []) => {
    setPGPUnlockIdentityID(identityID || null);
    setPGPUnlockRecipientKeyIDs(recipientKeyIDs);
    pgpUnlockCallbackRef.current = onUnlocked || null;
    setPGPUnlockOpen(true);
  }, []);

  const closePGPUnlock = useCallback(() => {
    setPGPUnlockOpen(false);
    setPGPUnlockIdentityID(null);
    setPGPUnlockRecipientKeyIDs([]);
    pgpUnlockCallbackRef.current = null;
  }, []);

  useEffect(() => {
    if (!pgpUnlock.unlockedUntil) return;
    const delay = Math.max(0, pgpUnlock.unlockedUntil - Date.now());
    const timer = window.setTimeout(() => applyPGPUnlock(emptyPGPUnlockState, true), delay);
    return () => window.clearTimeout(timer);
  }, [applyPGPUnlock, pgpUnlock.unlockedUntil]);

  useEffect(() => {
    const userID = bootstrap?.user?.id || 0;
    if (!userID || !("serviceWorker" in navigator)) return;
    let cancelled = false;
    function onMessage(event: MessageEvent) {
      const data = event.data as { type?: string; userID?: number; state?: SerializedPGPUnlockState };
      if (data?.type === "rolltop:pgp-unlock-request" && data.userID === userID && pgpUnlockRef.current.unlockedUntil > Date.now()) {
        void publishPGPUnlockToWorker(userID, pgpUnlockRef.current);
        return;
      }
      if (data?.type !== "rolltop:pgp-unlock-state" || data.userID !== userID || !data.state) return;
      void restorePGPUnlockState(data.state).then((state) => {
        if (cancelled) return;
        pgpUnlockRef.current = state;
        setPGPUnlock(state);
        if (state.keys.length > 0) {
          setPGPUnlockOpen(false);
          setPGPUnlockIdentityID(null);
          const callback = pgpUnlockCallbackRef.current;
          pgpUnlockCallbackRef.current = null;
          callback?.(state);
        }
      });
    }
    navigator.serviceWorker.addEventListener("message", onMessage);
    requestPGPUnlockFromWorker(userID);
    return () => {
      cancelled = true;
      navigator.serviceWorker.removeEventListener("message", onMessage);
    };
  }, [bootstrap?.user?.id]);

  const toggleNotifications = useCallback(async () => {
    if (!("Notification" in window)) {
      addToast("This browser does not support notifications.", "error");
      return;
    }
    if (notificationsEnabled) {
      setNotificationPreference("off");
      setNotificationsEnabled(false);
      addToast("New-mail notifications paused.");
      return;
    }
    const result = Notification.permission === "granted" ? "granted" : await Notification.requestPermission();
    if (result === "granted") {
      setNotificationPreference("on");
      setNotificationsEnabled(true);
      addToast("New-mail notifications enabled.");
      return;
    }
    setNotificationPreference("off");
    setNotificationsEnabled(false);
    addToast("Notifications were not enabled.", "error");
  }, [addToast, notificationsEnabled]);

  const notifyNewMail = useCallback((count: number, run: SyncRun | null) => {
    if (!notificationsEnabled || !("Notification" in window) || Notification.permission !== "granted" || count <= 0) return;
    const sender = displayNotificationSender(run?.latest_new_from || "");
    const subject = truncateNotificationText(run?.latest_new_subject || "", 110);
    const title = sender ? `rolltop - ${sender}` : "rolltop";
    const fallback = count === 1 ? "1 new message synced." : `${count} new messages synced.`;
    const body = subject
      ? count === 1 ? subject : `${count} new messages synced. Latest: ${subject}`
      : fallback;
    new Notification(title, {
      body,
      tag: "rolltop-new-mail"
    });
  }, [notificationsEnabled]);

  // When someone returns to an idle tab, warm All Mail before they click it.
  useEffect(() => {
    if (!bootstrap?.user) return;
    function onMouseMove() {
      const now = Date.now();
      const inactiveFor = now - lastMouseActivityAt.current;
      lastMouseActivityAt.current = now;
      if (inactiveFor < allMailWakePrefetchAfterMS) return;
      if (now - lastAllMailWakePrefetchAt.current < allMailWakePrefetchAfterMS) return;
      lastAllMailWakePrefetchAt.current = now;
      api.prefetchMail(null, 1);
    }
    window.addEventListener("mousemove", onMouseMove, { passive: true });
    return () => window.removeEventListener("mousemove", onMouseMove);
  }, [bootstrap?.user]);

  // The event stream keeps chrome data hot without each view polling for folder
  // counts or sync progress. Malformed events are ignored so cached views remain usable.
  useEffect(() => {
    if (!bootstrap?.user) return;
    const events = new EventSource("/api/events");
    events.addEventListener("chrome", (event) => {
      try {
        const chrome = JSON.parse((event as MessageEvent).data) as ChromeEvent;
        setBootstrap((current) => current ? {
          ...current,
          mailboxes: chrome.mailboxes,
          sync_running: chrome.sync_running,
          latest_sync_run: chrome.latest_sync_run,
          active_sync_runs: chrome.active_sync_runs || [],
          server_started_at: chrome.server_started_at || current.server_started_at,
          server_uptime_seconds: chrome.server_uptime_seconds ?? current.server_uptime_seconds,
          build_version: chrome.build_version || current.build_version,
          build_date: chrome.build_date || current.build_date,
          build_label: chrome.build_label || current.build_label,
          public_site_url: chrome.public_site_url || current.public_site_url
        } : current);
        if (chrome.latest_sync_run) {
          const previous = lastNotify.current;
          const newMessages = chrome.latest_sync_run.new_messages || 0;
          if (previous && previous.id === chrome.latest_sync_run.id && newMessages > previous.stored) {
            notifyNewMail(newMessages - previous.stored, chrome.latest_sync_run);
          }
          lastNotify.current = { id: chrome.latest_sync_run.id, stored: newMessages };
        }
      } catch {
        // Cached/offline views should stay usable if an event is malformed or missed.
      }
    });
    return () => {
      events.close();
    };
  }, [bootstrap?.user, notifyNewMail]);

  // Folder drag/drop hides rows optimistically only for moves. Ctrl/Cmd-drag
  // copies messages to the destination and leaves the source list untouched.
  const moveMessages = useCallback(
    async (messageIDs: number[], mailbox: MoveTarget, action: MessageTransferAction = "move") => {
      if (!bootstrap?.csrf) return;
      const ids = Array.from(new Set(messageIDs.filter((id) => Number.isFinite(id) && id > 0)));
      if (ids.length === 0) return;
      const copying = action === "copy";
      if (!copying) {
        setHiddenMessageIDs((current) => {
          const next = new Set(current);
          ids.forEach((id) => next.add(id));
          return next;
        });
      }
      const verb = copying ? "Copying" : "Moving";
      const toastID = addToast(`${verb} ${ids.length.toLocaleString()} ${ids.length === 1 ? "message" : "messages"} to ${mailbox.name}...`, "loading");
      try {
        if (copying) {
          const data = await api.bulkCopyMessages(bootstrap.csrf, ids, mailbox.id);
          if (data.queued) {
            updateToast(toastID, `Copy task started for ${ids.length.toLocaleString()} messages.`, "success");
          } else {
            updateToast(toastID, `Copied ${(data.copied || ids.length).toLocaleString()} ${ids.length === 1 ? "message" : "messages"} to ${mailbox.name}.`, "success");
          }
          return;
        }
        const data = ids.length === 1
          ? await api.moveMessage(bootstrap.csrf, ids[0], mailbox.id).then((res) => ({ ...res, queued: false, moved: 1 }))
          : await api.bulkMoveMessages(bootstrap.csrf, ids, mailbox.id);
        if (data.queued) {
          updateToast(toastID, `Move task started for ${ids.length.toLocaleString()} messages.`, "success");
        } else {
          updateToast(toastID, `Moved ${(data.moved || ids.length).toLocaleString()} ${ids.length === 1 ? "message" : "messages"} to ${mailbox.name}.`, "success");
        }
      } catch (err) {
        if (!copying) {
          setHiddenMessageIDs((current) => {
            const next = new Set(current);
            ids.forEach((id) => next.delete(id));
            return next;
          });
        }
        updateToast(toastID, `${copying ? "Copy" : "Move"} failed: ${messageFromError(err)}`, "error");
      }
    },
    [addToast, bootstrap?.csrf, updateToast]
  );

  const logout = useCallback(async () => {
    if (!bootstrap?.csrf) return;
    await api.logout(bootstrap.csrf);
    applyPGPUnlock(emptyPGPUnlockState, true);
    setPGPUnlockOpen(false);
    setPGPUnlockIdentityID(null);
    pgpUnlockCallbackRef.current = null;
    setBootstrap((current) => (current ? { ...current, user: null, mailboxes: [] } : current));
    navigate("/login");
  }, [applyPGPUnlock, bootstrap?.csrf, navigate]);

  const openCompose = useCallback((query = "") => {
    setComposeOverlayQuery(query.replace(/^\?/, ""));
  }, []);

  if (!bootstrap) {
    return (
      <div className="auth-page">
        <div className="auth-brand"><LogoMark />rolltop</div>
        {bootError ? <div className="error">{bootError}</div> : <div className="panel muted">Loading mail...</div>}
        <ToastStack toasts={toasts} onDismiss={removeToast} />
      </div>
    );
  }

  if (!bootstrap.users_exist || !bootstrap.user) {
    return (
      <>
        {!bootstrap.users_exist ? (
          <SetupPage csrf={bootstrap.csrf} onReady={refreshBootstrap} navigate={navigate} />
        ) : location.path === "/reset-password" ? (
          <PasswordResetPage csrf={bootstrap.csrf} token={new URLSearchParams(location.search).get("token") || ""} onReady={refreshBootstrap} navigate={navigate} />
        ) : (
          <LoginPage csrf={bootstrap.csrf} onReady={refreshBootstrap} navigate={navigate} />
        )}
        <ToastStack toasts={toasts} onDismiss={removeToast} />
      </>
    );
  }

  return (
    <>
      <AppShell
        user={bootstrap.user}
        csrf={bootstrap.csrf}
        mailboxes={bootstrap.mailboxes || []}
        latestSyncRun={bootstrap.latest_sync_run || null}
        activeSyncRuns={bootstrap.active_sync_runs || []}
        syncRunning={Boolean(bootstrap.sync_running)}
        accountNeedsPassword={Boolean(bootstrap.account_needs_password)}
        accountNotice={bootstrap.account_notice || ""}
        enabledPlugins={bootstrap.enabled_plugins || []}
        serverStartedAt={bootstrap.server_started_at || ""}
        serverUptimeSeconds={bootstrap.server_uptime_seconds || 0}
        buildVersion={bootstrap.build_version || ""}
        buildDate={bootstrap.build_date || ""}
        buildLabel={bootstrap.build_label || ""}
        location={location}
        navigate={navigate}
        onMoveMessages={moveMessages}
        openCompose={openCompose}
        refreshChrome={refreshBootstrap}
        notificationsEnabled={notificationsEnabled}
        toggleNotifications={toggleNotifications}
        pgpUnlock={pgpUnlock}
        openPGPUnlock={openPGPUnlock}
        lockPGP={lockPGP}
        logout={logout}
      >
        <RouteView
          csrf={bootstrap.csrf}
          user={bootstrap.user}
          mailboxes={bootstrap.mailboxes || []}
          latestSyncRun={bootstrap.latest_sync_run || null}
          activeSyncRuns={bootstrap.active_sync_runs || []}
          enabledPlugins={bootstrap.enabled_plugins || []}
          location={location}
          navigate={navigate}
          hiddenMessageIDs={hiddenMessageIDs}
          openCompose={openCompose}
          refreshChrome={refreshBootstrap}
          pgpUnlock={pgpUnlock}
          openPGPUnlock={openPGPUnlock}
          addToast={addToast}
        />
      </AppShell>
      {composeOverlayQuery !== null ? (
        <ComposeOverlay
          csrf={bootstrap.csrf}
          query={composeOverlayQuery}
          pgpEnabled={(bootstrap.enabled_plugins || []).includes("client_side_pgp")}
          pgpUnlock={pgpUnlock}
          openPGPUnlock={openPGPUnlock}
          addToast={addToast}
          onClose={() => setComposeOverlayQuery(null)}
        />
      ) : null}
      {pgpUnlockOpen ? (
        <PGPUnlockDialog
          userID={bootstrap.user.id}
          identityID={pgpUnlockIdentityID}
          recipientKeyIDs={pgpUnlockRecipientKeyIDs}
          onClose={closePGPUnlock}
          onUnlocked={(state) => {
            applyPGPUnlock(state, true);
            setPGPUnlockIdentityID(null);
            setPGPUnlockRecipientKeyIDs([]);
            const callback = pgpUnlockCallbackRef.current;
            pgpUnlockCallbackRef.current = null;
            callback?.(state);
          }}
          addToast={addToast}
        />
      ) : null}
      <ToastStack toasts={toasts} onDismiss={removeToast} />
    </>
  );
}

function PGPUnlockDialog({
  userID,
  identityID,
  recipientKeyIDs,
  onClose,
  onUnlocked,
  addToast
}: {
  userID: number;
  identityID: number | null;
  recipientKeyIDs: string[];
  onClose: () => void;
  onUnlocked: (state: PGPUnlockState) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [keys, setKeys] = useState<IdentityPGPPrivateKey[]>([]);
  const [selectedID, setSelectedID] = useState(0);
  const [passphrase, setPassphrase] = useState("");
  const [durationMinutes, setDurationMinutes] = useState(30);
  const [loading, setLoading] = useState(true);
  const [unlocking, setUnlocking] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    api.pgpPrivateKeys()
      .then((data) => {
        if (cancelled) return;
        return hydrateBrowserPGPPrivateKeys(userID, data.keys || []);
      })
      .then((list) => {
        if (cancelled || !list) return;
        const preferred = identityID ? list.find((key) => key.identity_id === identityID) : null;
        if (recipientKeyIDs.length === 0) {
          setKeys(list);
          setSelectedID(preferred?.id || list[0]?.id || 0);
          return;
        }
        return matchingPGPPrivateKeyIDForRecipients(list, recipientKeyIDs).then((matchedID) => {
          if (cancelled) return;
          setKeys(list);
          setSelectedID(matchedID || preferred?.id || list[0]?.id || 0);
        });
      })
      .catch((err) => {
        if (!cancelled) setError(messageFromError(err));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => { cancelled = true; };
  }, [identityID, recipientKeyIDs, userID]);

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
