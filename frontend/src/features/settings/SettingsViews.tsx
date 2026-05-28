// File overview: Settings surface for profile preferences, IMAP servers, SMTP servers, outgoing
// identities, folder sync/indexing controls, storage usage, and admin plugin panels.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent, ReactNode } from "react";
import { api } from "../../api";
import type { DatePrefs, LocationState, Toast } from "../../appTypes";
import type { Account, AccountPurgeEstimate, Bootstrap, IdentityPGPPrivateKey, MailIdentity, PluginSetting, Mailbox, SMTPAccount, StorageStats, SyncFolder, SyncRun, ThemeDefinition, User } from "../../types";
import { Icon } from "../../components/Icon";
import { Field, Stat } from "../../components/common";
import { emptyAccountForm, accountToForm } from "../../lib/accountForm";
import { messageFromError } from "../../lib/errors";
import { displayDateTime, displayTime, formatBytes } from "../../lib/format";
import { folderParentNames, folderTree, type FolderNode } from "../../lib/folders";
import { effectiveMailboxSyncMode, mergeSyncRuns } from "../../lib/sync";
import { pluginIDs } from "../../plugins/registry";
import type { ClientSidePGPPlugin } from "../../../../plugins/client_side_pgp/frontend/types";
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

type StorageIndexBreakdownView = {
  FileCount?: unknown;
  ZapCount?: unknown;
  ZapBytes?: unknown;
  LargestZapPath?: unknown;
  LargestZapBytes?: unknown;
  RootBytes?: unknown;
  OtherBytes?: unknown;
};

type PGPPrivateKeyStorage = "browser" | "server";

function storageIndexBreakdown(stats: StorageStats): StorageIndexBreakdownView {
  const value = stats.IndexBreakdown;
  return value && typeof value === "object" ? value as StorageIndexBreakdownView : {};
}

function formatStatCount(value: unknown): string {
  const count = typeof value === "number" ? value : Number(value || 0);
  return Number.isFinite(count) ? count.toLocaleString() : "0";
}

function statDetail(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function imapDeletePrompt(estimate: AccountPurgeEstimate): string {
  return [
    `Remove the IMAP server "${estimate.account_name}" from rolltop?`,
    "",
    "This does not delete any messages from the remote IMAP server.",
    "rolltop will hide the server now, then purge local SQLite rows, message bodies, and full-text index documents in the background.",
    "",
    `Folders: ${formatStatCount(estimate.mailbox_count)}`,
    `Message headers: ${formatStatCount(estimate.message_count)}`,
    `Message bodies: ${formatStatCount(estimate.blob_count)} blobs, ${formatBytes(estimate.blob_bytes)}`,
    `Full text index: ${formatStatCount(estimate.search_index_count)} documents`,
    "",
    `Type ${estimate.account_name} to confirm.`
  ].join("\n");
}

function smtpDeleteConfirmationName(account: SMTPAccount): string {
  return account.label.trim() || account.host.trim() || "SMTP server";
}

function hasStorageIndexBreakdown(value: StorageIndexBreakdownView): boolean {
  return Boolean(value.FileCount || value.ZapCount || value.ZapBytes || value.LargestZapBytes || value.RootBytes || value.OtherBytes);
}

function searchPresetDefaults(preset: string) {
  switch (preset) {
    case "strict":
      return {
        search_preset: "strict",
        search_recency_bias: "light",
        search_fuzzy: "off",
        search_sender_boost: true,
        search_sender_history: "light",
        search_contact_boost: "light",
        search_attachment_weight: "normal",
        search_compact_splitting: false
      };
    case "forgiving":
      return {
        search_preset: "forgiving",
        search_recency_bias: "normal",
        search_fuzzy: "forgiving",
        search_sender_boost: true,
        search_sender_history: "strong",
        search_contact_boost: "strong",
        search_attachment_weight: "strong",
        search_compact_splitting: true
      };
    default:
      return {
        search_preset: "balanced",
        search_recency_bias: "normal",
        search_fuzzy: "balanced",
        search_sender_boost: true,
        search_sender_history: "normal",
        search_contact_boost: "normal",
        search_attachment_weight: "normal",
        search_compact_splitting: true
      };
  }
}

function normalizeIdentityKey(value: string) {
  return value.trim().toLowerCase();
}

function identityAccountMatches(identity: MailIdentity, account: Account | undefined, smtp: SMTPAccount | undefined) {
  if (!account) return false;
  const keys = new Set([identity.email, smtp?.username || ""].map(normalizeIdentityKey).filter(Boolean));
  if (keys.size === 0) return false;
  return [account.email, account.username, account.smtp_username].some((value) => keys.has(normalizeIdentityKey(value || "")));
}

function imapAccountLabel(account: Account) {
  return account.label || account.email || account.host || "IMAP server";
}

function smtpAccountLabel(account: SMTPAccount) {
  return account.label || account.username || account.host || "SMTP server";
}

function mailboxChoiceLabel(mailbox: Mailbox, accounts: Account[]) {
  const account = accounts.find((item) => item.id === mailbox.account_id);
  const accountLabel = account ? imapAccountLabel(account) : mailbox.account_label || mailbox.account_email || "IMAP";
  return `${accountLabel} / ${mailbox.name}`;
}

function firstMailboxIDForAccountRole(mailboxes: Mailbox[], accountID: number, role: string) {
  return mailboxes.find((mailbox) => mailbox.account_id === accountID && mailbox.role === role)?.id || 0;
}

function identityMailboxChoices(identity: MailIdentity, role: string, mailboxes: Mailbox[], accounts: Account[], smtpAccounts: SMTPAccount[]) {
  const roleMatches = mailboxes.filter((mailbox) => mailbox.role === role);
  if (identity.imap_account_id) {
    return roleMatches.filter((mailbox) => mailbox.account_id === identity.imap_account_id);
  }
  const smtp = smtpAccounts.find((item) => item.id === identity.smtp_account_id);
  const matchingAccount = roleMatches.filter((mailbox) => identityAccountMatches(identity, accounts.find((account) => account.id === mailbox.account_id), smtp));
  return matchingAccount.length > 0 ? matchingAccount : roleMatches;
}

function RichSignatureEditor({ value, onChange }: { value: string; onChange: (value: string) => void }) {
  const editorRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const editor = editorRef.current;
    if (!editor || document.activeElement === editor || editor.innerHTML === value) return;
    editor.innerHTML = value || "";
  }, [value]);

  function format(command: string, commandValue?: string) {
    const editor = editorRef.current;
    if (!editor) return;
    editor.focus();
    document.execCommand(command, false, commandValue);
    onChange(editor.innerHTML);
  }

  function addLink() {
    const href = window.prompt("Link URL");
    if (!href) return;
    format("createLink", href);
  }

  return (
    <div className="rich-signature-editor">
      <div className="rich-signature-toolbar" aria-label="Signature formatting">
        <button type="button" className="ghost" onClick={() => format("bold")}><strong>B</strong></button>
        <button type="button" className="ghost" onClick={() => format("italic")}><em>I</em></button>
        <button type="button" className="ghost" onClick={() => format("insertUnorderedList")}><Icon name="format_list_bulleted" /></button>
        <button type="button" className="ghost" onClick={addLink}><Icon name="link" /></button>
      </div>
      <div
        ref={editorRef}
        className="rich-signature-input"
        contentEditable
        role="textbox"
        aria-multiline="true"
        suppressContentEditableWarning
        onInput={(event) => onChange(event.currentTarget.innerHTML)}
        onBlur={(event) => onChange(event.currentTarget.innerHTML)}
      />
    </div>
  );
}

function IdentityMailboxFields({
  identity,
  accounts,
  smtpAccounts,
  mailboxes,
  updateIdentity
}: {
  identity: MailIdentity;
  accounts: Account[];
  smtpAccounts: SMTPAccount[];
  mailboxes: Mailbox[];
  updateIdentity: (id: number, patch: Partial<MailIdentity>) => void;
}) {
  const sentChoices = identityMailboxChoices(identity, "sent", mailboxes, accounts, smtpAccounts);
  const draftsChoices = identityMailboxChoices(identity, "drafts", mailboxes, accounts, smtpAccounts);

  function changeIMAPAccount(accountID: number) {
    updateIdentity(identity.id, {
      imap_account_id: accountID,
      sent_mailbox_id: accountID ? firstMailboxIDForAccountRole(mailboxes, accountID, "sent") : 0,
      drafts_mailbox_id: accountID ? firstMailboxIDForAccountRole(mailboxes, accountID, "drafts") : 0
    });
  }

  return (
    <div className="identity-mailbox-fields">
      <div>
        <label>IMAP server</label>
        <select value={identity.imap_account_id || 0} onChange={(event) => changeIMAPAccount(Number(event.target.value))}>
          <option value={0}>Automatic</option>
          {accounts.map((account) => <option key={account.id} value={account.id}>{imapAccountLabel(account)}</option>)}
        </select>
      </div>
      <div>
        <label>Sent Mail folder</label>
        <select value={identity.sent_mailbox_id || 0} onChange={(event) => updateIdentity(identity.id, { sent_mailbox_id: Number(event.target.value) })}>
          <option value={0}>Automatic</option>
          {sentChoices.map((mailbox) => <option key={mailbox.id} value={mailbox.id}>{mailboxChoiceLabel(mailbox, accounts)}</option>)}
        </select>
      </div>
      <div>
        <label>Drafts folder</label>
        <select value={identity.drafts_mailbox_id || 0} onChange={(event) => updateIdentity(identity.id, { drafts_mailbox_id: Number(event.target.value) })}>
          <option value={0}>Automatic</option>
          {draftsChoices.map((mailbox) => <option key={mailbox.id} value={mailbox.id}>{mailboxChoiceLabel(mailbox, accounts)}</option>)}
        </select>
      </div>
    </div>
  );
}

function fallbackThemes(): ThemeDefinition[] {
  return [
    { id: "classic", name: "Classic" },
    { id: "classic_dark", name: "Classic Dark" }
  ];
}

function profileFormForUser(user: User, availableThemes: ThemeDefinition[] = fallbackThemes()) {
  const defaults = searchPresetDefaults(user.search_preset || "balanced");
  const themeIDs = new Set(availableThemes.map((theme) => theme.id));
  return {
    backup_email: user.backup_email || "",
    date_locale: user.date_locale || "",
    date_format: user.date_format || "mdy",
    theme: themeIDs.has(user.theme) ? user.theme : "classic",
    search_preset: ["strict", "balanced", "forgiving"].includes(user.search_preset) ? user.search_preset : defaults.search_preset,
    search_recency_bias: ["none", "light", "normal", "strong"].includes(user.search_recency_bias) ? user.search_recency_bias : defaults.search_recency_bias,
    search_fuzzy: ["off", "balanced", "forgiving"].includes(user.search_fuzzy) ? user.search_fuzzy : defaults.search_fuzzy,
    search_sender_boost: user.search_sender_boost !== false,
    search_sender_history: ["none", "light", "normal", "strong"].includes(user.search_sender_history) ? user.search_sender_history : (user.search_sender_boost === false ? "none" : defaults.search_sender_history),
    search_contact_boost: ["none", "light", "normal", "strong"].includes(user.search_contact_boost) ? user.search_contact_boost : defaults.search_contact_boost,
    search_attachment_weight: ["off", "light", "normal", "strong"].includes(user.search_attachment_weight) ? user.search_attachment_weight : defaults.search_attachment_weight,
    search_compact_splitting: user.search_compact_splitting !== false
  };
}

type SearchChoice = { value: string; label: string };

const searchPresetChoices: SearchChoice[] = [
  { value: "strict", label: "Strict" },
  { value: "balanced", label: "Balanced" },
  { value: "forgiving", label: "Forgiving" }
];

const fuzzyChoices: SearchChoice[] = [
  { value: "off", label: "Off" },
  { value: "balanced", label: "Balanced" },
  { value: "forgiving", label: "Forgiving" }
];

const recencyChoices: SearchChoice[] = [
  { value: "none", label: "None" },
  { value: "light", label: "Light" },
  { value: "normal", label: "Normal" },
  { value: "strong", label: "Strong" }
];

const attachmentWeightChoices: SearchChoice[] = [
  { value: "off", label: "Exclude" },
  { value: "light", label: "Light" },
  { value: "normal", label: "Normal" },
  { value: "strong", label: "Strong" }
];

const boostWeightChoices: SearchChoice[] = [
  { value: "none", label: "None" },
  { value: "light", label: "Light" },
  { value: "normal", label: "Normal" },
  { value: "strong", label: "Strong" }
];

function SearchSliderRow({ title, value, choices, description, onChange }: {
  title: string;
  value: string;
  choices: SearchChoice[];
  description: string;
  onChange: (value: string) => void;
}) {
  const index = Math.max(0, choices.findIndex((choice) => choice.value === value));
  return (
    <div className="search-tuning-row">
      <div className="search-tuning-copy">
        <strong>{title}</strong>
        <small>{description}</small>
      </div>
      <div className="search-slider-control">
        <input
          type="range"
          min={0}
          max={Math.max(0, choices.length - 1)}
          step={1}
          value={index}
          aria-label={title}
          onChange={(event) => onChange(choices[Number(event.target.value)]?.value || choices[0].value)}
        />
        <div className="search-slider-labels">
          {choices.map((choice) => <span className={choice.value === value ? "active" : ""} key={choice.value}>{choice.label}</span>)}
        </div>
      </div>
    </div>
  );
}

function storageEmailDetail(value: unknown): string {
  return `${formatStatCount(value)} emails`;
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

function blankMailIdentity(user: User, identities: MailIdentity[] = []): MailIdentity {
  return {
    id: 0,
    contact_id: 0,
    contact_email_id: 0,
    smtp_account_id: 0,
    imap_account_id: 0,
    sent_mailbox_id: 0,
    drafts_mailbox_id: 0,
    email: "",
    display_name: user.name || "",
    signature: "",
    autocrypt_enabled: true,
    is_primary: identities.length === 0
  };
}

function cloneMailIdentity(identity: MailIdentity): MailIdentity {
  return { ...identity, autocrypt_enabled: identity.autocrypt_enabled ?? true };
}

type SettingsRoute = {
  kind: "main" | "imap" | "smtp" | "identities";
  id: number | null;
  isNew: boolean;
};

type FolderSettingsDraft = Pick<Mailbox, "sync_mode" | "role" | "icon" | "show_in_sidebar" | "show_in_all_mail" | "include_in_search">;

// Settings uses real URL subpages for IMAP/SMTP editing so refresh/back keeps
// the selected server instead of returning to the settings index.
function settingsRouteFromPath(path: string): SettingsRoute {
  if (path === "/settings/account/identities") return { kind: "identities", id: null, isNew: false };
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
  { value: "auto", label: "Auto", description: "Sync automatically when rolltop refreshes this account." },
  { value: "manual", label: "Manual", description: "Keep the folder available, but sync only when requested." },
  { value: "never", label: "Never", description: "Do not sync this folder." },
  { value: "inherit", label: "Inherit", description: "Use the nearest parent folder sync mode." }
];

const folderRoleChoices = [
  { value: "", label: "Normal" },
  { value: "inbox", label: "Inbox" },
  { value: "sent", label: "Sent" },
  { value: "drafts", label: "Drafts" },
  { value: "trash", label: "Trash" }
];

const uniqueFolderRoles = new Set(folderRoleChoices.map((choice) => choice.value).filter(Boolean));

const folderIconChoices = [
  { value: "folder", label: "Folder" },
  { value: "inbox", label: "Inbox" },
  { value: "archive", label: "Archive" },
  { value: "send", label: "Sent" },
  { value: "draft", label: "Draft" },
  { value: "delete", label: "Trash" },
  { value: "label", label: "Label" },
  { value: "shopping_bag", label: "Purchases" },
  { value: "report", label: "Spam" },
  { value: "star", label: "Star" },
  { value: "bookmark", label: "Bookmark" },
  { value: "flame", label: "Flame" },
  { value: "clock", label: "Clock" },
  { value: "receipt", label: "Receipts" },
  { value: "credit_card", label: "Cards" },
  { value: "briefcase", label: "Work" },
  { value: "bank", label: "Finance" },
  { value: "newspaper", label: "News" },
  { value: "calendar", label: "Calendar" },
  { value: "camera", label: "Photos" },
  { value: "home", label: "Home" },
  { value: "building", label: "Business" },
  { value: "school", label: "School" },
  { value: "travel", label: "Travel" },
  { value: "heart", label: "Personal" },
  { value: "file_text", label: "Docs" },
  { value: "chart", label: "Reports" }
];

const folderVisibilityChoices = [
  { key: "show_in_sidebar", label: "Sidebar" },
  { key: "show_in_all_mail", label: "All Mail" },
  { key: "include_in_search", label: "Search" }
] as const;

const dateLocaleChoices = [
  { value: "", label: "Browser default" },
  { value: "en-US", label: "English (United States)" },
  { value: "en-GB", label: "English (United Kingdom)" },
  { value: "en-CA", label: "English (Canada)" },
  { value: "en-AU", label: "English (Australia)" },
  { value: "fr-FR", label: "French (France)" },
  { value: "fr-CA", label: "French (Canada)" },
  { value: "de-DE", label: "German (Germany)" },
  { value: "es-ES", label: "Spanish (Spain)" },
  { value: "es-MX", label: "Spanish (Mexico)" },
  { value: "it-IT", label: "Italian (Italy)" },
  { value: "pt-BR", label: "Portuguese (Brazil)" },
  { value: "pt-PT", label: "Portuguese (Portugal)" },
  { value: "nl-NL", label: "Dutch (Netherlands)" },
  { value: "sv-SE", label: "Swedish (Sweden)" },
  { value: "da-DK", label: "Danish (Denmark)" },
  { value: "fi-FI", label: "Finnish (Finland)" },
  { value: "nb-NO", label: "Norwegian Bokmal (Norway)" },
  { value: "ja-JP", label: "Japanese (Japan)" },
  { value: "ko-KR", label: "Korean (South Korea)" },
  { value: "zh-CN", label: "Chinese Simplified (China)" },
  { value: "zh-TW", label: "Chinese Traditional (Taiwan)" }
];

function folderCanInherit(mailbox: Mailbox) {
  return folderParentNames(mailbox.name).length > 0;
}

function folderSyncModeChoices(mailbox: Mailbox) {
  return syncModeChoices.filter((choice) => choice.value !== "inherit" || folderCanInherit(mailbox));
}

function folderSettingsDraft(mailbox: Mailbox): FolderSettingsDraft {
  const syncMode = mailbox.sync_mode || (folderCanInherit(mailbox) ? "inherit" : "auto");
  return {
    sync_mode: !folderCanInherit(mailbox) && syncMode === "inherit" ? "auto" : syncMode,
    role: mailbox.role || "",
    icon: mailbox.icon || "folder",
    show_in_sidebar: mailbox.show_in_sidebar,
    show_in_all_mail: mailbox.show_in_all_mail,
    include_in_search: mailbox.include_in_search
  };
}

function folderSyncModeLabel(value: string) {
  return syncModeChoices.find((choice) => choice.value === value)?.label || "Auto";
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
  availableThemes,
  location,
  navigate,
  refreshChrome,
  pgpEnabled,
  pgpPlugin,
  addToast
}: {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  activeSyncRuns: SyncRun[];
  availableThemes: ThemeDefinition[];
  location: LocationState;
  navigate: (url: string) => void;
  refreshChrome: () => Promise<Bootstrap | null>;
  pgpEnabled: boolean;
  pgpPlugin?: ClientSidePGPPlugin;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const route = settingsRouteFromPath(location.path);
  const [account, setAccount] = useState<Account | null>(null);
  const [imapAccounts, setIMAPAccounts] = useState<Account[]>([]);
  const [smtpAccounts, setSMTPAccounts] = useState<SMTPAccount[]>([]);
  const [identities, setIdentities] = useState<MailIdentity[]>([]);
  const [selectedIdentityID, setSelectedIdentityID] = useState<number | "new" | null>(null);
  const [identityDraft, setIdentityDraft] = useState<MailIdentity>(() => blankMailIdentity(user));
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
  const [profileForm, setProfileForm] = useState(() => profileFormForUser(user, availableThemes));
  const [editingFolderID, setEditingFolderID] = useState<number | null>(null);
  const [folderDraft, setFolderDraft] = useState<FolderSettingsDraft | null>(null);
  const [deletingAccountID, setDeletingAccountID] = useState<number | null>(null);
  const [deletingSMTPID, setDeletingSMTPID] = useState<number | null>(null);
  const [savingIdentity, setSavingIdentity] = useState(false);
  const [pgpKeys, setPGPKeys] = useState<IdentityPGPPrivateKey[]>([]);
  const [pgpPrivateKeyStorage, setPGPPrivateKeyStorage] = useState<PGPPrivateKeyStorage>("browser");
  const [pgpPrivateImportOpen, setPGPPrivateImportOpen] = useState(false);
  const [pgpGenerateOpen, setPGPGenerateOpen] = useState(false);
  const [pgpSaving, setPGPSaving] = useState(false);
  const [pgpGenerating, setPGPGenerating] = useState(false);
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

  const loadPGPKeys = useCallback(async () => {
    if (!pgpEnabled || !pgpPlugin) {
      setPGPKeys([]);
      return;
    }
    const data = await pgpPlugin.privateKeys();
    setPGPKeys(data.keys || []);
  }, [pgpEnabled, pgpPlugin]);

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
      const nextIdentities = data.identities || [];
      setIMAPAccounts(accounts);
      setSMTPAccounts(smtp);
      setIdentities(nextIdentities);
      if (selectedIdentityID !== "new") {
        const nextIdentity = selectedIdentityID
          ? nextIdentities.find((identity) => identity.id === selectedIdentityID) || null
          : nextIdentities[0] || null;
        if (nextIdentity) {
          setSelectedIdentityID(nextIdentity.id);
          setIdentityDraft(cloneMailIdentity(nextIdentity));
        } else {
          setSelectedIdentityID("new");
          setIdentityDraft(blankMailIdentity(user, nextIdentities));
        }
      }
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
  }, [loadStorage, location.path, selectedAccountID, selectedSMTPID, selectedIdentityID, user]);

  useEffect(() => {
    void load().catch((err) => addToast(messageFromError(err), "error"));
  }, [addToast, load]);

  useEffect(() => {
    void loadPGPKeys().catch((err) => addToast(messageFromError(err), "error"));
  }, [addToast, loadPGPKeys]);

  useEffect(() => {
    setProfileForm(profileFormForUser(user, availableThemes));
  }, [
    availableThemes,
    user.date_locale,
    user.date_format,
    user.theme,
    user.search_preset,
    user.search_recency_bias,
    user.search_fuzzy,
    user.search_sender_boost,
    user.search_attachment_weight,
    user.search_compact_splitting
  ]);

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

  function chooseIdentity(identity: MailIdentity) {
    setSelectedIdentityID(identity.id);
    setIdentityDraft(cloneMailIdentity(identity));
  }

  function newIdentity() {
    setSelectedIdentityID("new");
    setIdentityDraft(blankMailIdentity(user, identities));
    navigate("/settings/account/identities");
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

  async function deleteSelectedSMTPAccount() {
    if (!selectedSMTP || deletingSMTPID) return;
    const expected = smtpDeleteConfirmationName(selectedSMTP);
    setDeletingSMTPID(selectedSMTP.id);
    try {
      const typed = window.prompt([
        `Remove the SMTP server "${expected}" from rolltop?`,
        "",
        "This does not delete messages or Me identities.",
        "Identities using this SMTP server will be set back to Default.",
        "",
        `Type ${expected} to confirm.`
      ].join("\n"));
      if (typed === null) return;
      if (typed.trim() !== expected) {
        addToast("SMTP server name did not match. Nothing was removed.", "error");
        return;
      }
      await api.deleteSMTPAccount(csrf, selectedSMTP.id);
      addToast(`Removed ${expected}. Identities using it now use Default.`);
      setSMTPAccounts((current) => current.filter((item) => item.id !== selectedSMTP.id));
      setIdentities((current) => current.map((identity) => identity.smtp_account_id === selectedSMTP.id ? { ...identity, smtp_account_id: 0 } : identity));
      setIdentityDraft((current) => current.smtp_account_id === selectedSMTP.id ? { ...current, smtp_account_id: 0 } : current);
      setSelectedSMTPID(null);
      setSMTPForm(emptySMTPFormForUser(user));
      navigate("/settings/account");
      await refreshChrome();
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setDeletingSMTPID(null);
    }
  }


  async function saveIdentity(identity: MailIdentity) {
    setSavingIdentity(true);
    try {
      const data = await api.saveMailIdentity(csrf, identity);
      setIdentities(data.identities);
      setSelectedIdentityID(data.identity.id);
      setIdentityDraft(cloneMailIdentity(data.identity));
      addToast(`${data.identity.email} identity saved.`);
      await refreshChrome();
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setSavingIdentity(false);
    }
  }

  function updateIdentityDraft(patch: Partial<MailIdentity>) {
    setIdentityDraft((current) => ({ ...current, ...patch }));
  }

  function markIdentityAutocryptEnabled(identityID: number) {
    setIdentities((current) => current.map((identity) => identity.id === identityID ? { ...identity, autocrypt_enabled: true } : identity));
    setIdentityDraft((current) => current.id === identityID ? { ...current, autocrypt_enabled: true } : current);
  }

  function identityPGPPassphraseValues() {
    return [user.email, user.name, identityDraft.email, identityDraft.display_name, identityDraft.email.split("@")[0] || "", identityDraft.email.split("@")[1] || ""];
  }

  async function importIdentityPGPKey(armored: string) {
    if (!identityDraft.id) {
      throw new Error("Save the identity before adding a PGP key.");
    }
    setPGPSaving(true);
    try {
      if (!pgpPlugin) throw new Error("PGP plugin is still loading. Try again in a moment.");
      const parsed = await pgpPlugin.privateKeyRecordFromArmoredSource(armored);
      const matchingIdentity = identities.find((identity) => pgpPlugin.pgpUserIDsMatchEmail(parsed.user_ids, identity.email));
      if (!matchingIdentity) {
        const keyEmails = pgpPlugin.pgpUserIDEmails(parsed.user_ids);
        const detail = keyEmails.length > 0 ? ` It lists ${keyEmails.join(", ")}.` : "";
        throw new Error(`This private key is not for one of your profile email addresses.${detail}`);
      }
      if (matchingIdentity.id !== identityDraft.id) {
        throw new Error(`This private key is for ${matchingIdentity.email}. Select that identity before importing it.`);
      }
      const parsedFingerprint = normalizedPGPIdentifier(parsed.fingerprint);
      const parsedKeyID = normalizedPGPIdentifier(parsed.key_id);
      const duplicate = pgpKeys.find((key) =>
        (parsedFingerprint && normalizedPGPIdentifier(key.fingerprint) === parsedFingerprint) ||
        (!parsedFingerprint && parsedKeyID && normalizedPGPIdentifier(key.key_id) === parsedKeyID)
      );
      if (duplicate) {
        const duplicateIdentity = identities.find((identity) => identity.id === duplicate.identity_id);
        throw new Error(`This private key is already saved${duplicateIdentity?.email ? ` for ${duplicateIdentity.email}` : ""}.`);
      }
      const saved = await pgpPlugin.savePrivateKey(csrf, {
        ...parsed,
        identity_id: identityDraft.id,
        label: identityDraft.email || firstPGPUserID(parsed.user_ids) || parsed.label || "PGP key",
        private_key_armored: pgpPrivateKeyStorage === "server" ? parsed.private_key_armored : "",
        private_key_storage: pgpPrivateKeyStorage,
        is_active_signing: true,
        is_active_encryption: true,
        is_decrypt_only: false
      });
      if (pgpPrivateKeyStorage === "browser") {
        try {
          await pgpPlugin.saveBrowserPGPPrivateKey(user.id, saved.key, parsed.private_key_armored || "");
        } catch (err) {
          if (saved.key.id) {
            await pgpPlugin.deletePrivateKey(csrf, saved.key.id).catch(() => undefined);
          }
          throw err;
        }
      }
      const firstIdentityKey = !pgpKeys.some((key) => key.identity_id === identityDraft.id);
      setPGPKeys((current) => [...current.filter((key) => key.id !== saved.key.id), saved.key]);
      if (firstIdentityKey && saved.key.is_active_encryption && !saved.key.is_decrypt_only) {
        markIdentityAutocryptEnabled(identityDraft.id);
      }
      setPGPPrivateImportOpen(false);
      addToast(pgpPrivateKeyStorage === "browser" ? "PGP private key imported in this browser." : "PGP private key imported.");
    } catch (err) {
      const message = messageFromError(err);
      addToast(message, "error");
      throw new Error(message);
    } finally {
      setPGPSaving(false);
    }
  }

  async function generateIdentityPGPKey(passphrase: string) {
    if (!identityDraft.id) {
      addToast("Save the identity before generating a PGP key.", "error");
      return;
    }
    if (!pgpPlugin) {
      addToast("PGP plugin is still loading. Try again in a moment.", "error");
      return;
    }
    const issues = pgpPlugin.pgpPassphraseIssues(passphrase, identityPGPPassphraseValues());
    if (issues.length > 0) {
      addToast(issues[0], "error");
      return;
    }
    setPGPGenerating(true);
    try {
      const generated = await pgpPlugin.generatePrivateKey(identityDraft.display_name, identityDraft.email, passphrase);
      const saved = await pgpPlugin.savePrivateKey(csrf, {
        ...generated,
        identity_id: identityDraft.id,
        label: generated.label || identityDraft.email || "PGP key",
        private_key_armored: pgpPrivateKeyStorage === "server" ? generated.private_key_armored : "",
        private_key_storage: pgpPrivateKeyStorage,
        is_active_signing: true,
        is_active_encryption: true,
        is_decrypt_only: false
      });
      if (pgpPrivateKeyStorage === "browser") {
        try {
          await pgpPlugin.saveBrowserPGPPrivateKey(user.id, saved.key, generated.private_key_armored || "");
        } catch (err) {
          if (saved.key.id) {
            await pgpPlugin.deletePrivateKey(csrf, saved.key.id).catch(() => undefined);
          }
          throw err;
        }
      }
      const firstIdentityKey = !pgpKeys.some((key) => key.identity_id === identityDraft.id);
      setPGPKeys((current) => [...current.filter((key) => key.id !== saved.key.id), saved.key]);
      if (firstIdentityKey && saved.key.is_active_encryption && !saved.key.is_decrypt_only) {
        markIdentityAutocryptEnabled(identityDraft.id);
      }
      setPGPGenerateOpen(false);
      addToast(pgpPrivateKeyStorage === "browser" ? "PGP private key generated and saved in this browser." : "PGP private key generated in this browser.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setPGPGenerating(false);
    }
  }

  async function deleteIdentityPGPKey(id: number) {
    const key = pgpKeys.find((item) => item.id === id);
    const storageLabel = key?.private_key_storage === "browser" ? "browser private key and server public-key metadata" : "PGP private key from rolltop";
    if (!window.confirm(`Remove this ${storageLabel}? Export it first if this is your only copy.`)) return;
    try {
      if (!pgpPlugin) throw new Error("PGP plugin is still loading. Try again in a moment.");
      await pgpPlugin.deletePrivateKey(csrf, id);
      if (key?.private_key_storage === "browser") {
        await pgpPlugin?.deleteBrowserPGPPrivateKey(user.id, id).catch(() => undefined);
      }
      setPGPKeys((current) => current.filter((key) => key.id !== id));
      addToast("PGP private key removed.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function exportIdentityPGPKey(key: IdentityPGPPrivateKey, kind: "private" | "public" | "revocation") {
    let data = kind === "private" ? key.private_key_armored : kind === "public" ? key.public_key_armored : key.revocation_certificate;
    if (kind === "private" && key.private_key_storage === "browser" && !data?.trim() && key.id) {
      try {
        if (!pgpPlugin) throw new Error("PGP plugin is still loading. Try again in a moment.");
        data = await pgpPlugin.loadBrowserPGPPrivateKey(user.id, key.id);
      } catch (err) {
        addToast(messageFromError(err), "error");
        return;
      }
    }
    if (!data) {
      addToast(key.private_key_storage === "browser" ? "This private key is not saved in this browser." : "No key material available to export.", "error");
      return;
    }
    if (kind === "private" && !window.confirm([
      "Export this PGP private key?",
      "",
      "Do not send your private key or passphrase to anyone. Anyone with both can decrypt mail encrypted to you and sign mail as you.",
      "",
      "Only save it somewhere you control."
    ].join("\n"))) {
      return;
    }
    if (kind === "revocation" && !window.confirm([
      "Export this revocation certificate?",
      "",
      "This is the public kill switch for the key. Publish it only if the private key is lost, compromised, or retired."
    ].join("\n"))) {
      return;
    }
    const suffix = kind === "private" ? "private" : kind === "public" ? "public" : "publishable-revocation-certificate";
    const blob = new Blob([data], { type: "application/pgp-keys" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${identityDraft.email || "pgp-key"}-${suffix}.asc`;
    a.click();
    URL.revokeObjectURL(url);
  }


  async function saveProfile(event: FormEvent) {
    event.preventDefault();
    try {
      await api.saveProfile(csrf, profileForm);
      addToast("Preferences saved.");
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

  async function deleteSelectedIMAPAccount() {
    if (!account || deletingAccountID) return;
    setDeletingAccountID(account.id);
    try {
      const estimate = await api.imapAccountPurgeEstimate(account.id);
      const expected = estimate.account_name;
      const typed = window.prompt(imapDeletePrompt(estimate));
      if (typed === null) return;
      if (typed.trim() !== expected) {
        addToast("IMAP server name did not match. Nothing was removed.", "error");
        return;
      }
      await api.deleteIMAPAccount(csrf, account.id, typed.trim());
      addToast(`Removing ${expected} locally. Remote IMAP mail is untouched.`);
      setIMAPAccounts((current) => current.filter((item) => item.id !== account.id));
      setFolders((current) => current.filter((folder) => folder.mailbox.account_id !== account.id));
      setIdentities((current) => current.map((identity) => identity.imap_account_id === account.id ? { ...identity, imap_account_id: 0, sent_mailbox_id: 0, drafts_mailbox_id: 0 } : identity));
      setIdentityDraft((current) => current.imap_account_id === account.id ? { ...current, imap_account_id: 0, sent_mailbox_id: 0, drafts_mailbox_id: 0 } : current);
      setSelectedAccountID(null);
      setAccount(null);
      navigate("/settings/account");
      await refreshChrome();
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setDeletingAccountID(null);
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

  function purgeSearchIndexConfirmMessage(folder: SyncFolder) {
    return [
      `Purge the full-text index for ${folder.mailbox.name}?`,
      "",
      "This removes only rolltop's local full-text search documents for this folder.",
      "It does not delete local message references or messages from your IMAP server.",
      "The next sync will rebuild missing full-text entries before fetching new mail."
    ].join("\n");
  }

  function purgeLocalReferencesConfirmMessage(folder: SyncFolder) {
    return [
      `Purge local references and the full-text index for ${folder.mailbox.name}?`,
      "",
      "This removes rolltop's local message references, raw-message cache, and full-text search documents for this folder.",
      "It does not delete messages from your IMAP server.",
      "The next sync will refetch this folder from IMAP and rebuild the local full-text index."
    ].join("\n");
  }

  function folderRoleConflict(folder: SyncFolder, role: string) {
    if (!uniqueFolderRoles.has(role)) return null;
    return folders.find((item) =>
      item.mailbox.account_id === folder.mailbox.account_id &&
      item.mailbox.id !== folder.mailbox.id &&
      (item.mailbox.role || "") === role
    ) || null;
  }

  async function saveEditingFolder(event: FormEvent) {
    event.preventDefault();
    if (!editingFolderID || !folderDraft) return;
    const folder = folderMap.get(editingFolderID);
    if (!folder) {
      closeFolderSettings();
      return;
    }
    const nextDraft = { ...folderDraft };
    if (!folderCanInherit(folder.mailbox) && nextDraft.sync_mode === "inherit") {
      nextDraft.sync_mode = "auto";
    }
    const conflict = folderRoleConflict(folder, nextDraft.role || "");
    if (conflict) {
      addToast(`${folderRoleLabel(nextDraft.role || "")} is already assigned to ${conflict.mailbox.name}.`, "error");
      return;
    }
    const saved = await saveFolderSettings(folder, nextDraft);
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

  async function purgeFolderSearchIndex(folder: SyncFolder) {
    if (!window.confirm(purgeSearchIndexConfirmMessage(folder))) return;
    try {
      await api.purgeFolderSearchIndex(csrf, folder.mailbox.id);
      addToast(`${folder.mailbox.name} full-text index purge started.`);
      await refreshChrome();
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function purgeFolderLocalReferences(folder: SyncFolder) {
    if (!window.confirm(purgeLocalReferencesConfirmMessage(folder))) return;
    try {
      await api.purgeFolderLocalReferences(csrf, folder.mailbox.id);
      addToast(`${folder.mailbox.name} local references purge started.`);
      await refreshChrome();
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  const allFolderMailboxes = useMemo(() => folders.map((folder) => folder.mailbox), [folders]);
  const selectedFolders = useMemo(
    () => selectedAccountID ? folders.filter((folder) => folder.mailbox.account_id === selectedAccountID) : folders,
    [folders, selectedAccountID]
  );
  const selectedFolderMailboxes = useMemo(() => selectedFolders.map((folder) => folder.mailbox), [selectedFolders]);
  const folderMap = useMemo(() => new Map(selectedFolders.map((folder) => [folder.mailbox.id, folder])), [selectedFolders]);
  const folderNodes = useMemo(() => folderTree(selectedFolderMailboxes, { includeHidden: true }), [selectedFolderMailboxes]);
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

  function renderIdentitySummary() {
    const primary = identities.find((identity) => identity.is_primary) || identities[0] || null;
    return (
      <section className="panel account-list-panel">
        <div className="panel-headline">
          <div>
            <h2>Identities</h2>
            <div className="muted">Outgoing names, SMTP server, IMAP server, and Sent/Drafts folders.</div>
          </div>
          <button className="secondary" type="button" onClick={() => navigate("/settings/account/identities")}><Icon name="group" />Manage</button>
        </div>
        <button className="server-row" type="button" onClick={() => navigate("/settings/account/identities")}>
          <span className="server-row-icon"><Icon name="group" /></span>
          <strong>{identities.length === 1 ? "1 identity" : `${identities.length} identities`}</strong>
          <small>{primary ? `${primary.display_name || primary.email} · ${primary.email}` : "No Me identities configured"}</small>
        </button>
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
      const searchLabel = !folder.mailbox.include_in_search
        ? "Search off"
        : searchPercent === null
          ? "Search n/a"
          : `Search ${searchPercent}%`;
      const currentRole = folder.mailbox.role || "";
      const currentIcon = folder.mailbox.icon || "folder";
      const syncLabel = folderSyncModeLabel(folder.mailbox.sync_mode || "inherit");
      const effectiveSyncMode = effectiveMailboxSyncMode(folder.mailbox, selectedFolderMailboxes);
      const canShowSyncAction = effectiveSyncMode !== "never";
      const roleLabel = folderRoleLabel(currentRole);
      const iconLabel = folderIconLabel(currentIcon);
      const visibilityLabel = folderVisibilityLabel(folder.mailbox);
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
              {canShowSyncAction ? (
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
              ) : null}
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

  function renderIdentityPGPSettings() {
    const keys = identityDraft.id ? pgpKeys.filter((key) => key.identity_id === identityDraft.id) : [];
    const PGPKeyImportModal = pgpPlugin?.KeyImportModal;
    const PGPKeyGenerateModal = pgpPlugin?.KeyGenerateModal;
    return (
      <section className="identity-pgp-settings">
        <div className="panel-headline">
          <div>
            <h3>PGP keys</h3>
            <div className="muted">Public keys are saved on the rolltop server so Autocrypt and public-key attachments work from every browser. Private keys can stay in this browser only, or be stored as a server-encrypted copy for unlock/export on other browsers. Your PGP passphrase, key unlock, and message decryption stay in this browser.</div>
          </div>
        </div>
        {!identityDraft.id ? <div className="notice subtle">Save this identity before adding PGP keys.</div> : null}
        <label className="identity-primary identity-autocrypt-toggle">
          <input
            type="checkbox"
            checked={identityDraft.autocrypt_enabled ?? true}
            onChange={(event) => updateIdentityDraft({ autocrypt_enabled: event.target.checked })}
          />
          Advertise public key with Autocrypt
        </label>
        <div className="identity-pgp-key-list">
          {keys.length === 0 ? <div className="muted">No PGP private keys saved for this identity.</div> : null}
          {keys.map((key) => (
            <div className="identity-pgp-key-row" key={key.id || key.fingerprint}>
              <Icon name="lock" />
              <span>
                <strong>{key.label || key.fingerprint || "PGP key"}</strong>
                <small>{[
                  shortPGPValue(key.fingerprint || key.key_id),
                  firstPGPUserID(key.user_ids),
                  key.private_key_storage === "browser" ? "Private key in browser storage" : "Private key server-stored",
                  key.created_at ? `Imported ${displayDateTime(key.created_at, user)}` : ""
                ].filter(Boolean).join(" · ")}</small>
              </span>
              <div className="identity-pgp-key-actions">
                <details className="message-menu identity-pgp-key-menu">
                  <summary className="icon-action" title="PGP key actions" aria-label="PGP key actions">
                    <Icon name="more_vert" />
                  </summary>
                  <div className="message-menu-panel identity-pgp-key-menu-panel">
                    <button type="button" onClick={() => void exportIdentityPGPKey(key, "public")}>
                      <Icon name="signature" />
                      <span><strong>Export public key</strong><small>Share this so others can encrypt mail to you and verify your signatures.</small></span>
                    </button>
                    <button type="button" onClick={() => void exportIdentityPGPKey(key, "private")}>
                      <Icon name="lock" />
                      <span><strong>Export private key</strong><small>Danger: never send this key or its passphrase to anyone.</small></span>
                    </button>
                    {key.revocation_certificate ? (
                      <button type="button" onClick={() => void exportIdentityPGPKey(key, "revocation")}>
                        <Icon name="report" />
                        <span><strong>Download publishable revocation certificate</strong><small>Publish this only if the key is lost, compromised, or retired.</small></span>
                      </button>
                    ) : null}
                    {key.id ? (
                      <button className="danger" type="button" onClick={() => void deleteIdentityPGPKey(key.id || 0)}>
                        <Icon name="delete" />
                        <span><strong>Remove saved private key</strong><small>Deletes this rolltop server copy; it does not revoke the key.</small></span>
                      </button>
                    ) : null}
                  </div>
                </details>
              </div>
            </div>
          ))}
        </div>
        <div className="identity-pgp-storage-choice">
          <strong>Private key storage for new keys</strong>
          <label>
            <input
              type="radio"
              checked={pgpPrivateKeyStorage === "browser"}
              onChange={() => setPGPPrivateKeyStorage("browser")}
            />
            <span><strong>This browser only</strong><small>Best server compromise: rolltop saves the public key, while this browser keeps the private key. Other browsers must import the same private key before they can decrypt or sign.</small></span>
          </label>
          <label>
            <input
              type="radio"
              checked={pgpPrivateKeyStorage === "server"}
              onChange={() => setPGPPrivateKeyStorage("server")}
            />
            <span><strong>Server-encrypted copy</strong><small>More convenient across browsers. The server stores the armored private key encrypted with the rolltop master key, and your PGP passphrase is still required in the browser.</small></span>
          </label>
        </div>
        <div className="identity-pgp-grid">
          <section className="identity-pgp-action-card">
            <h4>Import private key</h4>
            <p>Bring in an existing ASCII-armored private key from a file or pasted text.</p>
            <button className="secondary" type="button" disabled={!identityDraft.id || pgpSaving} onClick={() => setPGPPrivateImportOpen(true)}>
              {pgpSaving ? "Importing..." : "Import key"}
            </button>
          </section>
          <section className="identity-pgp-action-card">
            <h4>Generate private key</h4>
            <p>Create a new passphrase-protected key in this browser using the storage choice above.</p>
            <button className="secondary" type="button" disabled={!identityDraft.id || pgpGenerating} onClick={() => setPGPGenerateOpen(true)}>
              {pgpGenerating ? "Generating..." : "Generate key"}
            </button>
          </section>
        </div>
        {pgpPrivateImportOpen && PGPKeyImportModal ? (
          <PGPKeyImportModal
            title="Import private key"
            description={pgpPrivateKeyStorage === "browser" ? "Paste, drop, or choose a passphrase-protected ASCII-armored PGP private key. rolltop saves the public key on the server and keeps the private key in this browser only." : "Paste, drop, or choose a passphrase-protected ASCII-armored PGP private key. rolltop stores a server-encrypted private-key copy for unlock/export in your browsers."}
            placeholder="-----BEGIN PGP PRIVATE KEY BLOCK-----"
            busy={pgpSaving}
            onCancel={() => { if (!pgpSaving) setPGPPrivateImportOpen(false); }}
            onImport={(armored) => importIdentityPGPKey(armored)}
          />
        ) : null}
        {pgpGenerateOpen && PGPKeyGenerateModal ? (
          <PGPKeyGenerateModal
            email={identityDraft.email}
            busy={pgpGenerating}
            validatePassphrase={(passphrase) => pgpPlugin?.pgpPassphraseIssues(passphrase, identityPGPPassphraseValues()) || []}
            onCancel={() => { if (!pgpGenerating) setPGPGenerateOpen(false); }}
            onGenerate={(passphrase) => generateIdentityPGPKey(passphrase)}
          />
        ) : null}
      </section>
    );
  }

  function renderIdentitySettings() {
    const selectedTitle = selectedIdentityID === "new" ? "New identity" : identityDraft.display_name || identityDraft.email || "Identity";
    return (
      <section className="contacts-shell identity-settings-shell">
        <aside className="contacts-list">
          <div className="contacts-list-items">
            {identities.length === 0 ? <div className="muted">No identities yet.</div> : null}
            {identities.map((identity) => (
              <button
                type="button"
                className={`contact-row ${identity.id === selectedIdentityID ? "active" : ""}`}
                key={identity.id}
                onClick={() => chooseIdentity(identity)}
              >
                <span className="server-row-icon"><Icon name="group" /></span>
                <span>
                  <strong>{identity.display_name || identity.email}</strong>
                  <small>{identity.email}</small>
                </span>
                {identity.is_primary ? <em>Primary</em> : null}
              </button>
            ))}
          </div>
        </aside>
        <form className="contact-editor identity-editor" onSubmit={(event) => { event.preventDefault(); void saveIdentity(identityDraft); }}>
          <div className="panel-headline">
            <div>
              <h2>{selectedTitle}</h2>
              <div className="muted">SMTP, IMAP, Sent/Drafts folders, and signature.</div>
            </div>
          </div>
          <div className="identity-main">
            <Field label="Display name" value={identityDraft.display_name} required onChange={(value) => updateIdentityDraft({ display_name: value })} />
            <div>
              <label>Email</label>
              <input
                type="email"
                value={identityDraft.email}
                readOnly={identityDraft.id > 0}
                required
                onChange={(event) => updateIdentityDraft({ email: event.target.value })}
              />
            </div>
            <div>
              <label>Outbound SMTP server</label>
              <select value={identityDraft.smtp_account_id || 0} onChange={(event) => updateIdentityDraft({ smtp_account_id: Number(event.target.value) })}>
                <option value={0}>Default</option>
                {smtpAccounts.map((smtp) => <option value={smtp.id} key={smtp.id}>{smtpAccountLabel(smtp)}</option>)}
              </select>
            </div>
            <label className="identity-primary"><input type="checkbox" checked={identityDraft.is_primary} onChange={(event) => updateIdentityDraft({ is_primary: event.target.checked })} /> Primary</label>
          </div>
          <IdentityMailboxFields identity={identityDraft} accounts={imapAccounts} smtpAccounts={smtpAccounts} mailboxes={allFolderMailboxes} updateIdentity={(_, patch) => updateIdentityDraft(patch)} />
          {pgpEnabled ? renderIdentityPGPSettings() : null}
          <div>
            <label>Signature</label>
            <RichSignatureEditor value={identityDraft.signature} onChange={(value) => updateIdentityDraft({ signature: value })} />
          </div>
          <div className="contact-savebar">
            <button disabled={savingIdentity}>{savingIdentity ? "Saving..." : "Save identity"}</button>
          </div>
        </form>
      </section>
    );
  }

  function renderProfileSettings() {
    const displayName = user.name || user.email;
    return (
      <section className="panel profile-settings">
        <div className="panel-headline profile-settings-headline">
          <div>
            <h2>Profile</h2>
            <div className="muted">Signed in as {displayName}</div>
          </div>
        </div>
        <div className="profile-account-summary">
          <span className="settings-field-label">Account</span>
          <strong>{displayName}</strong>
          <small>{user.email}</small>
        </div>
        <form className="profile-backup-email" onSubmit={saveProfile}>
          <div>
            <label>Backup email</label>
            <input
              type="email"
              value={profileForm.backup_email}
              placeholder="Used only for password resets"
              onChange={(event) => setProfileForm((current) => ({ ...current, backup_email: event.target.value }))}
            />
          </div>
          <button className="secondary" type="submit">Save backup email</button>
        </form>
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
            <label>Locale</label>
            <select value={profileForm.date_locale} onChange={(event) => setProfileForm((current) => ({ ...current, date_locale: event.target.value }))}>
              {(profileForm.date_locale && !dateLocaleChoices.some((choice) => choice.value === profileForm.date_locale)
                ? [...dateLocaleChoices, { value: profileForm.date_locale, label: `${profileForm.date_locale} (saved custom)` }]
                : dateLocaleChoices
              ).map((choice) => (
                <option value={choice.value} key={choice.value || "browser-default"}>{choice.label}</option>
              ))}
            </select>
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
              {(availableThemes.length > 0 ? availableThemes : fallbackThemes()).map((theme) => (
                <option value={theme.id} key={theme.id}>{theme.name}</option>
              ))}
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

  function renderSearchSettings() {
    return (
      <form className="panel search-tuning-settings" onSubmit={saveProfile}>
        <div className="panel-headline">
          <div>
            <h2>Search tuning</h2>
            <div className="muted">These are query-time ranking controls, so changes do not require a reindex.</div>
          </div>
        </div>
        <div className="search-tuning-list">
          <SearchSliderRow
            title="Ranking profile"
            value={profileForm.search_preset}
            choices={searchPresetChoices}
            description="Strict reduces expansion, Balanced keeps the current defaults with recent-first ranking, and Forgiving widens typo and attachment matching."
            onChange={(value) => setProfileForm((current) => ({ ...current, ...searchPresetDefaults(value) }))}
          />
          <SearchSliderRow
            title="Typo matching"
            value={profileForm.search_fuzzy}
            choices={fuzzyChoices}
            description="Controls unquoted fuzzy matching for typos. Quoted searches remain literal."
            onChange={(value) => setProfileForm((current) => ({ ...current, search_fuzzy: value }))}
          />
          <SearchSliderRow
            title="Recent mail boost"
            value={profileForm.search_recency_bias}
            choices={recencyChoices}
            description="Normal now favors recent comparable matches; Strong is an aggressive recent-first ranking mode."
            onChange={(value) => setProfileForm((current) => ({ ...current, search_recency_bias: value }))}
          />
          <SearchSliderRow
            title="Attachment text weight"
            value={profileForm.search_attachment_weight}
            choices={attachmentWeightChoices}
            description="Adjusts whether attachment filenames and extracted text are ignored, lightly weighted, normal, or prominent."
            onChange={(value) => setProfileForm((current) => ({ ...current, search_attachment_weight: value }))}
          />
          <SearchSliderRow
            title="Sender history"
            value={profileForm.search_sender_history}
            choices={boostWeightChoices}
            description="Uses your read history to nudge senders you usually open higher in best-match results."
            onChange={(value) => setProfileForm((current) => ({ ...current, search_sender_history: value, search_sender_boost: value !== "none" }))}
          />
          <SearchSliderRow
            title="In contacts"
            value={profileForm.search_contact_boost}
            choices={boostWeightChoices}
            description="Boosts mail from senders saved in your contacts without changing which messages can match."
            onChange={(value) => setProfileForm((current) => ({ ...current, search_contact_boost: value }))}
          />
          <label className="search-tuning-row search-tuning-toggle">
            <div className="search-tuning-copy">
              <strong>Joined words</strong>
              <small>Lets searches like darkroom also consider strong dark room matches.</small>
            </div>
            <input type="checkbox" checked={profileForm.search_compact_splitting} onChange={(event) => setProfileForm((current) => ({ ...current, search_compact_splitting: event.target.checked }))} />
          </label>
        </div>
        <div className="actions"><button>Save search tuning</button></div>
      </form>
    );
  }

  function renderStorageSettings() {
    const indexBreakdown = storageIndexBreakdown(storage);
    const showIndexBreakdown = hasStorageIndexBreakdown(indexBreakdown);
    return (
      <section className="panel">
        <h2>Storage</h2>
        {storageLoading ? <div className="muted">Calculating storage usage...</div> : null}
        {storageError ? <div className="error">{storageError}</div> : null}
        <div className="storage-grid">
          <Stat label="Message Headers" value={formatBytes(storage.DatabaseBytes)} detail={storageEmailDetail(storage.MessageHeaderCount)} />
          <Stat label="Full Text Index" value={formatBytes(storage.IndexBytes)} detail={storageEmailDetail(storage.IndexMessageCount)} />
          <Stat label="Message Bodies" value={formatBytes(storage.BlobBytes)} detail={storageEmailDetail(storage.MessageBodyCount)} />
          <Stat label="Total" value={formatBytes(storage.TotalBytes)} detail={String(storage.Error || "")} />
        </div>
        {showIndexBreakdown ? (
          <>
            <h3>Full text index detail</h3>
            <div className="storage-grid">
              <Stat label="Index segments" value={formatBytes(indexBreakdown.ZapBytes)} detail={`${formatStatCount(indexBreakdown.ZapCount)} files`} />
              <Stat label="Largest segment" value={formatBytes(indexBreakdown.LargestZapBytes)} detail={statDetail(indexBreakdown.LargestZapPath)} />
              <Stat label="Root metadata" value={formatBytes(indexBreakdown.RootBytes)} detail="root.bolt" />
              <Stat label="Other index files" value={formatBytes(indexBreakdown.OtherBytes)} detail={`${formatStatCount(indexBreakdown.FileCount)} total files`} />
            </div>
          </>
        ) : null}
      </section>
    );
  }

  function renderLicenseSettings() {
    return (
      <section className="panel license-panel">
        <h2>License</h2>
        <p>
          rolltop is free software licensed under the GNU Affero General Public License version 3 or later.
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
    const modeChoices = folderSyncModeChoices(folder.mailbox);
    const selectedSyncMode = modeChoices.some((choice) => choice.value === folderDraft.sync_mode) ? folderDraft.sync_mode : "auto";
    const selectedModeChoice = modeChoices.find((choice) => choice.value === selectedSyncMode);
    const roleConflict = folderRoleConflict(folder, folderDraft.role || "");

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
              <label className="settings-field-label" htmlFor="folder-sync-mode">Sync mode</label>
              <select
                id="folder-sync-mode"
                value={selectedSyncMode}
                onChange={(event) => updateFolderDraft({ sync_mode: event.target.value })}
              >
                {modeChoices.map((choice) => (
                  <option value={choice.value} key={choice.value}>{choice.label}</option>
                ))}
              </select>
              <div className="folder-mode-help">
                {selectedModeChoice ? (
                  <p><strong>{selectedModeChoice.label}:</strong> {selectedModeChoice.description}</p>
                ) : null}
              </div>
            </section>

            <section className="folder-edit-section">
              <label className="settings-field-label" htmlFor="folder-role">Folder role</label>
              <select
                id="folder-role"
                value={folderDraft.role || ""}
                onChange={(event) => updateFolderDraft({ role: event.target.value })}
              >
                {folderRoleChoices.map((choice) => {
                  const assigned = choice.value ? folderRoleConflict(folder, choice.value) : null;
                  return (
                    <option value={choice.value} key={choice.value || "normal"} disabled={Boolean(assigned)}>
                      {choice.label}{assigned ? ` - used by ${assigned.mailbox.name}` : ""}
                    </option>
                  );
                })}
              </select>
              {roleConflict ? <div className="folder-role-warning">{folderRoleLabel(folderDraft.role || "")} is already assigned to {roleConflict.mailbox.name}.</div> : null}
            </section>

            <section className="folder-edit-section folder-edit-section-wide">
              <span className="settings-field-label">Sidebar icon</span>
              <div className="folder-icon-grid" aria-label="Sidebar icon">
                {folderIconChoices.map((choice) => (
                  <button
                    className={currentIcon === choice.value ? "active" : ""}
                    type="button"
                    key={choice.value}
                    onClick={() => updateFolderDraft({ icon: choice.value })}
                    title={choice.label}
                    aria-label={choice.label}
                  >
                    <Icon name={choice.value} weight={currentIcon === choice.value ? "bold" : undefined} />
                  </button>
                ))}
              </div>
            </section>

            <section className="folder-edit-section">
              <span className="settings-field-label">Visible in</span>
              <div className="folder-edit-visibility">
                {folderVisibilityChoices.map((choice) => {
                  const active = Boolean(folderDraft[choice.key]);
                  return (
                    <button
                      className={active ? "active" : ""}
                      type="button"
                      key={choice.key}
                      onClick={() => updateFolderDraft({ [choice.key]: !active } as Partial<FolderSettingsDraft>)}
                      aria-pressed={active}
                    >
                      {choice.label}
                    </button>
                  );
                })}
              </div>
            </section>

            <section className="folder-edit-section folder-edit-section-wide folder-purge-section">
              <span className="settings-field-label">Local purge</span>
              <div className="folder-purge-note">These actions only change rolltop's local cache. They never delete messages from your IMAP server.</div>
              <div className="folder-purge-actions">
                <button
                  className="secondary folder-purge-button"
                  type="button"
                  disabled={folder.is_running}
                  onClick={() => purgeFolderSearchIndex(folder)}
                  title={folder.is_running ? "Wait for this folder's current sync to finish" : "Purge only the local full-text search index"}
                >
                  <Icon name="search" />Purge full-text index
                </button>
                <button
                  className="secondary folder-purge-button danger"
                  type="button"
                  disabled={folder.is_running}
                  onClick={() => purgeFolderLocalReferences(folder)}
                  title={folder.is_running ? "Wait for this folder's current sync to finish" : "Purge local references and the local full-text search index"}
                >
                  <Icon name="delete" />Purge references and index
                </button>
              </div>
            </section>
          </div>

          <footer className="folder-edit-footer">
            <button className="secondary" type="button" onClick={closeFolderSettings}>Cancel</button>
            <button type="submit" disabled={Boolean(roleConflict)}>Save settings</button>
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

  if (route.kind === "identities") {
    return (
      <>
        <div className="content-head">
          <div className="list-head-main">
            <button className="icon-button" type="button" onClick={() => navigate("/settings/account")} title="Back to settings"><Icon name="arrow_back" /></button>
            <div>
              <h1>Identities</h1>
              <span className="label-pill">{identities.length.toLocaleString()}</span>
            </div>
          </div>
          <button className="secondary" type="button" onClick={newIdentity}><Icon name="edit" />New</button>
        </div>
        {loading ? <div className="panel muted">Loading settings...</div> : null}
        {notice ? <div className="notice">{notice}</div> : null}
        {renderIdentitySettings()}
      </>
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
          <div className="actions split-actions">
            <button>Save IMAP server</button>
            {account ? (
              <button className="danger secondary" type="button" disabled={deletingAccountID === account.id} onClick={deleteSelectedIMAPAccount}>
                <Icon name="delete" />{deletingAccountID === account.id ? "Removing..." : "Remove IMAP Server"}
              </button>
            ) : null}
          </div>
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
          <div className="actions split-actions">
            <button>Save SMTP server</button>
            {selectedSMTP ? (
              <button className="danger secondary" type="button" disabled={deletingSMTPID === selectedSMTP.id} onClick={deleteSelectedSMTPAccount}>
                <Icon name="delete" />{deletingSMTPID === selectedSMTP.id ? "Removing..." : "Remove SMTP Server"}
              </button>
            ) : null}
          </div>
        </form>
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
      {renderProfileSettings()}
      <div className="settings-server-index">
        {renderIMAPList()}
        {renderSMTPList()}
        {renderIdentitySummary()}
      </div>
      {renderDisplaySettings()}
      {renderSearchSettings()}
      {renderStorageSettings()}
      {renderLicenseSettings()}
    </>
  );
}


function shortPGPValue(value: string): string {
  const clean = value.replace(/\s+/g, "");
  if (clean.length <= 16) return clean;
  return `${clean.slice(0, 8)}...${clean.slice(-8)}`;
}

function normalizedPGPIdentifier(value: string): string {
  return value.replace(/[\s:]/g, "").toUpperCase();
}

function firstPGPUserID(value: string): string {
  return value.split(/\r?\n/).map((item) => item.trim()).find(Boolean) || "";
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
  const [passwordResetFromAddress, setPasswordResetFromAddress] = useState("");
  const [passwords, setPasswords] = useState<Record<number, string>>({});

  const load = useCallback(async () => {
    const userData = await api.users();
    setUsers(userData.users);
    setPasswordResetFromAddress(userData.password_reset_from_address || "");
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

  async function savePasswordResetSettings(event: FormEvent) {
    event.preventDefault();
    try {
      const saved = await api.savePasswordResetSettings(csrf, passwordResetFromAddress);
      setPasswordResetFromAddress(saved.from_address || "");
      addToast("Password reset settings saved.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function changePassword(user: User) {
    const password = passwords[user.id] || "";
    if (password.length < 12) {
      addToast("Password must be at least 12 characters.", "error");
      return;
    }
    try {
      await api.setUserPassword(csrf, user.id, password);
      setPasswords((current) => ({ ...current, [user.id]: "" }));
      addToast(`Password changed for ${user.email}.`);
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function deleteAccount(user: User) {
    if (!window.confirm(`Delete ${user.email} from rolltop? This removes the local account and local rolltop data for that user. It does not delete remote IMAP mail.`)) return;
    try {
      await api.deleteUser(csrf, user.id);
      addToast(`Deleted ${user.email}.`);
      await load();
      await refreshChrome();
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
        <h2>Password resets</h2>
        <form className="admin-reset-settings" onSubmit={savePasswordResetSettings}>
          <div>
            <label>Password Reset from address</label>
            <input
              type="email"
              value={passwordResetFromAddress}
              placeholder="Defaults to the signed-in admin address"
              onChange={(event) => setPasswordResetFromAddress(event.target.value)}
            />
            <small>Reset links are sent as ordinary unencrypted email to the user's backup email address.</small>
          </div>
          <button className="secondary" type="submit">Save reset settings</button>
        </form>
      </section>
      <section className="panel">
        <h2>Existing users</h2>
        <table>
          <thead>
            <tr><th>Email</th><th>Name</th><th>Backup email</th><th>Role</th><th>Admin actions</th></tr>
          </thead>
          <tbody>
            {users.map((user) => (
              <tr key={user.id}>
                <td>{user.email}</td>
                <td>{user.name}</td>
                <td>{user.backup_email || "Not set"}</td>
                <td>{user.is_admin ? "Admin" : "User"}</td>
                <td>
                  <div className="admin-user-actions">
                    <input
                      type="password"
                      placeholder="New password"
                      value={passwords[user.id] || ""}
                      onChange={(event) => setPasswords((current) => ({ ...current, [user.id]: event.target.value }))}
                    />
                    <button className="secondary" type="button" onClick={() => void changePassword(user)}>Change</button>
                    <button className="danger" type="button" onClick={() => void deleteAccount(user)}>Delete</button>
                  </div>
                </td>
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
