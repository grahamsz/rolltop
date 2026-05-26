// File overview: Authenticated application chrome: top bar, search entry, folder sidebar, mobile
// drawer, drag-to-folder handling, sync status, and the mobile compose affordance.

import { useMemo, useState, useEffect, useRef } from "react";
import type { DragEvent, FormEvent, MouseEvent, ReactNode } from "react";
import { api } from "../../api";
import type { AppShellProps, LocationState, MoveTarget } from "../../appTypes";
import type { Bootstrap, Mailbox, SyncRun, User } from "../../types";
import { Icon } from "../../components/Icon";
import { folderTree, nodeContainsMailbox, type FolderNode } from "../../lib/folders";
import { mailRoute, mailURL, searchRoute, searchURL, currentLocation } from "../../lib/routes";
import { createPluginSet } from "../../plugins/registry";
import { SearchAutocomplete, useSearchAutocomplete } from "./SearchAutocomplete";

/**
 * AppShell renders everything that survives route changes after login: topbar,
 * folder navigation, sync widget, account warnings, mobile drawer state, and the
 * floating compose action. Children supply only the current content view.
 */
export function AppShell({
  user,
  csrf,
  mailboxes,
  latestSyncRun,
  activeSyncRuns,
  syncRunning,
  accountNeedsPassword,
  accountNotice,
  enabledPlugins,
  serverStartedAt,
  serverUptimeSeconds,
  location,
  navigate,
  logout,
  onMoveMessages,
  openCompose,
  refreshChrome,
  notificationsEnabled,
  toggleNotifications,
  children
}: AppShellProps) {
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);

  function composeFromMobile() {
    setMobileSidebarOpen(false);
    openCompose("");
  }

  return (
    <>
      <Topbar
        user={user}
        mailboxes={mailboxes}
        enabledPlugins={enabledPlugins}
        serverStartedAt={serverStartedAt}
        serverUptimeSeconds={serverUptimeSeconds}
        location={location}
        navigate={navigate}
        logout={logout}
        notificationsEnabled={notificationsEnabled}
        toggleNotifications={toggleNotifications}
        onMenu={() => setMobileSidebarOpen(true)}
      />
      <div className="app">
        {mobileSidebarOpen ? (
          <button className="mobile-sidebar-scrim" type="button" aria-label="Close folders" onClick={() => setMobileSidebarOpen(false)} />
        ) : null}
        <Sidebar
          mailboxes={mailboxes}
          csrf={csrf}
          latestSyncRun={latestSyncRun}
          activeSyncRuns={activeSyncRuns}
          syncRunning={syncRunning}
          currentPath={location.path}
          navigate={navigate}
          openCompose={openCompose}
          refreshChrome={refreshChrome}
          onMoveMessages={onMoveMessages}
          mobileOpen={mobileSidebarOpen}
          onClose={() => setMobileSidebarOpen(false)}
        />
        <main className="content">
          {accountNeedsPassword ? <AccountCredentialBanner notice={accountNotice} navigate={navigate} /> : null}
          {children}
        </main>
      </div>
      <button className="mobile-compose-fab" type="button" onClick={composeFromMobile} aria-label="Compose">
        <Icon name="edit" weight="bold" />
        <span>Compose</span>
      </button>
    </>
  );
}

// This banner is intentionally high in the shell so a broken master key or
// undecryptable IMAP password is visible on every authenticated page.
function AccountCredentialBanner({ notice, navigate }: { notice: string; navigate: (url: string) => void }) {
  return (
    <section className="account-alert" role="alert">
      <Icon name="report" weight="duotone" />
      <div>
        <strong>IMAP password required</strong>
        <span>{notice || "The saved IMAP password cannot be decrypted. Re-enter it to restore sync and full-message loading."}</span>
      </div>
      <button type="button" onClick={() => navigate("/settings/account")}>Re-enter password</button>
    </section>
  );
}

function useServerUptimeLabel(startedAt: string, fallbackSeconds: number) {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, [startedAt]);

  const started = Date.parse(startedAt || "");
  const seconds = Number.isFinite(started)
    ? Math.max(0, Math.floor((now - started) / 1000))
    : Math.max(0, Math.floor(fallbackSeconds || 0));
  return formatUptime(seconds);
}

function formatUptime(totalSeconds: number) {
  if (!Number.isFinite(totalSeconds) || totalSeconds <= 0) return "";
  const days = Math.floor(totalSeconds / 86_400);
  const hours = Math.floor((totalSeconds % 86_400) / 3_600);
  const minutes = Math.floor((totalSeconds % 3_600) / 60);
  if (days > 0) return `${days}d ${hours}h`;
  if (hours > 0) return `${hours}h ${minutes}m`;
  return `${Math.max(1, minutes)}m`;
}

// Topbar owns the search input because search is global navigation, not part of
// a specific mailbox or message view.
function Topbar({
  user,
  mailboxes,
  enabledPlugins,
  serverStartedAt,
  serverUptimeSeconds,
  location,
  navigate,
  logout,
  notificationsEnabled,
  toggleNotifications,
  onMenu
}: {
  user: User;
  mailboxes: Mailbox[];
  enabledPlugins: string[];
  serverStartedAt: string;
  serverUptimeSeconds: number;
  location: LocationState;
  navigate: (url: string) => void;
  logout: () => void;
  notificationsEnabled: boolean;
  toggleNotifications: () => Promise<void>;
  onMenu: () => void;
}) {
  const [query, setQuery] = useState(() => searchRoute(currentLocation().path).query);
  const [focused, setFocused] = useState(false);
  const uptimeLabel = useServerUptimeLabel(serverStartedAt, serverUptimeSeconds);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const pluginKey = enabledPlugins.join("|");
  const pluginSet = useMemo(() => createPluginSet(enabledPlugins), [pluginKey]);
  const autocomplete = useSearchAutocomplete({
    query,
    focused,
    inputRef: searchInputRef,
    mailboxes,
    pluginSet,
    setQuery
  });

  useEffect(() => {
    setQuery(searchRoute(location.path).query);
  }, [location.path]);

  function submit(event: FormEvent) {
    event.preventDefault();
    const trimmed = query.trim();
    if (trimmed === "") {
      navigate("/mail");
      return;
    }
    navigate(searchURL(trimmed));
  }

  return (
    <header className="topbar">
      <button className="ghost mobile-menu-button" type="button" title="Folders" aria-label="Folders" onClick={onMenu}>
        <Icon name="menu" />
      </button>
      <a
        href="/mail"
        className="brand"
        onClick={(event) => {
          event.preventDefault();
          navigate("/mail");
        }}
      >
        <Icon name="mailmirror" />
        mailmirror
      </a>
      <form className="top-search" onSubmit={submit}>
        <Icon name="search" />
        <input
          ref={searchInputRef}
          type="search"
          placeholder="Search mail"
          value={query}
          onFocus={() => setFocused(true)}
          onBlur={() => window.setTimeout(() => setFocused(false), 120)}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={autocomplete.onKeyDown}
          autoComplete="off"
        />
        {focused ? <SearchAutocomplete items={autocomplete.items} activeIndex={autocomplete.activeIndex} onChoose={autocomplete.choose} /> : null}
      </form>
      <nav className="top-actions" aria-label="Account">
        <button
          className={notificationsEnabled ? "notification-toggle active" : "notification-toggle"}
          type="button"
          role="switch"
          aria-checked={notificationsEnabled}
          title={notificationsEnabled ? "Pause notifications" : "Enable notifications"}
          onClick={() => void toggleNotifications()}
        >
          <Icon name="notifications" weight={notificationsEnabled ? "bold" : "regular"} />
          <span className="notification-toggle-track"><span /></span>
        </button>
        <button className="ghost settings-action" type="button" title="Settings" onClick={() => navigate("/settings/account")}>
          <Icon name="settings" />
        </button>
        {user.is_admin ? (
          <button className="ghost admin-action" type="button" title="Users" onClick={() => navigate("/admin/users")}>
            <Icon name="group" />
          </button>
        ) : null}
        {uptimeLabel ? <span className="uptime-chip" title={serverStartedAt ? `Started ${new Date(serverStartedAt).toLocaleString()}` : "Server uptime"}>Up {uptimeLabel}</span> : null}
        <span className="user-chip">{user.name || user.email}</span>
        <button className="secondary" type="button" onClick={logout}>Logout</button>
      </nav>
    </header>
  );
}

// Sidebar turns flat mailbox summaries into a tree, supports folder navigation,
// and accepts dragged message IDs from the message list.
function Sidebar({
  mailboxes,
  csrf,
  latestSyncRun,
  activeSyncRuns,
  syncRunning,
  currentPath,
  navigate,
  openCompose,
  refreshChrome,
  onMoveMessages,
  mobileOpen,
  onClose
}: {
  mailboxes: Mailbox[];
  csrf: string;
  latestSyncRun: SyncRun | null;
  activeSyncRuns: SyncRun[];
  syncRunning: boolean;
  currentPath: string;
  navigate: (url: string) => void;
  openCompose: (query?: string) => void;
  refreshChrome: () => Promise<Bootstrap | null>;
  onMoveMessages: (messageIDs: number[], mailbox: MoveTarget) => void;
  mobileOpen: boolean;
  onClose: () => void;
}) {
  const [dropID, setDropID] = useState<number | null>(null);
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(() => new Set());
  const activeMailbox = mailRoute(currentPath).mailboxID;
  const allMailActive = (currentPath === "/mail" || currentPath.startsWith("/mail/")) && !activeMailbox;
  const folders = useMemo(() => folderTree(mailboxes), [mailboxes]);

  function open(event: MouseEvent, url: string) {
    event.preventDefault();
    navigate(url);
    onClose();
  }

  function onDragOver(event: DragEvent, mailboxID: number) {
    const types = Array.from(event.dataTransfer.types);
    if (!types.includes("application/x-mailmirror-messages") && !types.includes("application/x-mailmirror-message")) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "move";
    setDropID(mailboxID);
  }

  function onDrop(event: DragEvent, mailbox: Mailbox) {
    event.preventDefault();
    setDropID(null);
    const bulk = event.dataTransfer.getData("application/x-mailmirror-messages");
    let ids: number[] = [];
    if (bulk) {
      try {
        const parsed = JSON.parse(bulk) as unknown;
        if (Array.isArray(parsed)) ids = parsed.map((id) => Number(id)).filter((id) => Number.isFinite(id) && id > 0);
      } catch {
        ids = [];
      }
    }
    if (ids.length === 0) {
      const raw = event.dataTransfer.getData("application/x-mailmirror-message") || event.dataTransfer.getData("text/plain");
      const messageID = Number.parseInt(raw, 10);
      if (Number.isFinite(messageID) && messageID > 0) ids = [messageID];
    }
    if (ids.length > 0) {
      onMoveMessages(ids, { id: mailbox.id, name: mailbox.name });
    }
  }

  function toggleGroup(name: string) {
    setExpandedGroups((current) => {
      const next = new Set(current);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  }

  function folderLink(mailbox: Mailbox, label = mailbox.name, depth = 0) {
    const active = currentPath.startsWith("/mailbox/") && activeMailbox === String(mailbox.id);
    const count = mailbox.unread_count;
    const url = mailURL(mailbox.id);
    return (
      <a
        href={url}
        className={`folder ${depth > 0 ? "folder-child" : ""} ${active ? "active" : ""} ${dropID === mailbox.id ? "drop-target" : ""}`}
        style={depth > 0 ? { paddingLeft: `${18 + depth * 18}px` } : undefined}
        key={mailbox.id}
        onClick={(event) => open(event, url)}
        onDragOver={(event) => onDragOver(event, mailbox.id)}
        onDragLeave={() => setDropID(null)}
        onDrop={(event) => onDrop(event, mailbox)}
      >
        <span className="folder-name"><Icon name={mailbox.icon || "folder"} weight={active ? "bold" : undefined} />{label}</span>
        {count > 0 ? <span className="folder-count">{count.toLocaleString()}</span> : null}
      </a>
    );
  }

  function folderNode(node: FolderNode, depth = 0): ReactNode {
    if (node.children.length === 0) return folderLink(node.mailbox, node.label, depth);
    const active = currentPath.startsWith("/mailbox/") && activeMailbox === String(node.mailbox.id);
    const count = node.mailbox.unread_count;
    const expanded = expandedGroups.has(node.mailbox.name) || nodeContainsMailbox(node, activeMailbox);
    const url = mailURL(node.mailbox.id);
    return (
      <div className="folder-tree" key={node.mailbox.id}>
        <div
          className={`folder folder-parent ${depth > 0 ? "folder-child" : ""} ${active ? "active" : ""} ${dropID === node.mailbox.id ? "drop-target" : ""}`}
          style={depth > 0 ? { paddingLeft: `${18 + depth * 18}px` } : undefined}
          onDragOver={(event) => onDragOver(event, node.mailbox.id)}
          onDragLeave={() => setDropID(null)}
          onDrop={(event) => onDrop(event, node.mailbox)}
        >
          <a href={url} className="folder-main" onClick={(event) => open(event, url)}>
            <span className="folder-name"><Icon name={node.mailbox.icon || "folder"} weight={active ? "bold" : undefined} />{node.label}</span>
          </a>
          {count > 0 ? <span className="folder-count">{count.toLocaleString()}</span> : null}
          <button className="folder-toggle" type="button" onClick={() => toggleGroup(node.mailbox.name)} title={expanded ? "Collapse folder" : "Expand folder"}>
            <Icon name={expanded ? "expand_more" : "chevron_right"} />
          </button>
        </div>
        {expanded ? <div className="folder-children">{node.children.map((child) => folderNode(child, depth + 1))}</div> : null}
      </div>
    );
  }

  return (
    <aside className={`sidebar ${mobileOpen ? "open" : ""}`}>
      <div className="sidebar-mobile-head">
        <span><Icon name="mailmirror" />Folders</span>
        <button className="ghost" type="button" title="Close folders" aria-label="Close folders" onClick={onClose}><Icon name="close" /></button>
      </div>
      <a href="/compose" className="button compose" onClick={(event) => {
        event.preventDefault();
        onClose();
        openCompose("");
      }}>
        <Icon name="edit" weight="bold" />
        Compose
      </a>
      <div className="sidebar-scroll">
        <a
          href="/mail"
          className={`folder ${allMailActive ? "active" : ""}`}
          onClick={(event) => open(event, "/mail")}
        >
          <span className="folder-name"><Icon name="mail" weight={allMailActive ? "bold" : undefined} />All Mail</span>
        </a>
        <div className="side-section">Folders</div>
        {Array.from(new Set(mailboxes.map((mailbox) => mailbox.account_email).filter(Boolean))).map((email) => (
          <div className="account-section" key={email}>{email}</div>
        ))}
        {folders.map((node) => folderNode(node))}
        <div className="side-section">Address Book</div>
        <a
          href="/contacts"
          className={`folder ${currentPath === "/contacts" ? "active" : ""}`}
          onClick={(event) => open(event, "/contacts")}
        >
          <span className="folder-name"><Icon name="group" weight={currentPath === "/contacts" ? "bold" : undefined} />Contacts</span>
        </a>
      </div>
      <SidebarSync csrf={csrf} latest={latestSyncRun} activeRuns={activeSyncRuns} running={syncRunning} refreshChrome={refreshChrome} />
      <div className="sidebar-license">
        GNU AGPLv3-or-later
      </div>
    </aside>
  );
}

function SidebarSync({
  csrf,
  latest,
  activeRuns,
  running,
  refreshChrome
}: {
  csrf: string;
  latest: SyncRun | null;
  activeRuns: SyncRun[];
  running: boolean;
  refreshChrome: () => Promise<Bootstrap | null>;
}) {
  const [busy, setBusy] = useState(false);
  const orderedActiveRuns = useMemo(() => stableSyncRunOrder(activeRuns), [activeRuns]);
  const visibleRuns = orderedActiveRuns.length > 0 ? orderedActiveRuns : latest ? [latest] : [];
  const isActive = activeRuns.length > 0 || running;

  async function startSync() {
    setBusy(true);
    try {
      await api.syncAccount(csrf);
      await refreshChrome();
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className={`sidebar-sync ${isActive ? "running" : "idle"}`}>
      <div className="sync-meta">
        <strong>{isActive ? `Syncing${activeRuns.length > 1 ? ` (${activeRuns.length})` : ""}` : "Sync"}</strong>
        <span>{latest ? `${latest.status}${latest.current_mailbox ? ` - ${latest.current_mailbox}` : ""}` : "never"}</span>
        <button className="secondary" type="button" disabled={busy || isActive} onClick={startSync}>
          <Icon name="sync" />
          {isActive ? "Syncing" : "Sync now"}
        </button>
      </div>
      <div className="sync-run-list">
        {visibleRuns.map((run) => (
          <SyncRunMini key={run.id} run={run} />
        ))}
      </div>
    </section>
  );
}


function stableSyncRunOrder(runs: SyncRun[]) {
  return [...runs].sort((a, b) => {
    const startedA = Date.parse(a.started_at) || 0;
    const startedB = Date.parse(b.started_at) || 0;
    if (startedA !== startedB) return startedA - startedB;
    return a.id - b.id;
  });
}

/** Render compact sync progress using message progress when available, falling back to folder progress. */
export function SyncRunMini({ run }: { run: SyncRun }) {
  const totalMessages = run.messages_total || 0;
  const totalFolders = run.mailboxes_total || 0;
  const progress = totalMessages > 0
    ? Math.min(100, Math.round((run.messages_seen / totalMessages) * 100))
      : totalFolders > 0
        ? Math.min(100, Math.round((run.mailboxes_done / totalFolders) * 100))
        : run.status === "running" ? 100 : 0;
  const isPurge = run.latest_new_from === "mailmirror:maintenance" && run.latest_new_subject.trim().toLowerCase().startsWith("purging");
  const indexedLabel = run.messages_stored > 0 ? `${run.messages_stored.toLocaleString()} indexed` : "Indexing...";
  const purgeLabel = totalMessages > 0
    ? `${run.messages_seen.toLocaleString()} of ${totalMessages.toLocaleString()} purged`
    : "Purging...";
  const detail = isPurge
    ? purgeLabel
    : run.messages_skipped > 0
      ? `${indexedLabel}, ${run.messages_skipped.toLocaleString()} skipped`
      : indexedLabel;
  return (
    <div className="sync-run-mini">
      <div className="sync-run-title">
        <span>{run.current_mailbox || run.status}</span>
        <span>{progress}%</span>
      </div>
      <div className="sync-run-detail">{detail}</div>
      <div className="progress" aria-label={`${run.current_mailbox || "Sync"} progress`}>
        <div style={{ width: `${progress}%` }} />
      </div>
    </div>
  );
}
