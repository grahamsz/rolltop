// File overview: Root React coordinator for bootstrap, session routing, global chrome state,
// toast lifecycle, server-sent events, browser-history navigation, and the compose overlay.

import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import type { Bootstrap, ChromeEvent, SyncRun, ThemeDefinition } from "./types";
import type { LocationState, MessageTransferAction, MoveTarget, SecurityUnlockState, Toast } from "./appTypes";
import { ToastStack } from "./components/common";
import { LogoMark } from "./components/Icon";
import { SetupPage, LoginPage, PasswordResetPage } from "./features/auth/AuthPages";
import { AppShell } from "./features/layout/AppShell";
import { ComposeOverlay } from "./features/compose/ComposeViews";
import { RouteView } from "./RouteView";
import { messageFromError } from "./lib/errors";
import { currentLocation } from "./lib/routes";
import { emptyRuntimePlugins, loadRuntimePlugins, type RuntimePlugins } from "./plugins/runtime";
import { emptySecurityUnlockState, securityUnlockPlugin } from "./plugins/securityUnlock";


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
const pluginThemeLinkID = "rolltop-plugin-theme-css";
const notificationIconURL = "/icon.svg?v=transparent-logo-v2";

function themeChoices(themes: ThemeDefinition[] | undefined): ThemeDefinition[] {
  return themes && themes.length > 0 ? themes : [
    { id: "classic", name: "Classic" },
    { id: "classic_dark", name: "Classic Dark" }
  ];
}

function loadPluginThemeCSS(theme: ThemeDefinition | undefined) {
  let link = document.getElementById(pluginThemeLinkID) as HTMLLinkElement | null;
  const href = theme?.css_url || "";
  if (!href) {
    link?.remove();
    return;
  }
  if (!link) {
    link = document.createElement("link");
    link.id = pluginThemeLinkID;
    link.rel = "stylesheet";
    document.head.appendChild(link);
  }
  if (link.href !== new URL(href, window.location.href).href) {
    link.href = href;
  }
}

type SecurityUnlockWorkerMessage = {
  type: "rolltop:security-unlock-get" | "rolltop:security-unlock-set";
  userID: number;
  state?: unknown;
};

function postServiceWorkerMessage(message: SecurityUnlockWorkerMessage) {
  if (!("serviceWorker" in navigator)) return;
  const controller = navigator.serviceWorker.controller;
  if (controller) {
    controller.postMessage(message);
    return;
  }
  navigator.serviceWorker.ready
    .then((registration) => registration.active?.postMessage(message))
    .catch(() => {
      // The tab-level unlock state still works without the service worker.
    });
}

async function publishSecurityUnlockToWorker(userID: number, state: SecurityUnlockState, runtimePlugins: RuntimePlugins) {
  const plugin = securityUnlockPlugin(runtimePlugins.all);
  if (!plugin) return;
  const serialized = await plugin.serializeUnlockState(state);
  postServiceWorkerMessage({ type: "rolltop:security-unlock-set", userID, state: serialized });
}

function requestSecurityUnlockFromWorker(userID: number) {
  postServiceWorkerMessage({ type: "rolltop:security-unlock-get", userID });
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
  const [runtimePlugins, setRuntimePlugins] = useState<RuntimePlugins>(() => emptyRuntimePlugins());
  const [bootError, setBootError] = useState("");
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [hiddenMessageIDs, setHiddenMessageIDs] = useState<Set<number>>(() => new Set());
  const [composeOverlayQuery, setComposeOverlayQuery] = useState<string | null>(null);
  const toastSeq = useRef(1);
  const lastNotify = useRef<{ id: number; stored: number } | null>(null);
  const lastMouseActivityAt = useRef(Date.now());
  const lastAllMailWakePrefetchAt = useRef(0);
  const [notificationsEnabled, setNotificationsEnabled] = useState(initialNotificationsEnabled);
  const [securityUnlock, setSecurityUnlock] = useState<SecurityUnlockState>({ unlockedUntil: 0, keys: [] });
  const [securityUnlockOpen, setSecurityUnlockOpen] = useState(false);
  const [securityUnlockIdentityID, setSecurityUnlockIdentityID] = useState<number | null>(null);
  const [securityUnlockRecipientKeyIDs, setSecurityUnlockRecipientKeyIDs] = useState<string[]>([]);
  const [securityUnlockFallbackEmail, setSecurityUnlockFallbackEmail] = useState("");
  const securityUnlockCallbackRef = useRef<((state: SecurityUnlockState) => void) | null>(null);
  const securityUnlockRef = useRef<SecurityUnlockState>(emptySecurityUnlockState);
  const activeUserIDRef = useRef<number | null>(null);
  const runtimePluginsRef = useRef<RuntimePlugins>(emptyRuntimePlugins());

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
    const choices = themeChoices(bootstrap?.available_themes);
    const selected = choices.find((choice) => choice.id === savedTheme) || choices.find((choice) => choice.id === "classic");
    loadPluginThemeCSS(selected);
    document.documentElement.dataset.theme = selected?.id || "classic";
  }, [bootstrap?.user?.theme, bootstrap?.available_themes]);

  useEffect(() => {
    const userID = bootstrap?.user?.id || null;
    if (activeUserIDRef.current === userID) return;
    const previousUserID = activeUserIDRef.current;
    activeUserIDRef.current = userID;
    securityUnlockRef.current = emptySecurityUnlockState;
    setSecurityUnlock(emptySecurityUnlockState);
    setSecurityUnlockOpen(false);
    setSecurityUnlockIdentityID(null);
    setSecurityUnlockRecipientKeyIDs([]);
    setSecurityUnlockFallbackEmail("");
    securityUnlockCallbackRef.current = null;
    if (previousUserID) void publishSecurityUnlockToWorker(previousUserID, emptySecurityUnlockState, runtimePluginsRef.current);
    if (userID) requestSecurityUnlockFromWorker(userID);
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

  useEffect(() => {
    let cancelled = false;
    void loadRuntimePlugins(bootstrap?.frontend_plugins || [])
      .then((plugins) => {
        if (cancelled) return;
        runtimePluginsRef.current = plugins;
        setRuntimePlugins(plugins);
      })
      .catch((err) => {
        if (!cancelled) addToast(messageFromError(err), "error");
      });
    return () => {
      cancelled = true;
    };
  }, [addToast, bootstrap?.frontend_plugins]);

  const applySecurityUnlock = useCallback((state: SecurityUnlockState, broadcast = false) => {
    securityUnlockRef.current = state;
    setSecurityUnlock(state);
    if (broadcast && activeUserIDRef.current) {
      void publishSecurityUnlockToWorker(activeUserIDRef.current, state, runtimePluginsRef.current);
    }
  }, []);

  const lockSecurity = useCallback(() => {
    applySecurityUnlock(emptySecurityUnlockState, true);
    setSecurityUnlockIdentityID(null);
    setSecurityUnlockRecipientKeyIDs([]);
    setSecurityUnlockFallbackEmail("");
    securityUnlockCallbackRef.current = null;
    const plugin = securityUnlockPlugin(runtimePluginsRef.current.all);
    addToast(plugin?.lockedToast || "Security keys locked.");
  }, [addToast, applySecurityUnlock]);

  const openSecurityUnlock = useCallback((identityID?: number, onUnlocked?: (state: SecurityUnlockState) => void, recipientKeyIDs: string[] = [], fallbackEmail = "") => {
    const plugin = securityUnlockPlugin(runtimePluginsRef.current.all);
    if (!plugin) {
      addToast("Message security is still loading. Try again in a moment.", "error");
      return;
    }
    setSecurityUnlockIdentityID(identityID || null);
    setSecurityUnlockRecipientKeyIDs(recipientKeyIDs);
    setSecurityUnlockFallbackEmail(fallbackEmail);
    securityUnlockCallbackRef.current = onUnlocked || null;
    setSecurityUnlockOpen(true);
  }, [addToast]);

  const closeSecurityUnlock = useCallback(() => {
    setSecurityUnlockOpen(false);
    setSecurityUnlockIdentityID(null);
    setSecurityUnlockRecipientKeyIDs([]);
    setSecurityUnlockFallbackEmail("");
    securityUnlockCallbackRef.current = null;
  }, []);

  useEffect(() => {
    if (!securityUnlock.unlockedUntil) return;
    const delay = Math.max(0, securityUnlock.unlockedUntil - Date.now());
    const timer = window.setTimeout(() => applySecurityUnlock(emptySecurityUnlockState, true), delay);
    return () => window.clearTimeout(timer);
  }, [applySecurityUnlock, securityUnlock.unlockedUntil]);

  useEffect(() => {
    const userID = bootstrap?.user?.id || 0;
    if (!userID || !("serviceWorker" in navigator)) return;
    let cancelled = false;
    function onMessage(event: MessageEvent) {
      const data = event.data as { type?: string; userID?: number; state?: unknown };
      if (data?.type === "rolltop:security-unlock-request" && data.userID === userID && securityUnlockRef.current.unlockedUntil > Date.now()) {
        void publishSecurityUnlockToWorker(userID, securityUnlockRef.current, runtimePluginsRef.current);
        return;
      }
      if (data?.type !== "rolltop:security-unlock-state" || data.userID !== userID || !data.state) return;
      const plugin = securityUnlockPlugin(runtimePluginsRef.current.all);
      if (!plugin) return;
      void plugin.restoreUnlockState(data.state).then((state) => {
        if (cancelled) return;
        securityUnlockRef.current = state;
        setSecurityUnlock(state);
        if (state.keys.length > 0) {
          setSecurityUnlockOpen(false);
          setSecurityUnlockIdentityID(null);
          const callback = securityUnlockCallbackRef.current;
          securityUnlockCallbackRef.current = null;
          callback?.(state);
        }
      });
    }
    navigator.serviceWorker.addEventListener("message", onMessage);
    requestSecurityUnlockFromWorker(userID);
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
      tag: "rolltop-new-mail",
      icon: notificationIconURL,
      badge: notificationIconURL
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
    applySecurityUnlock(emptySecurityUnlockState, true);
    setSecurityUnlockOpen(false);
    setSecurityUnlockIdentityID(null);
    securityUnlockCallbackRef.current = null;
    setBootstrap((current) => (current ? { ...current, user: null, mailboxes: [] } : current));
    navigate("/login");
  }, [applySecurityUnlock, bootstrap?.csrf, navigate]);

  const openCompose = useCallback((query = "") => {
    setComposeOverlayQuery(query.replace(/^\?/, ""));
  }, []);

  const unlockPlugin = securityUnlockPlugin(runtimePlugins.all);

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
        securityUnlockAvailable={Boolean(unlockPlugin)}
        securityUnlock={securityUnlock}
        openSecurityUnlock={openSecurityUnlock}
        lockSecurity={lockSecurity}
        logout={logout}
      >
        <RouteView
          csrf={bootstrap.csrf}
          user={bootstrap.user}
          mailboxes={bootstrap.mailboxes || []}
          latestSyncRun={bootstrap.latest_sync_run || null}
          activeSyncRuns={bootstrap.active_sync_runs || []}
          enabledPlugins={bootstrap.enabled_plugins || []}
          availableThemes={bootstrap.available_themes || []}
          location={location}
          navigate={navigate}
          hiddenMessageIDs={hiddenMessageIDs}
          openCompose={openCompose}
          refreshChrome={refreshBootstrap}
          runtimePlugins={runtimePlugins}
          securityUnlock={securityUnlock}
          openSecurityUnlock={openSecurityUnlock}
          addToast={addToast}
        />
      </AppShell>
      {composeOverlayQuery !== null ? (
        <ComposeOverlay
          csrf={bootstrap.csrf}
          query={composeOverlayQuery}
          securityEnabled={Boolean(unlockPlugin)}
          securityPlugins={runtimePlugins.all}
          securityUnlock={securityUnlock}
          openSecurityUnlock={openSecurityUnlock}
          addToast={addToast}
          onClose={() => setComposeOverlayQuery(null)}
        />
      ) : null}
      {securityUnlockOpen && unlockPlugin ? (
        <unlockPlugin.UnlockDialog
          userID={bootstrap.user.id}
          identityID={securityUnlockIdentityID}
          recipientKeyIDs={securityUnlockRecipientKeyIDs}
          fallbackEmail={securityUnlockFallbackEmail}
          onClose={closeSecurityUnlock}
          onUnlocked={(state) => {
            applySecurityUnlock(state, true);
            setSecurityUnlockIdentityID(null);
            setSecurityUnlockRecipientKeyIDs([]);
            setSecurityUnlockFallbackEmail("");
            const callback = securityUnlockCallbackRef.current;
            securityUnlockCallbackRef.current = null;
            callback?.(state);
          }}
          addToast={addToast}
        />
      ) : null}
      <ToastStack toasts={toasts} onDismiss={removeToast} />
    </>
  );
}
