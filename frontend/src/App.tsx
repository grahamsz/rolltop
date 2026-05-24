import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import type { Bootstrap, ChromeEvent } from "./types";
import type { LocationState, MoveTarget, Toast } from "./appTypes";
import { ToastStack } from "./components/common";
import { SetupPage, LoginPage } from "./features/auth/AuthPages";
import { AppShell } from "./features/layout/AppShell";
import { ComposeOverlay } from "./features/compose/ComposeViews";
import { RouteView } from "./RouteView";
import { messageFromError } from "./lib/errors";
import { currentLocation } from "./lib/routes";

export default function App() {
  const [location, setLocation] = useState<LocationState>(() => currentLocation());
  const [bootstrap, setBootstrap] = useState<Bootstrap | null>(null);
  const [bootError, setBootError] = useState("");
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [hiddenMessageIDs, setHiddenMessageIDs] = useState<Set<number>>(() => new Set());
  const [composeOverlayQuery, setComposeOverlayQuery] = useState<string | null>(null);
  const toastSeq = useRef(1);
  const lastNotify = useRef<{ id: number; stored: number } | null>(null);

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
    const theme = bootstrap?.user?.theme === "modern" ? "modern" : "classic";
    document.documentElement.dataset.theme = theme;
  }, [bootstrap?.user?.theme]);

  useEffect(() => {
    if (!bootstrap) return;
    if (!bootstrap.users_exist && location.path !== "/setup") {
      replaceRoute("/setup");
      return;
    }
    if (bootstrap.users_exist && !bootstrap.user && location.path !== "/login") {
      replaceRoute("/login");
      return;
    }
    if (bootstrap.user && (location.path === "/" || location.path === "/login" || location.path === "/setup")) {
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

  const notifyNewMail = useCallback((count: number) => {
    if (!("Notification" in window) || Notification.permission !== "granted" || count <= 0) return;
    new Notification("MailMirror", {
      body: count === 1 ? "1 new message synced." : `${count} new messages synced.`,
      tag: "mailmirror-new-mail"
    });
  }, []);

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
          active_sync_runs: chrome.active_sync_runs || []
        } : current);
        if (chrome.latest_sync_run) {
          const previous = lastNotify.current;
          const newMessages = chrome.latest_sync_run.new_messages || 0;
          if (previous && previous.id === chrome.latest_sync_run.id && newMessages > previous.stored) {
            notifyNewMail(newMessages - previous.stored);
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

  const moveMessages = useCallback(
    async (messageIDs: number[], mailbox: MoveTarget) => {
      if (!bootstrap?.csrf) return;
      const ids = Array.from(new Set(messageIDs.filter((id) => Number.isFinite(id) && id > 0)));
      if (ids.length === 0) return;
      setHiddenMessageIDs((current) => {
        const next = new Set(current);
        ids.forEach((id) => next.add(id));
        return next;
      });
      const toastID = addToast(`Moving ${ids.length.toLocaleString()} ${ids.length === 1 ? "message" : "messages"} to ${mailbox.name}...`, "loading");
      try {
        const data = ids.length === 1
          ? await api.moveMessage(bootstrap.csrf, ids[0], mailbox.id).then((res) => ({ ...res, queued: false, moved: 1 }))
          : await api.bulkMoveMessages(bootstrap.csrf, ids, mailbox.id);
        if (data.queued) {
          updateToast(toastID, `Move task started for ${ids.length.toLocaleString()} messages.`, "success");
        } else {
          updateToast(toastID, `Moved ${(data.moved || ids.length).toLocaleString()} ${ids.length === 1 ? "message" : "messages"} to ${mailbox.name}.`, "success");
        }
      } catch (err) {
        setHiddenMessageIDs((current) => {
          const next = new Set(current);
          ids.forEach((id) => next.delete(id));
          return next;
        });
        updateToast(toastID, `Move failed: ${messageFromError(err)}`, "error");
      }
    },
    [addToast, bootstrap?.csrf, updateToast]
  );

  const logout = useCallback(async () => {
    if (!bootstrap?.csrf) return;
    await api.logout(bootstrap.csrf);
    setBootstrap((current) => (current ? { ...current, user: null, mailboxes: [] } : current));
    navigate("/login");
  }, [bootstrap?.csrf, navigate]);

  const openCompose = useCallback((query = "") => {
    setComposeOverlayQuery(query.replace(/^\?/, ""));
  }, []);

  if (!bootstrap) {
    return (
      <div className="auth-page">
        <div className="auth-brand">MailMirror</div>
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
        location={location}
        navigate={navigate}
        logout={logout}
        onMoveMessages={moveMessages}
        openCompose={openCompose}
        refreshChrome={refreshBootstrap}
        addToast={addToast}
      >
        <RouteView
          csrf={bootstrap.csrf}
          user={bootstrap.user}
          mailboxes={bootstrap.mailboxes || []}
          latestSyncRun={bootstrap.latest_sync_run || null}
          activeSyncRuns={bootstrap.active_sync_runs || []}
          location={location}
          navigate={navigate}
          hiddenMessageIDs={hiddenMessageIDs}
          openCompose={openCompose}
          refreshChrome={refreshBootstrap}
          addToast={addToast}
        />
      </AppShell>
      {composeOverlayQuery !== null ? (
        <ComposeOverlay
          csrf={bootstrap.csrf}
          query={composeOverlayQuery}
          addToast={addToast}
          onClose={() => setComposeOverlayQuery(null)}
        />
      ) : null}
      <ToastStack toasts={toasts} onDismiss={removeToast} />
    </>
  );
}
