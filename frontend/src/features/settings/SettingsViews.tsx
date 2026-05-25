// File overview: Settings surface for profile preferences, IMAP servers, SMTP servers, outgoing
// identities, folder sync/indexing controls, storage usage, and admin plugin panels.

import { useCallback, useEffect, useMemo, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { api } from "../../api";
import type { DatePrefs, LocationState, Toast } from "../../appTypes";
import type { Account, Bootstrap, MailIdentity, PluginSetting, Mailbox, SMTPAccount, StorageStats, SyncFolder, SyncRun, User } from "../../types";
import { Icon } from "../../components/Icon";
import { Field, Stat } from "../../components/common";
import { emptyAccountForm, accountToForm } from "../../lib/accountForm";
import { messageFromError } from "../../lib/errors";
import { displayDateTime, displayTime, formatBytes } from "../../lib/format";
import { folderTree, type FolderNode } from "../../lib/folders";
import { mergeSyncRuns } from "../../lib/sync";
import { pluginIDs } from "../../plugins/registry";
import { AdminRemoteImageBlocklist } from "../../plugins/remoteImageBlocklist/AdminRemoteImageBlocklist";
import { PluginTogglePanel } from "./admin/PluginTogglePanel";

function emptySMTPForm() {
  return {
    label: "",
    host: "",
    port: "587",
    username: "",
    password: "",
    use_tls: true
  };
}

function smtpToForm(account: SMTPAccount | null) {
  if (!account) return emptySMTPForm();
  return {
    label: account.label || "",
    host: account.host || "",
    port: String(account.port || 587),
    username: account.username || "",
    password: "",
    use_tls: account.use_tls
  };
}

function emptyAccountFormForUser(user: User) {
  const email = user.email || "";
  return {
    ...emptyAccountForm(),
    email,
    label: email,
    username: email,
    mailbox: "*"
  };
}

function emptySMTPFormForUser(user: User) {
  const email = user.email || "";
  return {
    ...emptySMTPForm(),
    label: email,
    username: email
  };
}

type SettingsRoute = {
  kind: "main" | "imap" | "smtp";
  id: number | null;
  isNew: boolean;
};

type FolderSettingsDraft = Pick<Mailbox, "sync_mode" | "role" | "icon" | "show_in_sidebar" | "show_in_all_mail" | "include_in_search">;

// Settings uses real URL subpages for IMAP/SMTP editing so refresh/back keeps
// the selected server instead of returning to the settings index.
function settingsRouteFromPath(path: string): SettingsRoute {
  if (path === "/settings/account/imap/new") return { kind: "imap", id: null, isNew: true };
  if (path === "/settings/account/smtp/new") return { kind: "smtp", id: null, isNew: true };
  const imap = path.match(/^\/settings\/account\/imap\/(\d+)$/);
  if (imap) return { kind: "imap", id: Number(imap[1]), isNew: false };
  const smtp = path.match(/^\/settings\/account\/smtp\/(\d+)$/);
  if (smtp) return { kind: "smtp", id: Number(smtp[1]), isNew: false };
  return { kind: "main", id: null, isNew: false };
}

function percentValue(value: number | undefined) {
  const percent = Number(value || 0);
  if (!Number.isFinite(percent)) return 0;
  return Math.max(0, Math.min(100, Math.round(percent)));
}

const syncModeChoices = [
  { value: "inherit", label: "Inherit" },
  { value: "auto", label: "Auto" },
  { value: "manual", label: "Manual" },
  { value: "never", label: "Never" }
];

const folderRoleChoices = [
  { value: "", label: "Normal" },
  { value: "inbox", label: "Inbox" },
  { value: "sent", label: "Sent" },
  { value: "drafts", label: "Drafts" },
  { value: "trash", label: "Trash" }
];

const folderIconChoices = [
  { value: "folder", label: "Folder" },
  { value: "inbox", label: "Inbox" },
  { value: "archive", label: "Archive" },
  { value: "send", label: "Sent" },
  { value: "draft", label: "Draft" },
  { value: "delete", label: "Trash" },
  { value: "label", label: "Label" },
  { value: "shopping_bag", label: "Purchases" },
  { value: "report", label: "Spam" }
];

const folderVisibilityChoices = [
  { key: "show_in_sidebar", label: "Sidebar" },
  { key: "show_in_all_mail", label: "All Mail" },
  { key: "include_in_search", label: "Search" }
] as const;

function folderSettingsDraft(mailbox: Mailbox): FolderSettingsDraft {
  return {
    sync_mode: mailbox.sync_mode || "inherit",
    role: mailbox.role || "",
    icon: mailbox.icon || "folder",
    show_in_sidebar: mailbox.show_in_sidebar,
    show_in_all_mail: mailbox.show_in_all_mail,
    include_in_search: mailbox.include_in_search
  };
}

function folderSyncModeLabel(value: string) {
  return syncModeChoices.find((choice) => choice.value === value)?.label || "Inherit";
}

function folderRoleLabel(value: string) {
  return folderRoleChoices.find((choice) => choice.value === value)?.label || "Normal";
}

function folderIconLabel(value: string) {
  return folderIconChoices.find((choice) => choice.value === value)?.label || "Folder";
}

function folderVisibilityLabel(mailbox: Pick<Mailbox, "show_in_sidebar" | "show_in_all_mail" | "include_in_search">) {
  const selected = folderVisibilityChoices.filter((choice) => Boolean(mailbox[choice.key]));
  if (selected.length === folderVisibilityChoices.length) return "Sidebar, All Mail, Search";
  if (selected.length > 0) return selected.map((choice) => choice.label).join(", ");
  return "Hidden";
}

/**
 * SettingsView coordinates account data from /api/account with profile, storage,
 * IMAP, SMTP, identity, and folder-sync editors. The selected route determines
 * which server form is active while the main page remains a summary/dashboard.
 */
export function SettingsView({
  csrf,
  user,
  mailboxes,
  activeSyncRuns,
  location,
  navigate,
  refreshChrome,
  addToast
}: {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  activeSyncRuns: SyncRun[];
  location: LocationState;
  navigate: (url: string) => void;
  refreshChrome: () => Promise<Bootstrap | null>;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const route = settingsRouteFromPath(location.path);
  const [account, setAccount] = useState<Account | null>(null);
  const [imapAccounts, setIMAPAccounts] = useState<Account[]>([]);
  const [smtpAccounts, setSMTPAccounts] = useState<SMTPAccount[]>([]);
  const [identities, setIdentities] = useState<MailIdentity[]>([]);
  const [selectedAccountID, setSelectedAccountID] = useState<number | null>(null);
  const [selectedSMTPID, setSelectedSMTPID] = useState<number | null>(null);
  const [runs, setRuns] = useState<SyncRun[]>([]);
  const [folders, setFolders] = useState<SyncFolder[]>([]);
  const [storage, setStorage] = useState<StorageStats>({});
  const [storageLoading, setStorageLoading] = useState(true);
  const [storageError, setStorageError] = useState("");
  const [notice, setNotice] = useState("");
  const [accountNeedsPassword, setAccountNeedsPassword] = useState(false);
  const [form, setForm] = useState(() => emptyAccountForm());
  const [smtpForm, setSMTPForm] = useState(() => emptySMTPForm());
  const [profileForm, setProfileForm] = useState(() => ({
    date_locale: user.date_locale || "",
    date_format: user.date_format || "mdy",
    theme: ["classic_dark", "matrix"].includes(user.theme) ? user.theme : "classic"
  }));
  const [editingFolderID, setEditingFolderID] = useState<number | null>(null);
  const [folderDraft, setFolderDraft] = useState<FolderSettingsDraft | null>(null);
  const [loading, setLoading] = useState(true);

  const loadStorage = useCallback(async () => {
    setStorageLoading(true);
    setStorageError("");
    try {
      setStorage(await api.storage());
    } catch (err) {
      setStorageError(messageFromError(err));
    } finally {
      setStorageLoading(false);
    }
  }, []);

  // The account endpoint returns several related tables at once. Loading derives
  // selected IMAP/SMTP rows from the route, then rebuilds form state from those
  // records so direct links and browser back stay coherent.
  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.account();
      const routeForLoad = settingsRouteFromPath(location.path);
      const accounts = data.imap_accounts || [];
      const nextAccountID = routeForLoad.kind === "imap"
        ? routeForLoad.isNew
          ? null
          : routeForLoad.id && accounts.some((item) => item.id === routeForLoad.id)
            ? routeForLoad.id
            : null
        : selectedAccountID && accounts.some((item) => item.id === selectedAccountID)
          ? selectedAccountID
          : accounts[0]?.id || null;
      const nextAccount = accounts.find((item) => item.id === nextAccountID) || null;
      const smtp = data.smtp_accounts || [];
      const nextSMTPID = routeForLoad.kind === "smtp"
        ? routeForLoad.isNew
          ? null
          : routeForLoad.id && smtp.some((item) => item.id === routeForLoad.id)
            ? routeForLoad.id
            : null
        : selectedSMTPID && smtp.some((item) => item.id === selectedSMTPID)
          ? selectedSMTPID
          : smtp[0]?.id || null;
      const nextSMTP = smtp.find((item) => item.id === nextSMTPID) || null;
      setIMAPAccounts(accounts);
      setSMTPAccounts(smtp);
      setIdentities(data.identities || []);
      setSelectedAccountID(nextAccountID);
      setSelectedSMTPID(nextSMTPID);
      setAccount(nextAccount);
      setRuns(data.sync_runs);
      setFolders(data.sync_folders);
      setNotice(data.notice);
      setAccountNeedsPassword(Boolean(data.account_needs_password));
      setForm(nextAccount ? accountToForm(nextAccount) : emptyAccountFormForUser(user));
      setSMTPForm(nextSMTP ? smtpToForm(nextSMTP) : emptySMTPFormForUser(user));
      if (data.storage) {
        setStorage(data.storage);
        setStorageError("");
        setStorageLoading(false);
      } else {
        void loadStorage();
      }
    } finally {
      setLoading(false);
    }
  }, [loadStorage, location.path, selectedAccountID, selectedSMTPID, user]);

  useEffect(() => {
    void load().catch((err) => addToast(messageFromError(err), "error"));
  }, [addToast, load]);

  useEffect(() => {
    setProfileForm({
      date_locale: user.date_locale || "",
      date_format: user.date_format || "mdy",
      theme: ["classic_dark", "matrix"].includes(user.theme) ? user.theme : "classic"
    });
  }, [user.date_locale, user.date_format, user.theme]);

  useEffect(() => {
    setFolders((current) => current.map((folder) => {
      const mailbox = mailboxes.find((item) => item.id === folder.mailbox.id) || folder.mailbox;
      const activeRun = activeSyncRuns.find((run) =>
        run.status === "running" &&
        run.account_id === folder.mailbox.account_id &&
        run.current_mailbox.trim().toLowerCase() === folder.mailbox.name.trim().toLowerCase()
      );
      return {
        ...folder,
        mailbox: { ...folder.mailbox, message_count: mailbox.message_count, unread_count: mailbox.unread_count },
        is_running: Boolean(activeRun),
        last_run: activeRun || folder.last_run
      };
    }));
    if (activeSyncRuns.length > 0) {
      setRuns((current) => mergeSyncRuns(activeSyncRuns, current));
    }
  }, [mailboxes, activeSyncRuns]);

  // IMAP form edits keep common onboarding assumptions in sync: email seeds the
  // label/username, and same-as-IMAP mirrors credentials into SMTP fields.
  function setField(field: string, value: string | boolean) {
    setForm((current) => {
      const next = { ...current, [field]: value };
      if (field === "email" && typeof value === "string") {
        if (String(current.username || "").trim() === "" || current.username === current.email) {
          next.username = value;
        }
        if (String(current.label || "").trim() === "" || current.label === current.email) {
          next.label = value;
        }
      }
      if (field === "smtp_same_as_imap" && value === true) {
        next.smtp_host = next.host;
        next.smtp_username = next.username;
        next.smtp_password = next.password;
        next.smtp_use_tls = next.use_tls;
      }
      if (next.smtp_same_as_imap && ["host", "username", "password", "use_tls"].includes(field)) {
        next.smtp_host = String(next.host);
        next.smtp_username = String(next.username);
        next.smtp_password = String(next.password);
        next.smtp_use_tls = Boolean(next.use_tls);
      }
      return next;
    });
  }

  function setSMTPField(field: string, value: string | boolean) {
    setSMTPForm((current) => ({ ...current, [field]: value }));
  }

  function selectIMAP(next: Account) {
    setSelectedAccountID(next.id);
    setAccount(next);
    setForm(accountToForm(next));
    navigate(`/settings/account/imap/${next.id}`);
  }

  function newIMAPAccount() {
    setSelectedAccountID(null);
    setAccount(null);
    setForm(emptyAccountFormForUser(user));
    navigate("/settings/account/imap/new");
  }

  function selectSMTP(next: SMTPAccount) {
    setSelectedSMTPID(next.id);
    setSMTPForm(smtpToForm(next));
    navigate(`/settings/account/smtp/${next.id}`);
  }

  function newSMTPAccount() {
    setSelectedSMTPID(null);
    setSMTPForm(emptySMTPFormForUser(user));
    navigate("/settings/account/smtp/new");
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    try {
      const data = await api.saveIMAPAccount(csrf, {
        id: selectedAccountID || 0,
        email: form.email,
        label: form.label,
        host: form.host,
        port: Number(form.port),
        username: form.username,
        password: form.password,
        use_tls: form.use_tls,
        smtp_host: form.smtp_host,
        smtp_port: Number(form.smtp_port),
        smtp_username: form.smtp_username,
        smtp_password: form.smtp_password,
        smtp_use_tls: form.smtp_use_tls,
        smtp_same_as_imap: form.smtp_same_as_imap,
        mailbox: form.mailbox,
        sync_interval_minutes: Number(form.sync_interval_minutes)
      });
      addToast("IMAP server saved.");
      setSelectedAccountID(data.account.id);
      navigate(`/settings/account/imap/${data.account.id}`);
      await load();
      await refreshChrome();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function saveSMTP(event: FormEvent) {
    event.preventDefault();
    try {
      const data = await api.saveSMTPAccount(csrf, {
        id: selectedSMTPID || 0,
        label: smtpForm.label,
        host: smtpForm.host,
        port: Number(smtpForm.port),
        username: smtpForm.username,
        password: smtpForm.password,
        use_tls: smtpForm.use_tls
      });
      addToast("SMTP server saved.");
      setSelectedSMTPID(data.smtp_account.id);
      navigate(`/settings/account/smtp/${data.smtp_account.id}`);
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function saveIdentity(identity: MailIdentity) {
    try {
      const data = await api.saveMailIdentity(csrf, identity);
      setIdentities(data.identities);
      addToast(`${identity.email} identity saved.`);
      await refreshChrome();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  function updateIdentity(id: number, patch: Partial<MailIdentity>) {
    setIdentities((current) => current.map((identity) => identity.id === id ? { ...identity, ...patch } : identity));
  }

  async function saveProfile(event: FormEvent) {
    event.preventDefault();
    try {
      await api.saveProfile(csrf, profileForm);
      addToast("Display preferences saved.");
      await refreshChrome();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function syncNow() {
    try {
      await api.syncAccount(csrf);
      addToast("Sync started.");
      await refreshChrome();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  // Folder settings are saved as whole visibility/role/icon/sync-mode snapshots
  // so each small UI control can optimistically patch the same mailbox object.
  async function saveFolderSettings(folder: SyncFolder, patch: Partial<Mailbox>): Promise<boolean> {
    const next = { ...folder.mailbox, ...patch };
    try {
      await api.saveFolderSettings(csrf, folder.mailbox.id, {
        sync_mode: next.sync_mode,
        role: next.role || "",
        icon: next.icon || "folder",
        show_in_sidebar: next.show_in_sidebar,
        show_in_all_mail: next.show_in_all_mail,
        include_in_search: next.include_in_search
      });
      setFolders((current) => current.map((item) => item.mailbox.id === folder.mailbox.id ? { ...item, mailbox: next } : item));
      addToast(`${folder.mailbox.name} updated.`);
      await refreshChrome();
      return true;
    } catch (err) {
      addToast(messageFromError(err), "error");
      return false;
    }
  }

  function openFolderSettings(folder: SyncFolder) {
    setEditingFolderID(folder.mailbox.id);
    setFolderDraft(folderSettingsDraft(folder.mailbox));
  }

  function closeFolderSettings() {
    setEditingFolderID(null);
    setFolderDraft(null);
  }

  function updateFolderDraft(patch: Partial<FolderSettingsDraft>) {
    setFolderDraft((current) => current ? { ...current, ...patch } : current);
  }

  async function saveEditingFolder(event: FormEvent) {
    event.preventDefault();
    if (!editingFolderID || !folderDraft) return;
    const folder = folderMap.get(editingFolderID);
    if (!folder) {
      closeFolderSettings();
      return;
    }
    const saved = await saveFolderSettings(folder, folderDraft);
    if (saved) closeFolderSettings();
  }

  async function syncFolder(folder: SyncFolder) {
    try {
      await api.syncFolder(csrf, folder.mailbox.id);
      addToast(`${folder.mailbox.name} sync started.`);
      await refreshChrome();
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function rebuildFolderIndex(folder: SyncFolder) {
    if (!window.confirm(`Rebuild the search index for ${folder.mailbox.name}? This may take some time.`)) return;
    try {
      await api.rebuildFolderIndex(csrf, folder.mailbox.id);
      addToast(`${folder.mailbox.name} index rebuild started.`);
      await refreshChrome();
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  const selectedFolders = useMemo(
    () => selectedAccountID ? folders.filter((folder) => folder.mailbox.account_id === selectedAccountID) : folders,
    [folders, selectedAccountID]
  );
  const folderMap = useMemo(() => new Map(selectedFolders.map((folder) => [folder.mailbox.id, folder])), [selectedFolders]);
  const folderNodes = useMemo(() => folderTree(selectedFolders.map((folder) => folder.mailbox)), [selectedFolders]);
  const selectedAccountLabel = account ? (account.label || account.email) : route.kind === "imap" && route.isNew ? "New IMAP server" : "IMAP server";
  const selectedSMTP = smtpAccounts.find((item) => item.id === selectedSMTPID) || null;
  const identitiesBySMTP = useMemo(() => {
    const byServer = new Map<number, MailIdentity[]>();
    identities.forEach((identity) => {
      if (!identity.smtp_account_id) return;
      const existing = byServer.get(identity.smtp_account_id) || [];
      existing.push(identity);
      byServer.set(identity.smtp_account_id, existing);
    });
    return byServer;
  }, [identities]);
  const selectedSMTPLabel = selectedSMTP ? (selectedSMTP.label || selectedSMTP.host) : selectedSMTPID ? "SMTP server" : "New SMTP server";

  useEffect(() => {
    if (editingFolderID && !folderMap.has(editingFolderID)) {
      setEditingFolderID(null);
      setFolderDraft(null);
    }
  }, [editingFolderID, folderMap]);

  useEffect(() => {
    if (!editingFolderID) return;
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") closeFolderSettings();
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [editingFolderID]);

  function renderIMAPList() {
    return (
      <section className="panel account-list-panel">
        <div className="panel-headline">
          <div>
            <h2>IMAP servers</h2>
            <div className="muted">Mailboxes, sync rules, and indexed mail stay scoped to each signed-in user.</div>
          </div>
          <button className="secondary" type="button" onClick={newIMAPAccount}><Icon name="add" />Add IMAP</button>
        </div>
        <div className="server-list">
          {imapAccounts.length === 0 ? <div className="muted">No IMAP servers configured.</div> : null}
          {imapAccounts.map((item) => (
            <button className="server-row" type="button" key={item.id} onClick={() => selectIMAP(item)}>
              <span className="server-row-icon"><Icon name="inbox" /></span>
              <strong>{item.label || item.email}</strong>
              <small>{item.email} · {item.host}:{item.port}</small>
            </button>
          ))}
        </div>
      </section>
    );
  }

  function renderSMTPList() {
    return (
      <section className="panel account-list-panel">
        <div className="panel-headline">
          <div>
            <h2>SMTP servers</h2>
            <div className="muted">Identities can choose one of these servers for outgoing mail.</div>
          </div>
          <button className="secondary" type="button" onClick={newSMTPAccount}><Icon name="add" />Add SMTP</button>
        </div>
        <div className="server-list">
          {smtpAccounts.length === 0 ? <div className="muted">No SMTP servers configured.</div> : null}
          {smtpAccounts.map((item) => {
            const serverIdentities = identitiesBySMTP.get(item.id) || [];
            return (
              <button className="server-row server-row-with-identities" type="button" key={item.id} onClick={() => selectSMTP(item)}>
                <span className="server-row-icon"><Icon name="send" /></span>
                <strong>{item.label || item.host}</strong>
                <small>{item.username || "no username"} · {item.host}:{item.port}</small>
                <div className="server-identities">
                  <span className="server-identities-label">Outgoing identities</span>
                  {serverIdentities.length > 0 ? (
                    <span className="server-identity-list">
                      {serverIdentities.map((identity) => (
                        <span className="server-identity" key={identity.id}>
                          <strong>{identity.display_name || identity.email}</strong>
                          <small>{identity.email}</small>
                        </span>
                      ))}
                    </span>
                  ) : (
                    <span className="server-identity-empty">No identities assigned</span>
                  )}
                </div>
              </button>
            );
          })}
        </div>
      </section>
    );
  }

  // Folder settings render from the same tree model as the sidebar, with
  // maintenance actions and editable options separated from the read-only row.
  function renderFolderItems(nodes: FolderNode[], depth = 0): ReactNode[] {
    return nodes.flatMap((node) => {
      const folder = folderMap.get(node.mailbox.id);
      if (!folder) return [];
      const localPercent = percentValue(folder.mailbox.local_sync_percent ?? folder.mailbox.sync_percent);
      const searchPercent = typeof folder.mailbox.search_index_percent === "number"
        ? percentValue(folder.mailbox.search_index_percent)
        : null;
      const localMessageCount = folder.mailbox.local_message_count ?? folder.mailbox.message_count;
      const searchLabel = !folder.mailbox.include_in_search
        ? "Search off"
        : searchPercent === null
          ? "Search n/a"
          : `Search ${searchPercent}%`;
      const currentRole = folder.mailbox.role || "";
      const currentIcon = folder.mailbox.icon || "folder";
      const syncLabel = folderSyncModeLabel(folder.mailbox.sync_mode || "inherit");
      const roleLabel = folderRoleLabel(currentRole);
      const iconLabel = folderIconLabel(currentIcon);
      const visibilityLabel = folderVisibilityLabel(folder.mailbox);
      const canRebuildIndex = !folder.is_running && localMessageCount > 0;
      const rows: ReactNode[] = [
        <div className="folder-sync-row" key={folder.mailbox.id}>
          <div className="folder-sync-summary">
            <div className="folder-sync-name" style={depth > 0 ? { paddingLeft: `${depth * 18}px` } : undefined}>
              <Icon name={currentIcon} />
              <div>
                <strong>{node.label}</strong>
                <small>{folder.mailbox.account_email || "Mail account"} · {folder.mailbox.name}</small>
              </div>
            </div>
            <div className="folder-sync-status">
              <div className="folder-index-meters">
                <div className="sync-percent" aria-label="Local index progress">
                  <div><span style={{ width: `${localPercent}%` }} /></div>
                  <small>Local {localPercent}%</small>
                </div>
                <div className="sync-percent" aria-label="Search index progress">
                  <div><span style={{ width: `${searchPercent ?? 0}%` }} /></div>
                  <small>{searchLabel}</small>
                </div>
              </div>
              <div className="folder-sync-counts">
                <span><strong>{folder.mailbox.message_count.toLocaleString()}</strong> messages</span>
                <span><strong>{folder.mailbox.unread_count.toLocaleString()}</strong> unread</span>
              </div>
            </div>
            <div className="folder-sync-run">
              <span className="settings-field-label">Last run</span>
              <strong>{folder.is_running ? "Running" : folder.last_run ? folder.last_run.status : "Never"}</strong>
              <small>{folder.last_run ? displayDateTime(folder.last_run.updated_at, user) : "No sync activity"}</small>
            </div>
            <div className="folder-sync-settings-summary" aria-label={`${node.label} folder settings`}>
              <span title={`Sync mode: ${syncLabel}`}>Sync {syncLabel}</span>
              <span title={`Folder role: ${roleLabel}`}>{roleLabel}</span>
              <span title={`Sidebar icon: ${iconLabel}`}><Icon name={currentIcon} />{iconLabel}</span>
              <span title={`Visible in: ${visibilityLabel}`}>{visibilityLabel}</span>
            </div>
            <div className="folder-actions" aria-label={`${node.label} actions`}>
              <button
                className="folder-icon-action"
                type="button"
                disabled={!folder.can_sync_now}
                onClick={() => syncFolder(folder)}
                title={folder.can_sync_now ? `Sync ${node.label} now` : `${node.label} is already syncing`}
                aria-label={`Sync ${node.label} now`}
              >
                <Icon name="sync" />
              </button>
              <button
                className="folder-icon-action warning"
                type="button"
                disabled={!canRebuildIndex}
                onClick={() => rebuildFolderIndex(folder)}
                title={canRebuildIndex ? `Rebuild search index for ${node.label}. This may take some time.` : "No local messages are ready to index"}
                aria-label={`Rebuild search index for ${node.label}. This may take some time.`}
              >
                <Icon name="search" />
              </button>
              <button
                className="folder-icon-action"
                type="button"
                onClick={() => openFolderSettings(folder)}
                title={`Edit settings for ${node.label}`}
                aria-label={`Edit settings for ${node.label}`}
              >
                <Icon name="edit" />
              </button>
            </div>
          </div>
        </div>
      ];
      return rows.concat(renderFolderItems(node.children, depth + 1));
    });
  }

  function renderIdentitySettings() {
    return (
      <section className="panel identity-settings-panel">
        <h2>Identities</h2>
        <div className="muted">These are your Me contact email addresses. Each identity can choose an SMTP server and signature line here.</div>
        <div className="identity-list">
          {identities.length === 0 ? <div className="muted">No Me identities yet. Mark a contact as Me to add one.</div> : null}
          {identities.map((identity) => (
            <div className="identity-row" key={identity.id}>
              <div className="identity-main">
                <Field label="Display name" value={identity.display_name} onChange={(value) => updateIdentity(identity.id, { display_name: value })} />
                <div>
                  <label>Email</label>
                  <input value={identity.email} readOnly />
                </div>
                <div>
                  <label>SMTP server</label>
                  <select value={identity.smtp_account_id || 0} onChange={(event) => updateIdentity(identity.id, { smtp_account_id: Number(event.target.value) })}>
                    <option value={0}>Default</option>
                    {smtpAccounts.map((smtp) => <option value={smtp.id} key={smtp.id}>{smtp.label || smtp.host}</option>)}
                  </select>
                </div>
                <label className="identity-primary"><input type="checkbox" checked={identity.is_primary} onChange={(event) => updateIdentity(identity.id, { is_primary: event.target.checked })} /> Primary</label>
              </div>
              <div>
                <label>Signature line</label>
                <textarea value={identity.signature} onChange={(event) => updateIdentity(identity.id, { signature: event.target.value })} rows={3} />
              </div>
              <div className="actions"><button className="secondary" type="button" onClick={() => saveIdentity(identity)}>Save identity</button></div>
            </div>
          ))}
        </div>
      </section>
    );
  }

  // SMTP detail pages show identities under the selected outgoing server so the
  // user can edit assignment and signature without jumping back to contacts.
  function renderSMTPIdentitySettings() {
    const savedServerID = selectedSMTPID || 0;
    return (
      <section className="panel smtp-identity-settings identity-settings-panel">
        <div className="panel-headline">
          <div>
            <h2>Outgoing identities</h2>
            <div className="muted">Choose which Me identities send through this SMTP server, and adjust their signature lines here.</div>
          </div>
        </div>
        {!savedServerID ? <div className="notice subtle">Save this SMTP server before assigning identities to it.</div> : null}
        <div className="identity-list">
          {identities.length === 0 ? <div className="muted">No Me identities yet. Mark a contact as Me to add one.</div> : null}
          {identities.map((identity) => {
            const assignedHere = savedServerID > 0 && identity.smtp_account_id === savedServerID;
            return (
              <div className={`identity-row smtp-identity-row ${assignedHere ? "assigned" : ""}`} key={identity.id}>
                <div className="identity-main smtp-identity-main">
                  <Field label="Display name" value={identity.display_name} onChange={(value) => updateIdentity(identity.id, { display_name: value })} />
                  <div>
                    <label>Email</label>
                    <input value={identity.email} readOnly />
                  </div>
                  <div>
                    <label>Outgoing server</label>
                    <select value={identity.smtp_account_id || 0} disabled={!savedServerID} onChange={(event) => updateIdentity(identity.id, { smtp_account_id: Number(event.target.value) })}>
                      <option value={0}>Default</option>
                      {smtpAccounts.map((smtp) => <option value={smtp.id} key={smtp.id}>{smtp.label || smtp.host}</option>)}
                    </select>
                  </div>
                  <label className="identity-primary"><input type="checkbox" checked={identity.is_primary} onChange={(event) => updateIdentity(identity.id, { is_primary: event.target.checked })} /> Primary</label>
                </div>
                <label className="identity-assignment">
                  <input
                    type="checkbox"
                    checked={assignedHere}
                    disabled={!savedServerID}
                    onChange={(event) => updateIdentity(identity.id, { smtp_account_id: event.target.checked ? savedServerID : 0 })}
                  />
                  Use this SMTP server
                </label>
                <div>
                  <label>Signature line</label>
                  <textarea value={identity.signature} onChange={(event) => updateIdentity(identity.id, { signature: event.target.value })} rows={3} />
                </div>
                <div className="actions"><button className="secondary" type="button" onClick={() => saveIdentity(identity)}>Save identity</button></div>
              </div>
            );
          })}
        </div>
      </section>
    );
  }

  function renderDisplaySettings() {
    return (
      <form className="panel display-settings" onSubmit={saveProfile}>
        <h2>Display preferences</h2>
        <div className="settings-columns display-settings-grid">
          <section>
            <h3>Date localization</h3>
            <Field label="Locale" value={profileForm.date_locale} onChange={(value) => setProfileForm((current) => ({ ...current, date_locale: value }))} placeholder="Browser default, en-US, en-GB, ja-JP" />
          </section>
          <section>
            <h3>Date format</h3>
            <label>Date style</label>
            <select value={profileForm.date_format} onChange={(event) => setProfileForm((current) => ({ ...current, date_format: event.target.value }))}>
              <option value="mdy">MM/DD/YY</option>
              <option value="dmy">DD/MM/YY</option>
              <option value="ymd">YY/MM/DD</option>
              <option value="locale">Locale default</option>
            </select>
          </section>
          <section>
            <h3>Theme</h3>
            <label>Interface style</label>
            <select value={profileForm.theme} onChange={(event) => setProfileForm((current) => ({ ...current, theme: event.target.value }))}>
              <option value="classic">Classic</option>
              <option value="classic_dark">Classic Dark</option>
              <option value="matrix">Matrix</option>
            </select>
          </section>
          <section>
            <h3>Preview</h3>
            <div className="date-preview">
              <span>Recent mail</span>
              <strong>{displayTime(new Date().toISOString(), profileForm)}</strong>
              <span>Older mail</span>
              <strong>{displayTime(new Date(Date.now() - 400 * 24 * 60 * 60 * 1000).toISOString(), profileForm)}</strong>
            </div>
          </section>
        </div>
        <div className="actions"><button>Save display</button></div>
      </form>
    );
  }

  function renderStorageSettings() {
    return (
      <section className="panel">
        <h2>Storage</h2>
        {storageLoading ? <div className="muted">Calculating storage usage...</div> : null}
        {storageError ? <div className="error">{storageError}</div> : null}
        <div className="storage-grid">
          <Stat label="SQLite" value={formatBytes(storage.DatabaseBytes)} detail={String(storage.DatabasePath || "")} />
          <Stat label="Bleve" value={formatBytes(storage.IndexBytes)} detail={String(storage.IndexPath || "")} />
          <Stat label="Blobs" value={formatBytes(storage.BlobBytes)} detail={String(storage.BlobPath || "")} />
          <Stat label="Total" value={formatBytes(storage.TotalBytes)} detail={String(storage.Error || "")} />
        </div>
      </section>
    );
  }

  function renderLicenseSettings() {
    return (
      <section className="panel license-panel">
        <h2>License</h2>
        <p>
          MailMirror is free software licensed under the GNU Affero General Public License version 3 or later.
          You may run, study, share, and modify it under that license.
        </p>
        <p>
          The AGPL also applies when modified versions are provided over a network: users of that service must be
          offered access to the Corresponding Source for the version they are using.
        </p>
        <a href="https://www.gnu.org/licenses/agpl-3.0.html" target="_blank" rel="noreferrer">GNU AGPL v3 license text</a>
      </section>
    );
  }

  function renderFolderEditDialog() {
    const folder = editingFolderID ? folderMap.get(editingFolderID) : null;
    if (!folder || !folderDraft) return null;
    const currentIcon = folderDraft.icon || "folder";

    return (
      <div className="folder-dialog-backdrop" role="presentation" onMouseDown={(event) => {
        if (event.target === event.currentTarget) closeFolderSettings();
      }}>
        <form className="folder-edit-dialog" role="dialog" aria-modal="true" aria-labelledby="folder-edit-title" onSubmit={saveEditingFolder}>
          <header className="folder-edit-header">
            <div className="folder-edit-title">
              <span className="folder-edit-icon"><Icon name={currentIcon} /></span>
              <div>
                <h2 id="folder-edit-title">{folder.mailbox.name}</h2>
                <small>{folder.mailbox.account_email || "Mail account"}</small>
              </div>
            </div>
            <button className="folder-icon-action" type="button" onClick={closeFolderSettings} title="Close" aria-label="Close folder settings">
              <Icon name="close" />
            </button>
          </header>

          <div className="folder-edit-body">
            <section className="folder-edit-section">
              <span className="settings-field-label">Sync mode</span>
              <div className="folder-choice-buttons folder-choice-buttons-large">
                {syncModeChoices.map((choice) => (
                  <button
                    className={folderDraft.sync_mode === choice.value ? "active" : ""}
                    type="button"
                    key={choice.value}
                    onClick={() => updateFolderDraft({ sync_mode: choice.value })}
                  >
                    {choice.label}
                  </button>
                ))}
              </div>
            </section>

            <section className="folder-edit-section">
              <span className="settings-field-label">Folder role</span>
              <div className="folder-role-grid">
                {folderRoleChoices.map((choice) => (
                  <button
                    className={folderDraft.role === choice.value ? "active" : ""}
                    type="button"
                    key={choice.value || "normal"}
                    onClick={() => updateFolderDraft({ role: choice.value })}
                  >
                    {choice.label}
                  </button>
                ))}
              </div>
            </section>

            <section className="folder-edit-section folder-edit-section-wide">
              <span className="settings-field-label">Sidebar icon</span>
              <div className="folder-icon-grid">
                {folderIconChoices.map((choice) => (
                  <button
                    className={currentIcon === choice.value ? "active" : ""}
                    type="button"
                    key={choice.value}
                    onClick={() => updateFolderDraft({ icon: choice.value })}
                  >
                    <Icon name={choice.value} weight={currentIcon === choice.value ? "bold" : undefined} />
                    <span>{choice.label}</span>
                  </button>
                ))}
              </div>
            </section>

            <section className="folder-edit-section">
              <span className="settings-field-label">Visible in</span>
              <div className="folder-edit-visibility">
                {folderVisibilityChoices.map((choice) => (
                  <label key={choice.key}>
                    <input
                      type="checkbox"
                      checked={Boolean(folderDraft[choice.key])}
                      onChange={(event) => updateFolderDraft({ [choice.key]: event.target.checked } as Partial<FolderSettingsDraft>)}
                    />
                    <span>{choice.label}</span>
                  </label>
                ))}
              </div>
            </section>
          </div>

          <footer className="folder-edit-footer">
            <button className="secondary" type="button" onClick={closeFolderSettings}>Cancel</button>
            <button type="submit">Save settings</button>
          </footer>
        </form>
      </div>
    );
  }

  function renderFolderSettings() {
    return (
      <>
        <section className="panel folder-settings-panel">
          <h2>Folder sync</h2>
          <div className="muted">Folders under {selectedAccountLabel}</div>
          <div className="folder-sync-list">
            {folderNodes.length > 0 ? renderFolderItems(folderNodes) : <div className="muted">No folders discovered yet. Sync this account to discover folders.</div>}
          </div>
        </section>
        {renderFolderEditDialog()}
      </>
    );
  }

  function renderRecentRuns() {
    return (
      <section className="panel">
        <h2>Recent sync runs</h2>
        <table>
          <thead><tr><th>Status</th><th>Folder</th><th>Messages</th><th>Updated</th></tr></thead>
          <tbody>
            {runs.map((run) => (
              <tr key={run.id}>
                <td>{run.status}</td>
                <td>{run.current_mailbox}</td>
                <td>{run.messages_stored} indexed, {run.messages_skipped} skipped</td>
                <td>{displayDateTime(run.updated_at, user)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    );
  }

  if (route.kind === "imap") {
    return (
      <>
        <div className="content-head">
          <div className="list-head-main">
            <button className="icon-button" type="button" onClick={() => navigate("/settings/account")} title="Back to settings"><Icon name="arrow_back" /></button>
            <h1>{selectedAccountLabel}</h1>
          </div>
          <button type="button" onClick={syncNow}><Icon name="sync" />Sync all</button>
        </div>
        {loading ? <div className="panel muted">Loading settings...</div> : null}
        {notice ? <div className="notice">{notice}</div> : null}
        <form className="panel account-settings" onSubmit={save}>
          <h2>IMAP server</h2>
          <div className="settings-columns account-settings-grid">
            <section>
              <h3>Connection</h3>
              <Field label="Label" value={form.label} onChange={(value) => setField("label", value)} placeholder="Personal mail, Work archive" />
              <Field label="Email" value={form.email} onChange={(value) => setField("email", value)} type="email" />
              <Field label="Host" value={form.host} onChange={(value) => setField("host", value)} />
              <Field label="Port" value={form.port} onChange={(value) => setField("port", value)} type="number" />
              <Field label="Username" value={form.username} onChange={(value) => setField("username", value)} />
              <Field
                label="Password"
                value={form.password}
                onChange={(value) => setField("password", value)}
                type="password"
                placeholder={accountNeedsPassword ? "Required to restore IMAP access" : account ? "Leave blank to keep current password" : ""}
                required={accountNeedsPassword || !account}
              />
              <label><input type="checkbox" checked={form.use_tls} onChange={(event) => setField("use_tls", event.target.checked)} /> Use TLS</label>
            </section>
            <section>
              <h3>Sync scope</h3>
              <Field label="Folders" value={form.mailbox} onChange={(value) => setField("mailbox", value)} placeholder="*" />
              <Field label="Interval minutes" value={form.sync_interval_minutes} onChange={(value) => setField("sync_interval_minutes", value)} type="number" />
            </section>
          </div>
          <div className="actions"><button>Save IMAP server</button></div>
        </form>
        {route.isNew ? null : renderFolderSettings()}
        {route.isNew ? null : renderRecentRuns()}
      </>
    );
  }

  if (route.kind === "smtp") {
    return (
      <>
        <div className="content-head">
          <div className="list-head-main">
            <button className="icon-button" type="button" onClick={() => navigate("/settings/account")} title="Back to settings"><Icon name="arrow_back" /></button>
            <h1>{selectedSMTPLabel}</h1>
          </div>
        </div>
        {loading ? <div className="panel muted">Loading settings...</div> : null}
        {notice ? <div className="notice">{notice}</div> : null}
        <form className="panel smtp-settings-form" onSubmit={saveSMTP}>
          <h2>SMTP server</h2>
          <div className="settings-columns display-settings-grid">
            <section>
              <Field label="Label" value={smtpForm.label} onChange={(value) => setSMTPField("label", value)} />
              <Field label="Host" value={smtpForm.host} onChange={(value) => setSMTPField("host", value)} />
              <Field label="Port" value={smtpForm.port} onChange={(value) => setSMTPField("port", value)} type="number" />
            </section>
            <section>
              <Field label="Username" value={smtpForm.username} onChange={(value) => setSMTPField("username", value)} />
              <Field label="Password" value={smtpForm.password} onChange={(value) => setSMTPField("password", value)} type="password" placeholder={selectedSMTPID ? "Leave blank to keep current password" : ""} />
              <label><input type="checkbox" checked={smtpForm.use_tls} onChange={(event) => setSMTPField("use_tls", event.target.checked)} /> Use TLS / STARTTLS</label>
            </section>
          </div>
          <div className="actions"><button>Save SMTP server</button></div>
        </form>
        {renderSMTPIdentitySettings()}
      </>
    );
  }

  return (
    <>
      <div className="content-head">
        <h1>Settings</h1>
      </div>
      {loading ? <div className="panel muted">Loading settings...</div> : null}
      {notice ? <div className="notice">{notice}</div> : null}
      <div className="settings-server-index">
        {renderIMAPList()}
        {renderSMTPList()}
      </div>
      {renderDisplaySettings()}
      {renderStorageSettings()}
      {renderLicenseSettings()}
    </>
  );
}

/** AdminUsersView lets an admin create local users and refreshes chrome after user changes. */
export function AdminUsersView({
  csrf,
  refreshChrome,
  addToast
}: {
  csrf: string;
  refreshChrome: () => Promise<Bootstrap | null>;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [users, setUsers] = useState<User[]>([]);
  const [plugins, setPlugins] = useState<PluginSetting[]>([]);
  const [form, setForm] = useState({ email: "", name: "", password: "", is_admin: false });

  const load = useCallback(async () => {
    const userData = await api.users();
    setUsers(userData.users);
  }, []);

  useEffect(() => {
    void load().catch((err) => addToast(messageFromError(err), "error"));
  }, [addToast, load]);

  async function create(event: FormEvent) {
    event.preventDefault();
    try {
      await api.createUser(csrf, form);
      setForm({ email: "", name: "", password: "", is_admin: false });
      addToast("User created.");
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  const remoteImagePlugin = plugins.find((plugin) => plugin.id === pluginIDs.remoteImageBlocklist);

  return (
    <>
      <div className="content-head"><h1>Admin</h1></div>
      <form className="panel" onSubmit={create}>
        <h2>Create user</h2>
        <div className="grid">
          <Field label="Email" value={form.email} onChange={(value) => setForm((current) => ({ ...current, email: value }))} type="email" />
          <Field label="Name" value={form.name} onChange={(value) => setForm((current) => ({ ...current, name: value }))} />
          <Field label="Password" value={form.password} onChange={(value) => setForm((current) => ({ ...current, password: value }))} type="password" />
        </div>
        <div className="checks">
          <label>
            <input
              type="checkbox"
              checked={form.is_admin}
              onChange={(event) => setForm((current) => ({ ...current, is_admin: event.target.checked }))}
            /> Admin
          </label>
        </div>
        <button>Create user</button>
      </form>
      <section className="panel">
        <h2>Existing users</h2>
        <table>
          <thead>
            <tr><th>Email</th><th>Name</th><th>Role</th></tr>
          </thead>
          <tbody>
            {users.map((user) => (
              <tr key={user.id}>
                <td>{user.email}</td>
                <td>{user.name}</td>
                <td>{user.is_admin ? "Admin" : "User"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
      <PluginTogglePanel
        csrf={csrf}
        addToast={addToast}
        onPluginsChanged={setPlugins}
        onPluginSaved={refreshChrome}
      />
      <AdminRemoteImageBlocklist csrf={csrf} addToast={addToast} enabled={Boolean(remoteImagePlugin?.enabled)} />
    </>
  );
}

/** SyncRunView shows a single sync run's latest progress/status details. */
export function SyncRunView({
  location,
  navigate,
  datePrefs
}: {
  location: LocationState;
  navigate: (url: string) => void;
  datePrefs: DatePrefs;
}) {
  const id = location.path.split("/").pop() || "";
  const [run, setRun] = useState<SyncRun | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    api
      .syncRun(id)
      .then((data) => {
        if (!cancelled) setRun(data.sync_run);
      })
      .catch((err) => {
        if (!cancelled) setError(messageFromError(err));
      });
    return () => {
      cancelled = true;
    };
  }, [id]);

  return (
    <>
      <div className="content-head">
        <h1>Sync run</h1>
        <button className="secondary" type="button" onClick={() => navigate("/settings/account")}>Back to settings</button>
      </div>
      {error ? <div className="error">{error}</div> : null}
      {run ? (
        <section className="panel">
          <dl className="detail-list">
            <dt>Status</dt><dd>{run.status}</dd>
            <dt>Started</dt><dd>{displayDateTime(run.started_at, datePrefs)}</dd>
            <dt>Updated</dt><dd>{displayDateTime(run.updated_at, datePrefs)}</dd>
            <dt>Finished</dt><dd>{run.finished_at ? displayDateTime(run.finished_at, datePrefs) : "-"}</dd>
            <dt>Folder</dt><dd>{run.current_mailbox}</dd>
            <dt>UID</dt><dd>{run.current_uid}</dd>
            <dt>Messages</dt><dd>{run.messages_stored} indexed, {run.messages_skipped} skipped, {run.messages_seen} seen</dd>
            <dt>Error</dt><dd>{run.error || "-"}</dd>
          </dl>
        </section>
      ) : (
        <div className="panel muted">Loading sync run...</div>
      )}
    </>
  );
}
