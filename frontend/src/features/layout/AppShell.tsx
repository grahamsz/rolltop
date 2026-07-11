// File overview: Authenticated application chrome: top bar, search entry, folder sidebar, mobile
// drawer, drag-to-folder handling, sync status, and the mobile compose affordance.

import { useMemo, useState, useEffect, useRef } from "react";
import type { DragEvent, FormEvent, MouseEvent, ReactNode } from "react";
import { api } from "../../api";
import type { AppShellProps, LocationState, MessageTransferAction, MoveTarget, SecurityUnlockState } from "../../appTypes";
import type { Bootstrap, Mailbox, SyncRun, User } from "../../types";
import { Icon, LogoMark } from "../../components/Icon";
import { folderTree, nodeContainsMailbox, type FolderNode } from "../../lib/folders";
import { shouldAdvertiseAndroidApp } from "../../lib/androidNative";
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
  serverStartedAt,
  serverUptimeSeconds,
  buildVersion,
  buildDate,
  buildLabel,
  accountNeedsPassword,
  accountNotice,
  enabledPlugins,
  location,
  navigate,
  onMoveMessages,
  openCompose,
  refreshChrome,
  notificationsEnabled,
  toggleNotifications,
  securityUnlockAvailable,
  securityUnlock,
  openSecurityUnlock,
  lockSecurity,
  logout,
  children
}: AppShellProps) {
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  const [messageDragActive, setMessageDragActive] = useState(false);
  const dragOpenedSidebar = useRef(false);
  const dragCloseTimer = useRef<number | null>(null);

  useEffect(() => {
    return () => {
      if (dragCloseTimer.current !== null) window.clearTimeout(dragCloseTimer.current);
    };
  }, []);

  function clearDragCloseTimer() {
    if (dragCloseTimer.current === null) return;
    window.clearTimeout(dragCloseTimer.current);
    dragCloseTimer.current = null;
  }

  function openMobileSidebar() {
    clearDragCloseTimer();
    dragOpenedSidebar.current = false;
    setMobileSidebarOpen(true);
  }

  function closeMobileSidebar() {
    clearDragCloseTimer();
    dragOpenedSidebar.current = false;
    setMobileSidebarOpen(false);
  }

  function beginMessageDrag(event: DragEvent<HTMLDivElement>) {
    if (!isRolltopMessageDrag(event)) return;
    clearDragCloseTimer();
    setMessageDragActive(true);
    if (!window.matchMedia("(max-width: 760px)").matches || mobileSidebarOpen) return;
    dragOpenedSidebar.current = true;
    setMobileSidebarOpen(true);
  }

  function endMessageDrag(event: DragEvent<HTMLDivElement>) {
    if (!isRolltopMessageDrag(event)) return;
    setMessageDragActive(false);
    if (!dragOpenedSidebar.current) return;
    clearDragCloseTimer();
    dragCloseTimer.current = window.setTimeout(() => {
      dragCloseTimer.current = null;
      dragOpenedSidebar.current = false;
      setMobileSidebarOpen(false);
    }, 120);
  }

  function composeFromMobile() {
    closeMobileSidebar();
    openCompose("");
  }

  return (
    <>
      <Topbar
        user={user}
        mailboxes={mailboxes}
        enabledPlugins={enabledPlugins}
        location={location}
        navigate={navigate}
        notificationsEnabled={notificationsEnabled}
        toggleNotifications={toggleNotifications}
        securityUnlockAvailable={securityUnlockAvailable}
        securityUnlock={securityUnlock}
        openSecurityUnlock={openSecurityUnlock}
        lockSecurity={lockSecurity}
        logout={logout}
        onMenu={openMobileSidebar}
      />
      <div
        className={`app ${messageDragActive ? "message-drag-active" : ""}`}
        onDragStart={beginMessageDrag}
        onDragEnd={endMessageDrag}
      >
        {mobileSidebarOpen ? (
          <button className="mobile-sidebar-scrim" type="button" aria-label="Close folders" onClick={closeMobileSidebar} />
        ) : null}
        <Sidebar
          mailboxes={mailboxes}
          csrf={csrf}
          latestSyncRun={latestSyncRun}
          activeSyncRuns={activeSyncRuns}
          syncRunning={syncRunning}
          serverStartedAt={serverStartedAt}
          serverUptimeSeconds={serverUptimeSeconds}
          buildVersion={buildVersion}
          buildDate={buildDate}
          buildLabel={buildLabel}
          currentPath={location.path}
          navigate={navigate}
          openCompose={openCompose}
          refreshChrome={refreshChrome}
          onMoveMessages={onMoveMessages}
          mobileOpen={mobileSidebarOpen}
          dragActive={messageDragActive}
          onClose={closeMobileSidebar}
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

function isRolltopMessageDrag(event: DragEvent<HTMLElement>) {
  const target = event.target;
  if (target instanceof Element && target.closest("[data-rolltop-message-drag]")) return true;
  const types = Array.from(event.dataTransfer.types);
  return types.includes("application/x-rolltop-message-transfer") ||
    types.includes("application/x-rolltop-messages") ||
    types.includes("application/x-rolltop-message");
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

function buildDisplayLabel(version: string, buildDate: string, fallbackLabel: string) {
  const trimmedVersion = version.trim();
  if (trimmedVersion && trimmedVersion.toLowerCase() !== "latest") return trimmedVersion;
  const parsed = Date.parse(buildDate || "");
  if (Number.isFinite(parsed)) {
    return `built ${new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", year: "numeric" }).format(parsed)}`;
  }
  return fallbackLabel.trim();
}

// Topbar owns the search input because search is global navigation, not part of
// a specific mailbox or message view.
function Topbar({
  user,
  mailboxes,
  enabledPlugins,
  location,
  navigate,
  notificationsEnabled,
  toggleNotifications,
  securityUnlockAvailable,
  securityUnlock,
  openSecurityUnlock,
  lockSecurity,
  logout,
  onMenu
}: {
  user: User;
  mailboxes: Mailbox[];
  enabledPlugins: string[];
  location: LocationState;
  navigate: (url: string) => void;
  notificationsEnabled: boolean;
  toggleNotifications: () => Promise<void>;
  securityUnlockAvailable: boolean;
  securityUnlock: SecurityUnlockState;
  openSecurityUnlock: (identityID?: number, onUnlocked?: (state: SecurityUnlockState) => void, recipientKeyIDs?: string[], fallbackEmail?: string) => void;
  lockSecurity: () => void;
  logout: () => Promise<void>;
  onMenu: () => void;
}) {
  const [query, setQuery] = useState(() => searchRoute(currentLocation().path).query);
  const [focused, setFocused] = useState(false);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const accountMenuRef = useRef<HTMLDetailsElement>(null);
  const pluginKey = enabledPlugins.join("|");
  const pluginSet = useMemo(() => createPluginSet(enabledPlugins), [pluginKey]);
  const securityUnlocked = securityUnlock.keys.length > 0 && securityUnlock.unlockedUntil > Date.now();
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

  function closeAccountMenu() {
    if (accountMenuRef.current) accountMenuRef.current.open = false;
  }

  function menuNavigate(url: string) {
    closeAccountMenu();
    navigate(url);
  }

  async function menuToggleNotifications() {
    await toggleNotifications();
    closeAccountMenu();
  }

  async function menuLogout() {
    closeAccountMenu();
    await logout();
  }

  const accountLabel = user.name || user.email;

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
        <LogoMark />
        <span className="brand-wordmark">rolltop</span>
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
        {securityUnlockAvailable ? (
          <button
            className={securityUnlocked ? "ghost security-lock-toggle active" : "ghost security-lock-toggle"}
            type="button"
            title={securityUnlocked ? "Lock security keys" : "Unlock security key"}
            onClick={securityUnlocked ? lockSecurity : () => openSecurityUnlock()}
          >
            <Icon name={securityUnlocked ? "lock_open" : "lock"} weight={securityUnlocked ? "bold" : "regular"} />
          </button>
        ) : null}
        <details className="account-menu" ref={accountMenuRef}>
          <summary className="user-chip account-menu-summary" title={accountLabel} aria-label="Account menu">
            <span>{accountLabel}</span>
            <Icon name="expand_more" />
          </summary>
          <div className="account-menu-panel" role="menu">
            <div className="account-menu-identity">
              <strong>{accountLabel}</strong>
              <small>{user.email}</small>
            </div>
            <button
              className={notificationsEnabled ? "account-menu-row account-menu-notifications active" : "account-menu-row account-menu-notifications"}
              type="button"
              role="switch"
              aria-checked={notificationsEnabled}
              onClick={() => void menuToggleNotifications()}
            >
              <Icon name="notifications" weight={notificationsEnabled ? "bold" : "regular"} />
              <span><strong>Browser notifications</strong><small>{notificationsEnabled ? "Enabled for new mail" : "Paused for this browser"}</small></span>
              <span className="notification-toggle-track"><span /></span>
            </button>
            <button className="account-menu-row" type="button" role="menuitem" onClick={() => menuNavigate("/settings/account")}>
              <Icon name="settings" />
              <span><strong>Settings</strong><small>Profile, servers, folders, and identities</small></span>
            </button>
            {user.is_admin ? (
              <button className="account-menu-row" type="button" role="menuitem" onClick={() => menuNavigate("/admin/users")}>
                <Icon name="group" />
                <span><strong>Admin panel</strong><small>Users and server-wide controls</small></span>
              </button>
            ) : null}
            <button className="account-menu-row danger" type="button" role="menuitem" onClick={() => void menuLogout()}>
              <Icon name="logout" />
              <span><strong>Log out</strong><small>End this browser session</small></span>
            </button>
          </div>
        </details>
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
  serverStartedAt,
  serverUptimeSeconds,
  buildVersion,
  buildDate,
  buildLabel,
  currentPath,
  navigate,
  openCompose,
  refreshChrome,
  onMoveMessages,
  mobileOpen,
  dragActive,
  onClose
}: {
  mailboxes: Mailbox[];
  csrf: string;
  latestSyncRun: SyncRun | null;
  activeSyncRuns: SyncRun[];
  syncRunning: boolean;
  serverStartedAt: string;
  serverUptimeSeconds: number;
  buildVersion: string;
  buildDate: string;
  buildLabel: string;
  currentPath: string;
  navigate: (url: string) => void;
  openCompose: (query?: string) => void;
  refreshChrome: () => Promise<Bootstrap | null>;
  onMoveMessages: (messageIDs: number[], mailbox: MoveTarget, action?: MessageTransferAction) => void;
  mobileOpen: boolean;
  dragActive: boolean;
  onClose: () => void;
}) {
  const [dropID, setDropID] = useState<number | null>(null);
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(() => new Set());
  const uptimeLabel = useServerUptimeLabel(serverStartedAt, serverUptimeSeconds);
  const releaseLabel = buildDisplayLabel(buildVersion, buildDate, buildLabel);
  const uptimeParts = [uptimeLabel ? `Up ${uptimeLabel}` : "", releaseLabel].filter(Boolean);
  const activeMailbox = mailRoute(currentPath).mailboxID;
  const allMailActive = (currentPath === "/mail" || currentPath.startsWith("/mail/")) && !activeMailbox;
  const accountGroups = useMemo(() => sidebarAccountGroups(mailboxes), [mailboxes]);
  const advertiseAndroidApp = shouldAdvertiseAndroidApp();

  useEffect(() => {
    if (!dragActive) setDropID(null);
  }, [dragActive]);

  function open(event: MouseEvent, url: string) {
    event.preventDefault();
    navigate(url);
    onClose();
  }

  function canAcceptDraggedMessages(event: DragEvent) {
    const types = Array.from(event.dataTransfer.types);
    return types.includes("application/x-rolltop-message-transfer") || types.includes("application/x-rolltop-messages") || types.includes("application/x-rolltop-message");
  }

  function onDragEnter(event: DragEvent, mailboxID: number) {
    if (!canAcceptDraggedMessages(event)) return;
    event.preventDefault();
    setDropID(mailboxID);
  }

  function dragCopyRequested(event: DragEvent) {
    return event.ctrlKey || event.metaKey || event.dataTransfer.dropEffect === "copy";
  }

  function onDragOver(event: DragEvent, mailboxID: number) {
    if (!canAcceptDraggedMessages(event)) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = dragCopyRequested(event) ? "copy" : "move";
    setDropID(mailboxID);
  }

  function onDragLeave(event: DragEvent, mailboxID: number) {
    const nextTarget = event.relatedTarget;
    if (nextTarget instanceof Node && event.currentTarget.contains(nextTarget)) return;
    setDropID((current) => current === mailboxID ? null : current);
  }

  function onDrop(event: DragEvent, mailbox: Mailbox) {
    event.preventDefault();
    setDropID(null);
    const transfer = event.dataTransfer.getData("application/x-rolltop-message-transfer");
    const bulk = event.dataTransfer.getData("application/x-rolltop-messages");
    let ids: number[] = [];
    let sourceAccountIDs: number[] = [];
    if (transfer) {
      try {
        const parsed = JSON.parse(transfer) as { ids?: unknown; account_ids?: unknown };
        if (Array.isArray(parsed.ids)) ids = parsed.ids.map((id) => Number(id)).filter((id) => Number.isFinite(id) && id > 0);
        if (Array.isArray(parsed.account_ids)) {
          sourceAccountIDs = parsed.account_ids.map((id) => Number(id)).filter((id) => Number.isFinite(id) && id > 0);
        }
      } catch {
        ids = [];
        sourceAccountIDs = [];
      }
    }
    if (ids.length === 0 && bulk) {
      try {
        const parsed = JSON.parse(bulk) as unknown;
        if (Array.isArray(parsed)) ids = parsed.map((id) => Number(id)).filter((id) => Number.isFinite(id) && id > 0);
      } catch {
        ids = [];
      }
    }
    if (ids.length === 0) {
      const raw = event.dataTransfer.getData("application/x-rolltop-message") || event.dataTransfer.getData("text/plain");
      const messageID = Number.parseInt(raw, 10);
      if (Number.isFinite(messageID) && messageID > 0) ids = [messageID];
    }
    if (ids.length > 0) {
      const crossAccount = sourceAccountIDs.some((accountID) => mailbox.account_id > 0 && accountID !== mailbox.account_id);
      onMoveMessages(ids, { id: mailbox.id, name: mailbox.name }, crossAccount || dragCopyRequested(event) ? "copy" : "move");
      onClose();
    }
  }

  function toggleGroup(key: string) {
    setExpandedGroups((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
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
        className={`folder message-drop-target ${depth > 0 ? "folder-child" : ""} ${active ? "active" : ""} ${dropID === mailbox.id ? "drop-target" : ""}`}
        style={depth > 0 ? { paddingLeft: `${18 + depth * 18}px` } : undefined}
        key={mailbox.id}
        onClick={(event) => open(event, url)}
        onDragEnter={(event) => onDragEnter(event, mailbox.id)}
        onDragOver={(event) => onDragOver(event, mailbox.id)}
        onDragLeave={(event) => onDragLeave(event, mailbox.id)}
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
    const expandKey = folderExpandKey(node.mailbox);
    const expanded = expandedGroups.has(expandKey) || nodeContainsMailbox(node, activeMailbox);
    const url = mailURL(node.mailbox.id);
    return (
      <div className="folder-tree" key={node.mailbox.id}>
        <div
          className={`folder folder-parent message-drop-target ${depth > 0 ? "folder-child" : ""} ${active ? "active" : ""} ${dropID === node.mailbox.id ? "drop-target" : ""}`}
          style={depth > 0 ? { paddingLeft: `${18 + depth * 18}px` } : undefined}
          onDragEnter={(event) => onDragEnter(event, node.mailbox.id)}
          onDragOver={(event) => onDragOver(event, node.mailbox.id)}
          onDragLeave={(event) => onDragLeave(event, node.mailbox.id)}
          onDrop={(event) => onDrop(event, node.mailbox)}
        >
          <a href={url} className="folder-main" onClick={(event) => open(event, url)}>
            <span className="folder-name"><Icon name={node.mailbox.icon || "folder"} weight={active ? "bold" : undefined} />{node.label}</span>
          </a>
          {count > 0 ? <span className="folder-count">{count.toLocaleString()}</span> : null}
          <button className="folder-toggle" type="button" onClick={() => toggleGroup(expandKey)} title={expanded ? "Collapse folder" : "Expand folder"}>
            <Icon name={expanded ? "expand_more" : "chevron_right"} />
          </button>
        </div>
        {expanded ? <div className="folder-children">{node.children.map((child) => folderNode(child, depth + 1))}</div> : null}
      </div>
    );
  }

  return (
    <aside className={`sidebar ${mobileOpen ? "open" : ""} ${dragActive ? "message-drag-active" : ""}`}>
      <div className="sidebar-mobile-head">
        <span><Icon name="rolltop" />Folders</span>
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
        {accountGroups.map((group) => (
          <div className="account-folder-group" key={group.key}>
            <div className="account-section">{group.label}</div>
            {group.folders.map((node) => folderNode(node))}
          </div>
        ))}
        <div className="side-section">Address Book</div>
        <a
          href="/contacts"
          className={`folder ${currentPath === "/contacts" ? "active" : ""}`}
          onClick={(event) => open(event, "/contacts")}
        >
          <span className="folder-name"><Icon name="group" weight={currentPath === "/contacts" ? "bold" : undefined} />Contacts</span>
        </a>
        {advertiseAndroidApp ? (
          <>
            <div className="side-section">Android app</div>
            <a href="/android/rolltop.apk" className="folder android-app-download" download="rolltop.apk">
              <span className="folder-name"><Icon name="android" weight="fill" />Get Rolltop for Android</span>
              <Icon name="download" />
            </a>
          </>
        ) : null}
      </div>
      <SidebarSync csrf={csrf} latest={latestSyncRun} activeRuns={activeSyncRuns} running={syncRunning} refreshChrome={refreshChrome} />
      {uptimeParts.length > 0 ? (
        <div className="sidebar-uptime" title={serverStartedAt ? `Started ${new Date(serverStartedAt).toLocaleString()}` : "Server uptime"}>
          {uptimeParts.join(" · ")}
        </div>
      ) : null}
      <div className="sidebar-license">
        GNU AGPLv3-or-later
      </div>
    </aside>
  );
}

type SidebarAccountGroup = {
  key: string;
  label: string;
  folders: FolderNode[];
};

function sidebarAccountGroups(mailboxes: Mailbox[]): SidebarAccountGroup[] {
  const grouped = new Map<string, { key: string; label: string; mailboxes: Mailbox[] }>();
  for (const mailbox of mailboxes) {
    const key = mailbox.account_id ? String(mailbox.account_id) : `email:${mailbox.account_email || "local"}`;
    const existing = grouped.get(key);
    if (existing) {
      existing.mailboxes.push(mailbox);
      continue;
    }
    grouped.set(key, { key, label: mailboxAccountLabel(mailbox), mailboxes: [mailbox] });
  }

  const groups = Array.from(grouped.values());
  const labelCounts = groups.reduce((counts, group) => {
    counts.set(group.label, (counts.get(group.label) || 0) + 1);
    return counts;
  }, new Map<string, number>());

  return groups
    .map((group) => ({
      key: group.key,
      label: (labelCounts.get(group.label) || 0) > 1 ? `${group.label} · Account ${group.key}` : group.label,
      folders: folderTree(group.mailboxes)
    }))
    .filter((group) => group.folders.length > 0);
}

function mailboxAccountLabel(mailbox: Mailbox): string {
  const label = (mailbox.account_label || mailbox.account_email || "").trim();
  if (label) return label;
  return mailbox.account_id ? `Account ${mailbox.account_id}` : "Mail account";
}

function folderExpandKey(mailbox: Mailbox): string {
  return `${mailbox.account_id}:${mailbox.name}`;
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
  const isPurge = run.latest_new_from === "rolltop:maintenance" && run.latest_new_subject.trim().toLowerCase().startsWith("purging");
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
