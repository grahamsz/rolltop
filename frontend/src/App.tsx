import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ChangeEvent, DragEvent, FormEvent, KeyboardEvent, MouseEvent, ReactNode } from "react";
import {
  Archive,
  ArrowBendUpLeft,
  ArrowBendUpRight,
  ArrowLeft,
  ArrowsClockwise,
  Bell,
  CaretDown,
  CaretLeft,
  CaretRight,
  DotsThreeVertical,
  EnvelopeSimple,
  Folder,
  GearSix,
  Image,
  ListBullets,
  ListNumbers,
  MagnifyingGlass,
  Mailbox as MailboxIcon,
  Minus,
  NotePencil,
  Paperclip,
  PaperPlaneTilt,
  PencilSimple,
  Quotes,
  SealWarning,
  ShoppingBag,
  Star,
  Tag,
  TextAa,
  Trash,
  Tray,
  Users,
  X
} from "@phosphor-icons/react";
import type { Icon as PhosphorIcon, IconWeight } from "@phosphor-icons/react";
import { ApiError, api } from "./api";
import type {
  Account,
  Bootstrap,
  ChromeEvent,
  ComposeForm,
  Conversation,
  HeaderDetail,
  Mailbox,
  StorageStats,
  SyncFolder,
  SyncRun,
  ThreadMessage,
  User
} from "./types";

type LocationState = {
  path: string;
  search: string;
};

type Toast = {
  id: number;
  kind: "loading" | "success" | "error";
  message: string;
};

type MoveTarget = {
  id: number;
  name: string;
};

type FolderNode = {
  mailbox: Mailbox;
  label: string;
  children: FolderNode[];
};

type DatePrefs = Pick<User, "date_locale" | "date_format">;

const emptyCompose: ComposeForm = {
  to: "",
  cc: "",
  bcc: "",
  subject: "",
  body: "",
  body_html: "",
  in_reply_to_id: 0
};

const mailPageSize = 50;

function currentLocation(): LocationState {
  return { path: window.location.pathname, search: window.location.search };
}

function routeWithSearch(path: string, search = ""): string {
  return `${path}${search}`;
}

function messageFromError(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "Something went wrong.";
}

function positiveInt(value: string | null | undefined, fallback: number): number {
  const raw = value || "";
  const number = raw.startsWith("p") ? raw.slice(1) : raw;
  const parsed = Number.parseInt(number, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

function decodePathSegment(value = ""): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return "";
  }
}

function mailRoute(path: string): { mailboxID: string | null; page: number } {
  const parts = path.split("/").filter(Boolean);
  if (parts[0] === "mailbox") {
    const id = positiveInt(parts[1], 0);
    return { mailboxID: id > 0 ? String(id) : null, page: positiveInt(parts[2], 1) };
  }
  return { mailboxID: null, page: parts[0] === "mail" ? positiveInt(parts[1], 1) : 1 };
}

function mailURL(mailboxID: string | number | null, page = 1): string {
  const suffix = page > 1 ? `/p${page}` : "";
  return mailboxID ? `/mailbox/${mailboxID}${suffix}` : `/mail${suffix}`;
}

function searchRoute(path: string): { query: string; sort: string; page: number } {
  const parts = path.split("/").filter(Boolean);
  if (parts[0] === "search" && parts[1]?.startsWith("p")) {
    return { query: "", sort: "best", page: positiveInt(parts[1], 1) };
  }
  if (parts[0] !== "search" || parts[1] !== "q") return { query: "", sort: "best", page: 1 };
  const query = decodePathSegment(parts[2]);
  let page = 1;
  for (const part of parts.slice(3)) {
    if (part.startsWith("p")) page = positiveInt(part, page);
  }
  return { query, sort: "best", page };
}

function searchURL(query: string, page = 1): string {
  const trimmed = query.trim();
  if (!trimmed) return page > 1 ? `/search/p${page}` : "/search";
  const pagePart = page > 1 ? `/p${page}` : "";
  return `/search/q/${encodeURIComponent(trimmed)}${pagePart}`;
}

function formatBytes(value: unknown): string {
  const bytes = typeof value === "number" ? value : Number(value || 0);
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let amount = bytes;
  let index = 0;
  while (amount >= 1024 && index < units.length - 1) {
    amount /= 1024;
    index++;
  }
  return `${amount >= 10 || index === 0 ? amount.toFixed(0) : amount.toFixed(1)} ${units[index]}`;
}

function dateLocale(prefs?: DatePrefs): string | undefined {
  const locale = prefs?.date_locale?.trim();
  if (!locale) return undefined;
  try {
    Intl.DateTimeFormat(locale);
    return locale;
  } catch {
    return undefined;
  }
}

function isSameLocalDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

function isOlderThanLastYear(date: Date, now = new Date()): boolean {
  const cutoff = new Date(now);
  cutoff.setFullYear(cutoff.getFullYear() - 1);
  cutoff.setHours(0, 0, 0, 0);
  return date < cutoff;
}

function numericDate(date: Date, prefs?: DatePrefs): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  const mm = pad(date.getMonth() + 1);
  const dd = pad(date.getDate());
  const yy = pad(date.getFullYear() % 100);
  switch (prefs?.date_format) {
    case "dmy":
      return `${dd}/${mm}/${yy}`;
    case "ymd":
      return `${yy}/${mm}/${dd}`;
    case "locale":
      return date.toLocaleDateString(dateLocale(prefs), { year: "2-digit", month: "numeric", day: "numeric" });
    case "mdy":
    default:
      return `${mm}/${dd}/${yy}`;
  }
}

function displayTime(value: string, prefs?: DatePrefs): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const today = new Date();
  const locale = dateLocale(prefs);
  if (isSameLocalDay(date, today)) {
    return date.toLocaleTimeString(locale, { hour: "numeric", minute: "2-digit" });
  }
  if (isOlderThanLastYear(date, today)) {
    return numericDate(date, prefs);
  }
  return date.toLocaleDateString(locale, { month: "short", day: "numeric" });
}

function displayDateTime(value: string, prefs?: DatePrefs): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const locale = dateLocale(prefs);
  if (isOlderThanLastYear(date)) {
    return `${numericDate(date, prefs)} ${date.toLocaleTimeString(locale, { hour: "numeric", minute: "2-digit" })}`;
  }
  return date.toLocaleString(locale, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" });
}

function searchHighlightTerms(query: string, extraTerms: string[] = []): string[] {
  const seen = new Set<string>();
  const terms: string[] = [];
  const add = (raw: string) => {
    const value = raw.trim().replace(/^[`"'~*()[\]{}<>.,;:]+|[`"'~*()[\]{}<>.,;:]+$/g, "");
    if (!value) return;
    const lower = value.toLocaleLowerCase();
    if (["and", "or", "not"].includes(lower) || seen.has(lower)) return;
    seen.add(lower);
    terms.push(value);
  };

  for (const token of searchQueryTokens(query)) {
    const fieldIndex = token.indexOf(":");
    let value = token;
    if (fieldIndex > 0) {
      const field = token.slice(0, fieldIndex).trim().toLocaleLowerCase();
      if (["after", "before", "newer", "older", "has", "is"].includes(field)) continue;
      value = token.slice(fieldIndex + 1);
    }
    add(value);
    value.split(/[^\p{L}\p{N}]+/u).forEach((word) => {
      if ([...word].length >= 3) add(word);
    });
  }
  extraTerms.forEach(add);
  return terms.sort((a, b) => [...b].length - [...a].length).slice(0, 16);
}

function searchQueryTokens(query: string): string[] {
  const tokens: string[] = [];
  let index = 0;
  while (index < query.length) {
    while (index < query.length && /\s/u.test(query[index])) index++;
    if (index >= query.length) break;
    let token = "";
    let quote = "";
    while (index < query.length) {
      const char = query[index];
      if (quote) {
        if (char === quote) {
          quote = "";
        } else {
          token += char;
        }
        index++;
        continue;
      }
      if (char === "\"" || char === "'") {
        quote = char;
        index++;
        continue;
      }
      if (/\s/u.test(char)) break;
      token += char;
      index++;
    }
    if (token.trim()) tokens.push(token.trim());
  }
  return tokens;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function highlightRegExp(query: string, extraTerms: string[] = []): RegExp | null {
  const terms = searchHighlightTerms(query, extraTerms);
  if (terms.length === 0) return null;
  try {
    return new RegExp(`(${terms.map(escapeRegExp).join("|")})`, "giu");
  } catch {
    return null;
  }
}

function HighlightedText({ text, query, terms = [] }: { text: string; query: string; terms?: string[] }) {
  const pattern = highlightRegExp(query, terms);
  if (!pattern || !text) return <>{text}</>;
  const parts = text.split(pattern);
  return (
    <>
      {parts.map((part, index) =>
        part === "" ? null : index % 2 === 1 ? (
          <mark className="search-hit" key={`${part}-${index}`}>
            {part}
          </mark>
        ) : (
          <span key={`${part}-${index}`}>{part}</span>
        )
      )}
    </>
  );
}

function safeInternalURL(value: string | null | undefined, fallback = "/mail"): string {
  if (!value) return fallback;
  try {
    const url = new URL(value, window.location.origin);
    if (url.origin !== window.location.origin) return fallback;
    return `${url.pathname}${url.search}${url.hash}`;
  } catch {
    return fallback;
  }
}

function messageBackURL(location: LocationState): string {
  return safeInternalURL(new URLSearchParams(location.search).get("back"), "/mail");
}

function messageURL(messageID: number, searchQuery = "", matchTerms: string[] = [], backURL = ""): string {
  const query = searchQuery.trim();
  if (!query && matchTerms.length === 0 && !backURL) return `/messages/${messageID}`;
  const params = new URLSearchParams();
  if (query) params.set("q", query);
  matchTerms.slice(0, 10).forEach((term) => {
    if (term.trim()) params.append("term", term.trim());
  });
  if (backURL) params.set("back", safeInternalURL(backURL));
  return `/messages/${messageID}?${params}`;
}

function messageHighlightQuery(location: LocationState): string {
  const params = new URLSearchParams(location.search);
  return params.get("q") || params.get("highlight") || "";
}

function messageHighlightTerms(location: LocationState): string[] {
  return new URLSearchParams(location.search).getAll("term");
}

function highlightEmailDocument(doc: Document | null | undefined, query: string, terms: string[] = []) {
  if (!doc || (!query.trim() && terms.length === 0)) return;
  const body = doc.body;
  if (!body) return;
  const pattern = highlightRegExp(query, terms);
  if (!pattern) return;
  if (!doc.head.querySelector("[data-mailmirror-highlight-style]")) {
    const style = doc.createElement("style");
    style.setAttribute("data-mailmirror-highlight-style", "true");
    style.textContent = "mark.mailmirror-search-hit{background:rgba(229,169,40,.26);color:#202426;border-radius:3px;padding:0 1px;box-shadow:none}";
    doc.head.appendChild(style);
  }
  const blocked = new Set(["SCRIPT", "STYLE", "NOSCRIPT", "TEMPLATE", "TEXTAREA", "MARK"]);
  const walker = doc.createTreeWalker(body, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      const parent = node.parentElement;
      if (!parent || blocked.has(parent.tagName)) return NodeFilter.FILTER_REJECT;
      if (!node.nodeValue || !pattern.test(node.nodeValue)) return NodeFilter.FILTER_REJECT;
      pattern.lastIndex = 0;
      return NodeFilter.FILTER_ACCEPT;
    }
  });
  const nodes: Text[] = [];
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    nodes.push(node as Text);
  }
  for (const node of nodes) {
    const value = node.nodeValue || "";
    pattern.lastIndex = 0;
    const fragment = doc.createDocumentFragment();
    let lastIndex = 0;
    for (const match of value.matchAll(pattern)) {
      const index = match.index ?? 0;
      if (index > lastIndex) fragment.appendChild(doc.createTextNode(value.slice(lastIndex, index)));
      const mark = doc.createElement("mark");
      mark.className = "mailmirror-search-hit";
      mark.textContent = match[0];
      fragment.appendChild(mark);
      lastIndex = index + match[0].length;
    }
    if (lastIndex < value.length) fragment.appendChild(doc.createTextNode(value.slice(lastIndex)));
    node.parentNode?.replaceChild(fragment, node);
  }
}

function textToHTML(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll("\n", "<br>");
}

function folderTree(mailboxes: Mailbox[]): FolderNode[] {
  const visible = mailboxes.filter((mailbox) => mailbox.show_in_sidebar !== false);
  const byName = new Map(visible.map((mailbox) => [mailbox.name, mailbox]));
  const nodes = new Map<number, FolderNode>();
  for (const mailbox of visible) {
    nodes.set(mailbox.id, { mailbox, label: folderLabel(mailbox.name), children: [] });
  }
  const roots: FolderNode[] = [];
  for (const mailbox of visible) {
    const node = nodes.get(mailbox.id);
    if (!node) continue;
    const parent = closestVisibleParent(mailbox.name, byName);
    if (!parent) {
      roots.push(node);
      continue;
    }
    const parentNode = nodes.get(parent.id);
    if (!parentNode) {
      roots.push(node);
      continue;
    }
    node.label = folderLabel(mailbox.name, parent.name);
    parentNode.children.push(node);
  }
  const sortNodes = (items: FolderNode[]) => {
    items.sort((a, b) => folderSortKey(a.mailbox).localeCompare(folderSortKey(b.mailbox), undefined, { numeric: true, sensitivity: "base" }));
    for (const item of items) sortNodes(item.children);
    return items;
  };
  return sortNodes(roots);
}

function closestVisibleParent(name: string, byName: Map<string, Mailbox>): Mailbox | null {
  for (const parent of folderParentNames(name)) {
    const mailbox = byName.get(parent);
    if (mailbox) return mailbox;
  }
  return null;
}

function folderParentNames(name: string): string[] {
  const out: string[] = [];
  for (let i = name.length - 1; i > 0; i--) {
    if (name[i] === "." || name[i] === "/" || name[i] === "\\") out.push(name.slice(0, i));
  }
  return out;
}

function folderLabel(name: string, parent = ""): string {
  if (!parent) return name;
  const next = name.slice(parent.length);
  return next.replace(/^[./\\]+/, "") || name;
}

function folderSortKey(mailbox: Mailbox): string {
  if (mailbox.role === "inbox" || mailbox.name.toLowerCase() === "inbox") return "00";
  if (mailbox.role === "trash") return `90:${mailbox.name}`;
  return `10:${mailbox.name}`;
}

function nodeContainsMailbox(node: FolderNode, id: string | null): boolean {
  if (!id) return false;
  return String(node.mailbox.id) === id || node.children.some((child) => nodeContainsMailbox(child, id));
}

function mailboxRefreshKey(run: SyncRun | null, mailbox: Mailbox | undefined): string {
  if (!run) return "";
  const current = run.current_mailbox.trim().toLowerCase();
  const active = mailbox?.name.trim().toLowerCase() || "";
  if (active && current && active !== current) return "";
  const hasNewMessages = run.messages_stored > 0;
  const finished = run.status !== "running" && Boolean(run.finished_at);
  if (!hasNewMessages && !finished) return "";
  return `::::::`;
}

function normalizedSyncMode(mode: string): string {
  const value = mode.trim().toLowerCase();
  if (value === "manual" || value === "never" || value === "inherit") return value;
  return "auto";
}

function effectiveMailboxSyncMode(mailbox: Mailbox, mailboxes: Mailbox[]): string {
  const direct = normalizedSyncMode(mailbox.sync_mode || "");
  if (direct !== "inherit") return direct;
  const byName = new Map(mailboxes.map((item) => [item.name.trim().toLowerCase(), item]));
  for (const parent of folderParentNames(mailbox.name)) {
    const parentMailbox = byName.get(parent.trim().toLowerCase());
    if (!parentMailbox) continue;
    const parentMode = normalizedSyncMode(parentMailbox.sync_mode || "");
    if (parentMode !== "inherit") return parentMode;
  }
  return "auto";
}

function syncPercent(mailbox: Mailbox): number {
  const value = Number(mailbox.sync_percent || 0);
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Math.min(100, Math.round(value)));
}

function mailboxNeedsSync(mailbox: Mailbox): boolean {
  if (mailbox.remote_uid_next > 1 && mailbox.last_uid < mailbox.remote_uid_next - 1) return true;
  return syncPercent(mailbox) > 0 && syncPercent(mailbox) < 100;
}

function mailboxActiveRun(mailbox: Mailbox | undefined, activeRuns: SyncRun[], latestRun: SyncRun | null): SyncRun | null {
  if (!mailbox) return null;
  const name = mailbox.name.trim().toLowerCase();
  const runs = mergeSyncRuns(activeRuns, latestRun ? [latestRun] : []);
  return runs.find((run) => run.status === "running" && run.current_mailbox.trim().toLowerCase() === name) || null;
}

function mergeSyncRuns(primary: SyncRun[], rest: SyncRun[]): SyncRun[] {
  const seen = new Set<number>();
  const out: SyncRun[] = [];
  for (const run of [...primary, ...rest]) {
    if (seen.has(run.id)) continue;
    seen.add(run.id);
    out.push(run);
  }
  return out;
}

const iconMap: Record<string, PhosphorIcon> = {
  archive: Archive,
  arrow_back: ArrowLeft,
  attach_file: Paperclip,
  chevron_left: CaretLeft,
  chevron_right: CaretRight,
  close: X,
  delete: Trash,
  draft: NotePencil,
  edit: PencilSimple,
  expand_more: CaretDown,
  folder: Folder,
  format_color_text: TextAa,
  format_list_bulleted: ListBullets,
  format_list_numbered: ListNumbers,
  format_quote: Quotes,
  forward: ArrowBendUpRight,
  group: Users,
  image: Image,
  inbox: Tray,
  label: Tag,
  mail: EnvelopeSimple,
  mailbox: MailboxIcon,
  mailmirror: MailboxIcon,
  minimize: Minus,
  more_vert: DotsThreeVertical,
  notifications: Bell,
  report: SealWarning,
  reply: ArrowBendUpLeft,
  search: MagnifyingGlass,
  send: PaperPlaneTilt,
  settings: GearSix,
  shopping_bag: ShoppingBag,
  sync: ArrowsClockwise
};

const iconAliases: Record<string, string> = {
  drafts: "draft",
  sent: "send",
  spam: "report",
  trash: "delete"
};

const iconWeights: Partial<Record<string, IconWeight>> = {
  mailmirror: "duotone",
  report: "duotone",
  sync: "duotone"
};

function Icon({ name, weight }: { name: string; weight?: IconWeight }) {
  const normalized = name.trim().toLowerCase().replaceAll("-", "_");
  const key = iconAliases[normalized] || normalized;
  const Component = iconMap[key] || Folder;
  return <Component className="icon" aria-hidden="true" focusable="false" weight={weight || iconWeights[key] || "regular"} />;
}

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

function SetupPage({
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
      navigate("/mail");
    } catch (err) {
      setError(messageFromError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="auth-page">
      <div className="auth-brand">MailMirror</div>
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

function LoginPage({
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
      <div className="auth-brand">MailMirror</div>
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

function AppShell({
  user,
  csrf,
  mailboxes,
  latestSyncRun,
  activeSyncRuns,
  syncRunning,
  accountNeedsPassword,
  accountNotice,
  location,
  navigate,
  logout,
  onMoveMessages,
  openCompose,
  refreshChrome,
  addToast,
  children
}: {
  user: User;
  csrf: string;
  mailboxes: Mailbox[];
  latestSyncRun: SyncRun | null;
  activeSyncRuns: SyncRun[];
  syncRunning: boolean;
  accountNeedsPassword: boolean;
  accountNotice: string;
  location: LocationState;
  navigate: (url: string) => void;
  logout: () => void;
  onMoveMessages: (messageIDs: number[], mailbox: MoveTarget) => void;
  openCompose: (query?: string) => void;
  refreshChrome: () => Promise<Bootstrap | null>;
  addToast: (message: string, kind?: Toast["kind"]) => number;
  children: ReactNode;
}) {
  return (
    <>
      <Topbar user={user} location={location} navigate={navigate} logout={logout} addToast={addToast} />
      <div className="app">
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
        />
        <main className="content">
          {accountNeedsPassword ? <AccountCredentialBanner notice={accountNotice} navigate={navigate} /> : null}
          {children}
        </main>
      </div>
    </>
  );
}

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

function Topbar({
  user,
  location,
  navigate,
  logout,
  addToast
}: {
  user: User;
  location: LocationState;
  navigate: (url: string) => void;
  logout: () => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [query, setQuery] = useState(() => searchRoute(currentLocation().path).query);
  const [focused, setFocused] = useState(false);
  const suggestions = useMemo(
    () => [
      ["is:unread ", "Unread messages"],
      ["is:read ", "Read messages"],
      ["is:starred ", "Starred messages"],
      ["is:notstarred ", "Unstarred messages"],
      ["has:attachment ", "Messages with attachments"],
      ["lang:ja ", "Japanese messages"],
      ["lang:fr ", "French messages"],
      ["from:", "Sender or domain"],
      ["to:", "Recipient"],
      ["after:2026/01/01 ", "Received after date"],
      ["before:2026/01/01 ", "Received before date"],
      ["subject:", "Subject text"],
      ["filename:", "Attachment name"]
    ],
    []
  );

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

  function chooseSuggestion(value: string) {
    const before = query.replace(/\S*$/, "");
    setQuery(`${before}${value}`);
    setFocused(false);
  }

  async function enableNotifications() {
    if (!("Notification" in window)) {
      addToast("This browser does not support notifications.", "error");
      return;
    }
    const result = await Notification.requestPermission();
    if (result === "granted") addToast("New-mail notifications enabled.");
    else addToast("Notifications were not enabled.", "error");
  }

  return (
    <header className="topbar">
      <a
        href="/mail"
        className="brand"
        onClick={(event) => {
          event.preventDefault();
          navigate("/mail");
        }}
      >
        <Icon name="mailmirror" />
        MailMirror
      </a>
      <form className="top-search" onSubmit={submit}>
        <Icon name="search" />
        <input
          type="search"
          placeholder="Search mail"
          value={query}
          onFocus={() => setFocused(true)}
          onBlur={() => window.setTimeout(() => setFocused(false), 120)}
          onChange={(event) => setQuery(event.target.value)}
          autoComplete="off"
        />
        {focused ? (
          <div className="search-suggest">
            {suggestions.map(([value, label]) => (
              <button type="button" className="search-suggestion" key={value} onMouseDown={() => chooseSuggestion(value)}>
                <code>{value.trim()}</code>
                <span>{label}</span>
              </button>
            ))}
          </div>
        ) : null}
      </form>
      <nav className="top-actions" aria-label="Account">
        <button className="ghost" type="button" title="Enable notifications" onClick={enableNotifications}>
          <Icon name="notifications" />
        </button>
        <button className="ghost" type="button" title="Settings" onClick={() => navigate("/settings/account")}>
          <Icon name="settings" />
        </button>
        {user.is_admin ? (
          <button className="ghost" type="button" title="Users" onClick={() => navigate("/admin/users")}>
            <Icon name="group" />
          </button>
        ) : null}
        <span className="user-chip">{user.name || user.email}</span>
        <button className="secondary" type="button" onClick={logout}>Logout</button>
      </nav>
    </header>
  );
}

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
  onMoveMessages
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
}) {
  const [dropID, setDropID] = useState<number | null>(null);
  const [expandedGroups, setExpandedGroups] = useState<Set<string>>(() => new Set());
  const activeMailbox = mailRoute(currentPath).mailboxID;
  const allMailActive = (currentPath === "/mail" || currentPath.startsWith("/mail/")) && !activeMailbox;
  const folders = useMemo(() => folderTree(mailboxes), [mailboxes]);

  function open(event: MouseEvent, url: string) {
    event.preventDefault();
    navigate(url);
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
    <aside className="sidebar">
      <a href="/compose" className="button compose" onClick={(event) => {
        event.preventDefault();
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
      </div>
      <SidebarSync csrf={csrf} latest={latestSyncRun} activeRuns={activeSyncRuns} running={syncRunning} refreshChrome={refreshChrome} />
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
  const visibleRuns = activeRuns.length > 0 ? activeRuns : latest ? [latest] : [];
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

function SyncRunMini({ run }: { run: SyncRun }) {
  const totalMessages = run.messages_total || 0;
  const totalFolders = run.mailboxes_total || 0;
  const progress = totalMessages > 0
    ? Math.min(100, Math.round((run.messages_seen / totalMessages) * 100))
      : totalFolders > 0
        ? Math.min(100, Math.round((run.mailboxes_done / totalFolders) * 100))
        : run.status === "running" ? 100 : 0;
  const indexedLabel = run.messages_stored > 0 ? `${run.messages_stored.toLocaleString()} indexed` : "Indexing...";
  const detail = run.messages_skipped > 0
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

function RouteView({
  csrf,
  user,
  mailboxes,
  latestSyncRun,
  activeSyncRuns,
  location,
  navigate,
  hiddenMessageIDs,
  openCompose,
  refreshChrome,
  addToast
}: {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  latestSyncRun: SyncRun | null;
  activeSyncRuns: SyncRun[];
  location: LocationState;
  navigate: (url: string) => void;
  hiddenMessageIDs: Set<number>;
  openCompose: (query?: string) => void;
  refreshChrome: () => Promise<Bootstrap | null>;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  if (location.path === "/search" || location.path.startsWith("/search/")) {
    return <SearchView csrf={csrf} location={location} navigate={navigate} hiddenMessageIDs={hiddenMessageIDs} datePrefs={user} addToast={addToast} />;
  }
  if (location.path.startsWith("/messages/")) {
    return (
      <ThreadView
        csrf={csrf}
        datePrefs={user}
        location={location}
        navigate={navigate}
        mailboxes={mailboxes}
        refreshChrome={refreshChrome}
        openCompose={openCompose}
        addToast={addToast}
      />
    );
  }
  if (location.path === "/compose") {
    return <ComposePage csrf={csrf} location={location} navigate={navigate} addToast={addToast} />;
  }
  if (location.path === "/settings/account") {
    return <SettingsView csrf={csrf} user={user} mailboxes={mailboxes} activeSyncRuns={activeSyncRuns} refreshChrome={refreshChrome} addToast={addToast} />;
  }
  if (location.path === "/admin/users" && user.is_admin) {
    return <AdminUsersView csrf={csrf} addToast={addToast} />;
  }
  if (location.path.startsWith("/sync-runs/")) {
    return <SyncRunView location={location} navigate={navigate} datePrefs={user} />;
  }
  return (
    <MailView
      csrf={csrf}
      datePrefs={user}
      location={location}
      navigate={navigate}
      hiddenMessageIDs={hiddenMessageIDs}
      mailboxes={mailboxes}
      latestSyncRun={latestSyncRun}
      activeSyncRuns={activeSyncRuns}
      refreshChrome={refreshChrome}
      addToast={addToast}
    />
  );
}

function MailView({
  csrf,
  datePrefs,
  location,
  navigate,
  hiddenMessageIDs,
  mailboxes,
  latestSyncRun,
  activeSyncRuns,
  refreshChrome,
  addToast
}: {
  csrf: string;
  datePrefs: DatePrefs;
  location: LocationState;
  navigate: (url: string) => void;
  hiddenMessageIDs: Set<number>;
  mailboxes: Mailbox[];
  latestSyncRun: SyncRun | null;
  activeSyncRuns: SyncRun[];
  refreshChrome: () => Promise<Bootstrap | null>;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [loading, setLoading] = useState(true);
  const [syncBusy, setSyncBusy] = useState(false);
  const loaded = useRef(false);
  const [error, setError] = useState("");
  const [hasPrev, setHasPrev] = useState(false);
  const [hasNext, setHasNext] = useState(false);
  const [newMessageIDs, setNewMessageIDs] = useState<Set<number>>(() => new Set());
  const previousPageIDs = useRef<Set<number>>(new Set());
  const previousListKey = useRef("");
  const newMessageTimer = useRef<number | null>(null);
  const route = mailRoute(location.path);
  const mailboxID = route.mailboxID;
  const page = route.page;
  const mailbox = mailboxes.find((item) => String(item.id) === mailboxID);
  const totalCount = mailbox ? mailbox.message_count : mailboxes.filter((item) => item.show_in_all_mail !== false).reduce((sum, item) => sum + item.message_count, 0);
  const refreshKey = mailboxRefreshKey(latestSyncRun, mailbox);
  const listKey = `${mailboxID || "all"}:${page}`;
  const listPending = loading || previousListKey.current !== listKey;
  const activeRun = mailboxActiveRun(mailbox, activeSyncRuns, latestSyncRun);
  const effectiveMode = mailbox ? effectiveMailboxSyncMode(mailbox, mailboxes) : "auto";

  useEffect(() => {
    return () => {
      if (newMessageTimer.current !== null) window.clearTimeout(newMessageTimer.current);
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    const isNewList = previousListKey.current !== listKey;
    const canAnimateNewMail = page === 1 && loaded.current && !isNewList && Boolean(refreshKey) && Boolean(latestSyncRun?.new_messages);
    if (isNewList || !loaded.current) {
      setLoading(true);
      setConversations([]);
      setHasPrev(false);
      setHasNext(false);
    }
    setError("");
    api
      .mail(mailboxID, page)
      .then((data) => {
        if (cancelled) return;
        const nextIDs = new Set(data.conversations.map((conversation) => conversation.message.id));
        if (canAnimateNewMail) {
          const appeared = data.conversations
            .map((conversation) => conversation.message.id)
            .filter((id) => !previousPageIDs.current.has(id));
          if (appeared.length > 0) {
            setNewMessageIDs(new Set(appeared));
            if (newMessageTimer.current !== null) window.clearTimeout(newMessageTimer.current);
            newMessageTimer.current = window.setTimeout(() => setNewMessageIDs(new Set()), 2200);
          }
        } else {
          setNewMessageIDs(new Set());
        }
        previousPageIDs.current = nextIDs;
        previousListKey.current = listKey;
        setConversations(data.conversations);
        setHasPrev(data.has_prev);
        setHasNext(data.has_next);
      })
      .catch((err) => {
        if (!cancelled) {
          previousPageIDs.current = new Set();
          previousListKey.current = listKey;
          setConversations([]);
          setHasPrev(false);
          setHasNext(false);
          setError(messageFromError(err));
        }
      })
      .finally(() => {
        if (!cancelled) {
          loaded.current = true;
          setLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [mailboxID, page, refreshKey, listKey, latestSyncRun?.new_messages]);

  const pageURL = (nextPage: number) => mailURL(mailboxID, nextPage);

  function updateStarred(messageID: number, starredMessageID: number, starred: boolean) {
    setConversations((current) => current.map((conversation) => {
      if (conversation.message.id !== messageID && conversation.starred_message_id !== starredMessageID) return conversation;
      return {
        ...conversation,
        starred_message_id: starred ? starredMessageID : conversation.message.id,
        message: { ...conversation.message, is_starred: starred }
      };
    }));
  }

  async function startFolderSync() {
    if (!mailbox) return;
    setSyncBusy(true);
    try {
      if (effectiveMode === "never") {
        await api.setFolderMode(csrf, mailbox.id, "manual");
      }
      await api.syncFolder(csrf, mailbox.id);
      addToast(`${mailbox.name} sync started.`);
      await refreshChrome();
    } catch (err) {
      addToast(`Sync failed: ${messageFromError(err)}`, "error");
    } finally {
      setSyncBusy(false);
    }
  }

  return (
    <>
      <ListHeader
        title={mailbox?.name || "All Mail"}
        titleClassName="mailbox-title"
        pager={{
          page,
          pageSize: mailPageSize,
          itemCount: listPending ? 0 : conversations.length,
          total: totalCount,
          hasPrev: listPending ? false : hasPrev,
          hasNext: listPending ? false : hasNext,
          pageURL,
          navigate,
          ariaLabel: "Mailbox pagination"
        }}
      />
      {mailbox ? (
        <FolderSyncNotice
          mailbox={mailbox}
          effectiveMode={effectiveMode}
          activeRun={activeRun}
          busy={syncBusy}
          onSync={startFolderSync}
        />
      ) : null}
      {error ? <div className="error">{error}</div> : null}
      {listPending ? <MessageListSkeleton label="Loading messages" /> : null}
      {!listPending && !error ? (
        <MessageList
          csrf={csrf}
          conversations={conversations}
          hiddenMessageIDs={hiddenMessageIDs}
          highlightMessageIDs={newMessageIDs}
          datePrefs={datePrefs}
          returnURL={mailURL(mailboxID, page)}
          navigate={navigate}
          addToast={addToast}
          onStarredChange={updateStarred}
        />
      ) : null}
    </>
  );
}

function FolderSyncNotice({
  mailbox,
  effectiveMode,
  activeRun,
  busy,
  onSync
}: {
  mailbox: Mailbox;
  effectiveMode: string;
  activeRun: SyncRun | null;
  busy: boolean;
  onSync: () => void;
}) {
  const syncOff = effectiveMode === "never";
  const needsSync = mailboxNeedsSync(mailbox);
  const syncing = Boolean(activeRun);
  if (!syncOff && !needsSync && !syncing) return null;

  const percent = syncPercent(mailbox);
  const progress = activeRun ? syncRunProgress(activeRun, percent) : percent;
  const title = syncing ? "Syncing this folder" : syncOff ? "Folder sync is off" : "Folder is not fully synced";
  const detail = syncing
    ? "MailMirror is updating this folder now. New rows will appear as the sync indexes messages."
    : syncOff
      ? "This folder is excluded from sync, so MailMirror may only show messages that were already mirrored."
      : `MailMirror has mirrored about ${percent}% of this folder. Some messages may not appear until it finishes.`;
  const buttonLabel = syncing ? "Syncing" : busy ? "Starting" : syncOff ? "Enable and sync" : "Sync folder";

  return (
    <section className={`folder-sync-notice ${syncing ? "running" : ""}`} aria-live="polite">
      <Icon name={syncing ? "sync" : "report"} />
      <div className="folder-sync-copy">
        <strong>{title}</strong>
        <span>{detail}</span>
        {!syncOff ? (
          <div className="folder-sync-progress" aria-label={`${title} progress`}>
            <div style={{ width: `${progress}%` }} />
          </div>
        ) : null}
      </div>
      <button className={syncOff ? "" : "secondary"} type="button" disabled={busy || syncing} onClick={onSync}>
        <Icon name="sync" />
        {buttonLabel}
      </button>
    </section>
  );
}

function syncRunProgress(run: SyncRun, fallback: number): number {
  if (run.messages_total > 0) {
    return Math.max(0, Math.min(100, Math.round((run.messages_seen / run.messages_total) * 100)));
  }
  return fallback;
}

function SearchView({
  csrf,
  location,
  navigate,
  hiddenMessageIDs,
  datePrefs,
  addToast
}: {
  csrf: string;
  location: LocationState;
  navigate: (url: string) => void;
  hiddenMessageIDs: Set<number>;
  datePrefs: DatePrefs;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [hasPrev, setHasPrev] = useState(false);
  const [hasNext, setHasNext] = useState(false);
  const loadedKey = useRef("");
  const route = searchRoute(location.path);
  const query = route.query;
  const page = route.page;
  const searchKey = `${query}:best:${page}`;
  const listPending = loading || loadedKey.current !== searchKey;

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setConversations([]);
    setHasPrev(false);
    setHasNext(false);
    setError("");
    api
      .search(query, "best", page)
      .then((data) => {
        if (cancelled) return;
        loadedKey.current = searchKey;
        setConversations(data.conversations);
        setHasPrev(data.has_prev);
        setHasNext(data.has_next);
      })
      .catch((err) => {
        if (!cancelled) {
          loadedKey.current = searchKey;
          setConversations([]);
          setHasPrev(false);
          setHasNext(false);
          setError(messageFromError(err));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [query, page, searchKey]);

  const pageURL = (nextPage: number) => searchURL(query, nextPage);
  const returnURL = routeWithSearch(location.path, location.search);

  function updateStarred(messageID: number, starredMessageID: number, starred: boolean) {
    setConversations((current) => current.map((conversation) => {
      if (conversation.message.id !== messageID && conversation.starred_message_id !== starredMessageID) return conversation;
      return {
        ...conversation,
        starred_message_id: starred ? starredMessageID : conversation.message.id,
        message: { ...conversation.message, is_starred: starred }
      };
    }));
  }

  return (
    <>
      <ListHeader
        title="Search"
        pager={{
          page,
          pageSize: mailPageSize,
          itemCount: listPending ? 0 : conversations.length,
          hasPrev: listPending ? false : hasPrev,
          hasNext: listPending ? false : hasNext,
          pageURL,
          navigate,
          ariaLabel: "Search pagination"
        }}
      />
      {query ? <div className="muted">Results for <strong>{query}</strong></div> : null}
      {error ? <div className="error">{error}</div> : null}
      {listPending ? <MessageListSkeleton label="Searching" /> : null}
      {!listPending && !error ? (
        <MessageList
          csrf={csrf}
          conversations={conversations}
          hiddenMessageIDs={hiddenMessageIDs}
          navigate={navigate}
          searchQuery={query}
          datePrefs={datePrefs}
          returnURL={returnURL}
          addToast={addToast}
          onStarredChange={updateStarred}
        />
      ) : null}
    </>
  );
}

function MessageListSkeleton({ label }: { label: string }) {
  return (
    <div className="message-table loading-list" role="status" aria-label={label} aria-busy="true">
      {Array.from({ length: 8 }, (_, index) => (
        <div className="message-row skeleton-row" key={index}>
          <span className="skeleton-block sender-skeleton" />
          <span className="skeleton-block subject-skeleton" />
          <span className="skeleton-block date-skeleton" />
        </div>
      ))}
    </div>
  );
}

function MessageList({
  csrf,
  conversations,
  hiddenMessageIDs,
  highlightMessageIDs,
  searchQuery = "",
  datePrefs,
  returnURL = "",
  navigate,
  addToast,
  onStarredChange
}: {
  csrf: string;
  conversations: Conversation[];
  hiddenMessageIDs: Set<number>;
  highlightMessageIDs?: Set<number>;
  searchQuery?: string;
  datePrefs: DatePrefs;
  returnURL?: string;
  navigate: (url: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
  onStarredChange: (messageID: number, starredMessageID: number, starred: boolean) => void;
}) {
  const [selectedIDs, setSelectedIDs] = useState<Set<number>>(() => new Set());
  const lastSelectedIndex = useRef<number | null>(null);
  const visible = conversations.filter((conversation) => !hiddenMessageIDs.has(conversation.message.id));
  const visibleKey = visible.map((conversation) => conversation.message.id).join(",");

  useEffect(() => {
    const ids = new Set(visible.map((conversation) => conversation.message.id));
    setSelectedIDs((current) => {
      const next = new Set(Array.from(current).filter((id) => ids.has(id)));
      return next.size === current.size ? current : next;
    });
  }, [visibleKey]);

  function selectedDragIDs(messageID: number): number[] {
    if (!selectedIDs.has(messageID)) return [messageID];
    const selected = visible.map((conversation) => conversation.message.id).filter((id) => selectedIDs.has(id));
    return selected.length > 0 ? selected : [messageID];
  }

  function selectMessage(event: ChangeEvent<HTMLInputElement>, index: number, messageID: number) {
    event.stopPropagation();
    const checked = event.currentTarget.checked;
    setSelectedIDs((current) => {
      const next = new Set(current);
      if ((event.nativeEvent as Event & { shiftKey?: boolean }).shiftKey && lastSelectedIndex.current !== null) {
        const start = Math.min(lastSelectedIndex.current, index);
        const end = Math.max(lastSelectedIndex.current, index);
        for (const conversation of visible.slice(start, end + 1)) {
          if (checked) next.add(conversation.message.id);
          else next.delete(conversation.message.id);
        }
      } else if (checked) {
        next.add(messageID);
      } else {
        next.delete(messageID);
      }
      return next;
    });
    lastSelectedIndex.current = index;
  }

  async function toggleStar(event: MouseEvent<HTMLButtonElement>, conversation: Conversation) {
    event.preventDefault();
    event.stopPropagation();
    const msg = conversation.message;
    const targetID = conversation.starred_message_id || msg.id;
    const next = !msg.is_starred;
    onStarredChange(msg.id, targetID, next);
    try {
      await api.setStarred(csrf, targetID, next);
    } catch (err) {
      onStarredChange(msg.id, targetID, msg.is_starred);
      addToast(`Star update failed: ${messageFromError(err)}`, "error");
    }
  }

  function openRow(event: MouseEvent<HTMLDivElement>, href: string) {
    if ((event.target as HTMLElement).closest("button,input,label")) return;
    navigate(href);
  }

  function openRowWithKeyboard(event: KeyboardEvent<HTMLDivElement>, href: string) {
    if (event.currentTarget !== event.target) return;
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      navigate(href);
    }
  }

  if (visible.length === 0) {
    return <div className="panel muted">No messages here.</div>;
  }
  return (
    <div className="message-table">
      {visible.map((conversation, index) => {
        const msg = conversation.message;
        const matchTerms = conversation.match_terms || [];
        const href = messageURL(msg.id, searchQuery, matchTerms, returnURL);
        const attachmentNames = conversation.attachment_names || [];
        const attachmentMatches = conversation.attachment_matches || [];
        const selected = selectedIDs.has(msg.id);
        return (
          <div
            className={`message-row ${conversation.is_read ? "read" : "unread"} ${selected ? "selected" : ""} ${highlightMessageIDs?.has(msg.id) ? "new-delivery" : ""}`}
            draggable
            key={msg.id}
            role="link"
            tabIndex={0}
            onClick={(event) => openRow(event, href)}
            onKeyDown={(event) => openRowWithKeyboard(event, href)}
            onDragStart={(event) => {
              const ids = selectedDragIDs(msg.id);
              event.dataTransfer.effectAllowed = "move";
              event.dataTransfer.setData("application/x-mailmirror-messages", JSON.stringify(ids));
              event.dataTransfer.setData("application/x-mailmirror-message", String(ids[0]));
              event.dataTransfer.setData("text/plain", String(ids[0]));
            }}
          >
            <label className="message-select" onClick={(event) => event.stopPropagation()} title="Select message">
              <input
                type="checkbox"
                checked={selected}
                aria-label={`Select ${msg.subject || "message"}`}
                onChange={(event) => selectMessage(event, index, msg.id)}
              />
            </label>
            <button
              className={`star-action ${msg.is_starred ? "starred" : ""}`}
              type="button"
              aria-pressed={msg.is_starred}
              title={msg.is_starred ? "Unstar" : "Star"}
              onClick={(event) => void toggleStar(event, conversation)}
            >
              <Star className="icon" weight={msg.is_starred ? "fill" : "regular"} />
            </button>
            <span className="sender">
              <span className="sender-name">
                <HighlightedText text={conversation.participants || msg.from_addr || "Unknown sender"} query={searchQuery} terms={matchTerms} />
              </span>
              {conversation.count > 1 ? <span className="thread-count">({conversation.count})</span> : null}
            </span>
            <span className="subject">
              <strong>
                <HighlightedText text={msg.subject || "(no subject)"} query={searchQuery} terms={matchTerms} />
              </strong>
              <span className="snippet">
                <HighlightedText text={conversation.snippet} query={searchQuery} terms={matchTerms} />
              </span>
              {attachmentNames.length > 0 ? (
                <span className={`attachment-preview ${attachmentMatches.length > 0 || conversation.attachment_content_matched ? "matched" : ""}`}>
                  <Icon name="attach_file" />
                  <HighlightedText
                    text={attachmentMatches.length > 0 ? attachmentMatches.join(", ") : attachmentNames.join(", ")}
                    query={searchQuery}
                    terms={matchTerms}
                  />
                  {conversation.attachment_content_matched ? <span>content matched</span> : null}
                </span>
              ) : conversation.has_attachments ? <Icon name="attach_file" /> : null}
            </span>
            <span className="date">{displayTime(msg.date, datePrefs)}</span>
          </div>
        );
      })}
    </div>
  );
}

function senderDomain(value: string): string {
  const match = String(value || "").match(/@([^>\s,;]+)/);
  if (!match) return "";
  return match[1]
    .toLowerCase()
    .replace(/[)"'.,;:]+$/g, "")
    .split("/")
    .shift() || "";
}

function MessageDetailsToggle({
  summary,
  details,
  highlightQuery,
  highlightTerms
}: {
  summary: string;
  details: HeaderDetail[];
  highlightQuery: string;
  highlightTerms: string[];
}) {
  const visibleDetails = details.filter((detail) => detail.value.trim() !== "");
  if (visibleDetails.length === 0) {
    return (
      <div className="thread-recipients">
        <HighlightedText text={summary} query={highlightQuery} terms={highlightTerms} />
      </div>
    );
  }
  return (
    <details className="thread-recipients message-details" onClick={(event) => event.stopPropagation()}>
      <summary>
        <span>
          <HighlightedText text={summary} query={highlightQuery} terms={highlightTerms} />
        </span>
        <Icon name="expand_more" />
      </summary>
      <dl>
        {visibleDetails.map((detail) => (
          <Fragment key={`${detail.label}:${detail.value}`}>
            <dt>{detail.label}</dt>
            <dd>
              <HighlightedText text={detail.value} query={highlightQuery} terms={highlightTerms} />
            </dd>
          </Fragment>
        ))}
      </dl>
    </details>
  );
}

type RangePagerProps = {
  page: number;
  pageSize: number;
  itemCount: number;
  total?: number;
  hasPrev: boolean;
  hasNext: boolean;
  pageURL: (page: number) => string;
  navigate: (url: string) => void;
  ariaLabel: string;
};

function ListHeader({
  title,
  titleClassName = "",
  actions,
  pager
}: {
  title: ReactNode;
  titleClassName?: string;
  actions?: ReactNode;
  pager: RangePagerProps;
}) {
  return (
    <div className="content-head list-head">
      <div className="list-head-main">
        <h1 className={titleClassName}>{title}</h1>
        {actions}
      </div>
      <RangePager {...pager} />
    </div>
  );
}

function RangePager({
  page,
  pageSize,
  itemCount,
  total,
  hasPrev,
  hasNext,
  pageURL,
  navigate,
  ariaLabel
}: RangePagerProps) {
  const start = itemCount > 0 || hasNext ? (page - 1) * pageSize + 1 : 0;
  const end = itemCount > 0 ? (page - 1) * pageSize + itemCount : start > 0 ? page * pageSize : 0;
  const cappedEnd = total && total > 0 ? Math.min(end, total) : end;
  const label = start > 0
    ? `${start.toLocaleString()}-${cappedEnd.toLocaleString()}${total && total > 0 ? ` of ${total.toLocaleString()}` : hasNext ? " of many" : ""}`
    : total && total > 0 ? `0 of ${total.toLocaleString()}` : "0";

  return (
    <div className="range-pager" aria-label={ariaLabel}>
      <span>{label}</span>
      <button className="range-pager-button" type="button" disabled={!hasPrev} onClick={() => navigate(pageURL(page - 1))} title="Previous page">
        <Icon name="chevron_left" />
      </button>
      <button className="range-pager-button" type="button" disabled={!hasNext} onClick={() => navigate(pageURL(page + 1))} title="Next page">
        <Icon name="chevron_right" />
      </button>
    </div>
  );
}

function ThreadView({
  csrf,
  datePrefs,
  location,
  navigate,
  mailboxes,
  refreshChrome,
  openCompose,
  addToast
}: {
  csrf: string;
  datePrefs: DatePrefs;
  location: LocationState;
  navigate: (url: string) => void;
  mailboxes: Mailbox[];
  refreshChrome: () => Promise<Bootstrap | null>;
  openCompose: (query?: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const id = location.path.split("/").pop() || "";
  const highlightQuery = messageHighlightQuery(location);
  const highlightTerms = messageHighlightTerms(location);
  const [thread, setThread] = useState<ThreadMessage[]>([]);
  const [subject, setSubject] = useState("");
  const [mailboxID, setMailboxID] = useState<number | null>(null);
  const [composeFrom, setComposeFrom] = useState("");
  const [showImages, setShowImages] = useState(() => new URLSearchParams(location.search).get("images") === "1");
  const [expanded, setExpanded] = useState<Set<number>>(() => new Set());
  const [inlineReply, setInlineReply] = useState<ComposeForm | null>(null);
  const [unsubscribingID, setUnsubscribingID] = useState<number | null>(null);
  const [pendingUnsubscribe, setPendingUnsubscribe] = useState<ThreadMessage | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const mailbox = mailboxID ? mailboxes.find((item) => item.id === mailboxID) : null;
  const backURL = messageBackURL(location);
  const brandDomainKey = thread
    .map((item) => senderDomain(item.sender_email || item.message.from_addr))
    .filter(Boolean)
    .filter((domain, index, domains) => domains.indexOf(domain) === index)
    .join(",");
  const [brandIcons, setBrandIcons] = useState<Record<string, string>>({});

  const load = useCallback(
    async (images: boolean) => {
      setLoading(true);
      setError("");
      try {
        const data = await api.message(id, images, highlightQuery);
        setThread(data.thread);
        setSubject(data.message.subject || "(no subject)");
        setMailboxID(data.mailbox_id);
        setComposeFrom(data.compose_from);
        setExpanded(new Set(data.thread.filter((item) => item.expanded).map((item) => item.message.id)));
        void refreshChrome();
      } catch (err) {
        setError(messageFromError(err));
      } finally {
        setLoading(false);
      }
    },
    [highlightQuery, id, refreshChrome]
  );

  useEffect(() => {
    void load(showImages);
  }, [load, showImages]);

  useEffect(() => {
    if (!brandDomainKey) {
      setBrandIcons({});
      return;
    }
    let cancelled = false;
    const domains = brandDomainKey.split(",").filter(Boolean);
    api
      .brandIcons(domains)
      .then((data) => {
        if (!cancelled) setBrandIcons(data.icons || {});
      })
      .catch(() => {
        if (!cancelled) setBrandIcons({});
      });
    return () => {
      cancelled = true;
    };
  }, [brandDomainKey]);

  async function trustImages(messageID: number) {
    try {
      await api.trustImages(csrf, messageID);
      addToast("Remote images will be shown for this sender.");
      setShowImages(true);
      await load(true);
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function unsubscribe(item: ThreadMessage) {
    setUnsubscribingID(item.message.id);
    try {
      const data = await api.unsubscribe(csrf, item.message.id);
      const sentAt = data.sent_at || new Date().toISOString();
      setThread((current) => current.map((threadItem) =>
        threadItem.message.id === item.message.id
          ? { ...threadItem, one_click_unsubscribe_sent_at: sentAt }
          : threadItem
      ));
      addToast(data.already_sent ? `Unsubscribed on ${displayDateTime(sentAt, datePrefs)}.` : "One-click unsubscribe request sent.");
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setUnsubscribingID(null);
    }
  }

  function requestUnsubscribe(item: ThreadMessage) {
    if (item.one_click_unsubscribe_sent_at) return;
    setPendingUnsubscribe(item);
  }

  async function confirmUnsubscribe() {
    if (!pendingUnsubscribe) return;
    const item = pendingUnsubscribe;
    setPendingUnsubscribe(null);
    await unsubscribe(item);
  }

  function unsubscribeSentLabel(item: ThreadMessage) {
    if (!item.one_click_unsubscribe_sent_at) return "";
    return `Unsubscribed on ${displayDateTime(item.one_click_unsubscribe_sent_at, datePrefs)}`;
  }

  function beginReply(item: ThreadMessage) {
    setExpanded((current) => new Set(current).add(item.message.id));
    setInlineReply({
      ...emptyCompose,
      to: item.sender_email || item.message.from_addr,
      subject: item.reply_subject || `Re: ${item.message.subject}`,
      in_reply_to_id: item.message.id
    });
  }

  function toggleMessage(messageID: number) {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(messageID)) next.delete(messageID);
      else next.add(messageID);
      return next;
    });
  }

  async function toggleThreadStar(event: MouseEvent<HTMLButtonElement>, item: ThreadMessage) {
    event.stopPropagation();
    const next = !item.message.is_starred;
    setThread((current) => current.map((threadItem) => threadItem.message.id === item.message.id
      ? { ...threadItem, message: { ...threadItem.message, is_starred: next } }
      : threadItem));
    try {
      await api.setStarred(csrf, item.message.id, next);
    } catch (err) {
      setThread((current) => current.map((threadItem) => threadItem.message.id === item.message.id
        ? { ...threadItem, message: { ...threadItem.message, is_starred: item.message.is_starred } }
        : threadItem));
      addToast(`Star update failed: ${messageFromError(err)}`, "error");
    }
  }

  return (
    <>
      <div className="content-head">
        <div>
          <button className="ghost" type="button" onClick={() => navigate(backURL)} title="Back to results">
            <Icon name="arrow_back" />
          </button>
          <h1 className="thread-title">
            <HighlightedText text={subject} query={highlightQuery} terms={highlightTerms} />
          </h1>
          {mailbox ? <span className="label-pill">{mailbox.name}</span> : null}
        </div>
      </div>
      {error ? <div className="error">{error}</div> : null}
      {loading ? <div className="panel muted">Loading conversation...</div> : null}
      {pendingUnsubscribe ? (
        <div className="confirm-backdrop" role="presentation" onClick={() => setPendingUnsubscribe(null)}>
          <section
            className="confirm-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="unsubscribe-confirm-title"
            onClick={(event) => event.stopPropagation()}
          >
            <h2 id="unsubscribe-confirm-title">Unsubscribe?</h2>
            <p>
              Send a one-click unsubscribe request for {pendingUnsubscribe.sender_name || pendingUnsubscribe.sender_email || "this sender"}?
            </p>
            <div className="actions">
              <button className="secondary" type="button" onClick={() => setPendingUnsubscribe(null)}>No</button>
              <button type="button" onClick={() => void confirmUnsubscribe()}>Yes, unsubscribe</button>
            </div>
          </section>
        </div>
      ) : null}
      {!loading ? (
        <section className="thread-shell">
          {thread.map((item, index) => {
            const isExpanded = expanded.has(item.message.id);
            const domain = senderDomain(item.sender_email || item.message.from_addr);
            const brandIcon = domain ? brandIcons[domain] : "";
            const unsubscribeSent = unsubscribeSentLabel(item);
            return (
              <article className={`thread-card ${isExpanded ? "" : "collapsed"}`} key={item.message.id}>
                <div
                  className="thread-summary"
                  role="button"
                  tabIndex={0}
                  aria-expanded={isExpanded}
                  onClick={() => toggleMessage(item.message.id)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      toggleMessage(item.message.id);
                    }
                  }}
                >
                  {brandIcon ? (
                    <img className="thread-brand-icon" src={brandIcon} alt="" loading="lazy" />
                  ) : (
                    <div className="avatar">{item.sender_initial}</div>
                  )}
                  <div className="thread-person">
                    <div className="thread-from">
                      <span>
                        <HighlightedText text={item.sender_name || item.sender_email || "Unknown sender"} query={highlightQuery} terms={highlightTerms} />
                      </span>
                      <span className="thread-email">
                        <HighlightedText text={item.sender_email} query={highlightQuery} terms={highlightTerms} />
                      </span>
                      {item.one_click_unsubscribe ? (
                        <button
                          className={`unsubscribe-action ${unsubscribeSent ? "sent" : ""}`}
                          type="button"
                          disabled={unsubscribingID === item.message.id || Boolean(unsubscribeSent)}
                          title={unsubscribeSent || "Unsubscribe"}
                          onClick={(event) => {
                            event.stopPropagation();
                            requestUnsubscribe(item);
                          }}
                        >
                          {unsubscribingID === item.message.id ? "Unsubscribing" : unsubscribeSent || "Unsubscribe"}
                        </button>
                      ) : null}
                    </div>
                    <MessageDetailsToggle
                      summary={item.recipient_line}
                      details={item.header_details || []}
                      highlightQuery={highlightQuery}
                      highlightTerms={highlightTerms}
                    />
                    <div className="thread-collapsed-snippet">
                      <HighlightedText text={item.snippet} query={highlightQuery} terms={highlightTerms} />
                    </div>
                  </div>
                  <div className="thread-meta">
                    <span>{displayTime(item.message.date, datePrefs)}</span>
                    <button
                      className={`star-action thread-star ${item.message.is_starred ? "starred" : ""}`}
                      type="button"
                      aria-pressed={item.message.is_starred}
                      title={item.message.is_starred ? "Unstar" : "Star"}
                      onClick={(event) => void toggleThreadStar(event, item)}
                    >
                      <Star className="icon" weight={item.message.is_starred ? "fill" : "regular"} />
                    </button>
                    <details className="message-menu" onClick={(event) => event.stopPropagation()}>
                      <summary className="icon-action" title="Message actions" aria-label="Message actions">
                        <Icon name="more_vert" />
                      </summary>
                      <div className="message-menu-panel">
                        <button type="button" onClick={() => beginReply(item)}>
                          <Icon name="reply" />
                          Reply
                        </button>
                        <button type="button" onClick={() => openCompose(`forward=${item.message.id}`)}>
                          <Icon name="forward" />
                          Forward
                        </button>
                        {item.one_click_unsubscribe ? (
                          <button type="button" disabled={unsubscribingID === item.message.id || Boolean(unsubscribeSent)} onClick={() => requestUnsubscribe(item)}>
                            <Icon name="close" />
                            {unsubscribeSent || "Unsubscribe"}
                          </button>
                        ) : null}
                      </div>
                    </details>
                  </div>
                </div>
                {item.has_remote_images && !item.images_allowed ? (
                  <div className="image-notice">
                    <Icon name="image" />
                    <span>Remote images are blocked for this sender.</span>
                    <button className="secondary" type="button" onClick={() => setShowImages(true)}>Show images</button>
                    <button className="secondary" type="button" onClick={() => trustImages(item.message.id)}>
                      Always show from this sender
                    </button>
                  </div>
                ) : null}
                <div className="thread-body">
                  {item.body_preview_only ? (
                    <div className="body-notice">
                      <Icon name="report" />
                      <span>Showing the indexed preview only. MailMirror could not fetch the full original from IMAP.</span>
                      <button className="secondary" type="button" onClick={() => navigate("/settings/account")}>Account settings</button>
                    </div>
                  ) : null}
                  <EmailFrame srcDoc={item.body_doc} highlightQuery={highlightQuery} highlightTerms={highlightTerms} />
                  {item.has_hidden_quoted && item.full_body_doc ? (
                    <details className="quoted-details">
                      <summary>...</summary>
                      <EmailFrame srcDoc={item.full_body_doc} highlightQuery={highlightQuery} highlightTerms={highlightTerms} full />
                    </details>
                  ) : null}
                </div>
                {item.attachments.length > 0 ? (
                  <div className="attachments">
                    {item.attachments.map((attachment) => (
                      <a
                        className={`attachment ${attachment.matched ? "matched" : ""}`}
                        href={attachment.download_url}
                        download={attachment.filename || "attachment"}
                        key={attachment.id}
                      >
                        <Icon name="attach_file" />
                        <HighlightedText
                          text={attachment.filename || "Attachment"}
                          query={highlightQuery}
                          terms={highlightTerms}
                        />
                      </a>
                    ))}
                  </div>
                ) : null}
                {index === thread.length - 1 ? (
                  <div className="thread-actions">
                    <button className="thread-action" type="button" onClick={() => beginReply(item)}>
                      <Icon name="reply" weight="bold" />
                      Reply
                    </button>
                    <button
                      className="thread-action"
                      type="button"
                      onClick={() => openCompose(`forward=${item.message.id}`)}
                    >
                      <Icon name="forward" weight="bold" />
                      Forward
                    </button>
                  </div>
                ) : null}
                {inlineReply && inlineReply.in_reply_to_id === item.message.id ? (
                  <ComposeBox
                    csrf={csrf}
                    composeFrom={composeFrom}
                    initial={inlineReply}
                    inline
                    addToast={addToast}
                    onSent={() => {
                      setInlineReply(null);
                      void load(showImages);
                    }}
                    onCancel={() => setInlineReply(null)}
                  />
                ) : null}
                {index === thread.length - 1 && !inlineReply ? <div className="thread-tail" /> : null}
              </article>
            );
          })}
        </section>
      ) : null}
    </>
  );
}

function EmailFrame({
  srcDoc,
  highlightQuery = "",
  highlightTerms = [],
  full = false
}: {
  srcDoc: string;
  highlightQuery?: string;
  highlightTerms?: string[];
  full?: boolean;
}) {
  const ref = useRef<HTMLIFrameElement | null>(null);
  const [height, setHeight] = useState(full ? 220 : 96);
  const highlightKey = `${highlightQuery}:${highlightTerms.join(",")}`;

  useEffect(() => {
    setHeight(full ? 220 : 96);
  }, [srcDoc, highlightKey, full]);

  function resize() {
    const doc = ref.current?.contentDocument;
    const body = doc?.body;
    const html = doc?.documentElement;
    if (!body || !html) return;
    const next = Math.max(body.scrollHeight, body.offsetHeight, html.scrollHeight, html.offsetHeight, full ? 180 : 84) + 12;
    setHeight(next);
  }

  return (
    <iframe
      ref={ref}
      className={`email-frame ${full ? "full" : ""}`}
      srcDoc={srcDoc}
      title="Email body"
      sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
      scrolling="no"
      style={{ height }}
      onLoad={() => {
        highlightEmailDocument(ref.current?.contentDocument, highlightQuery, highlightTerms);
        resize();
        window.requestAnimationFrame(resize);
        window.setTimeout(resize, 120);
        window.setTimeout(resize, 600);
        window.setTimeout(resize, 1600);
      }}
    />
  );
}

function ComposeOverlay({
  csrf,
  query,
  addToast,
  onClose
}: {
  csrf: string;
  query: string;
  addToast: (message: string, kind?: Toast["kind"]) => number;
  onClose: () => void;
}) {
  const [form, setForm] = useState<ComposeForm | null>(null);
  const [from, setFrom] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    setForm(null);
    setError("");
    api
      .compose(query)
      .then((data) => {
        if (cancelled) return;
        setForm(data.compose);
        setFrom(data.compose_from);
      })
      .catch((err) => {
        if (!cancelled) setError(messageFromError(err));
      });
    return () => {
      cancelled = true;
    };
  }, [query]);

  return (
    <div className="compose-popover" role="dialog" aria-label="Compose message">
      {error ? <div className="error">{error}</div> : null}
      {form ? (
        <ComposeBox
          csrf={csrf}
          composeFrom={from}
          initial={form}
          addToast={addToast}
          onSent={onClose}
          onCancel={onClose}
        />
      ) : (
        <div className="compose-window compose-loading">
          <div className="compose-head">
            <span>New Message</span>
            <button className="ghost" type="button" title="Close" onClick={onClose}>
              <Icon name="close" />
            </button>
          </div>
          <div className="panel muted">Loading compose...</div>
        </div>
      )}
    </div>
  );
}

function ComposePage({
  csrf,
  location,
  navigate,
  addToast
}: {
  csrf: string;
  location: LocationState;
  navigate: (url: string) => void;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [form, setForm] = useState<ComposeForm | null>(null);
  const [from, setFrom] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    api
      .compose(location.search.replace(/^\?/, ""))
      .then((data) => {
        if (cancelled) return;
        setForm(data.compose);
        setFrom(data.compose_from);
      })
      .catch((err) => {
        if (!cancelled) setError(messageFromError(err));
      });
    return () => {
      cancelled = true;
    };
  }, [location.search]);

  return (
    <div className="compose-page">
      {error ? <div className="error">{error}</div> : null}
      {form ? (
        <ComposeBox
          csrf={csrf}
          composeFrom={from}
          initial={form}
          addToast={addToast}
          onSent={() => navigate("/mail")}
          onCancel={() => navigate("/mail")}
        />
      ) : (
        <div className="panel muted">Loading compose...</div>
      )}
    </div>
  );
}

function ComposeBox({
  csrf,
  composeFrom,
  initial,
  inline = false,
  addToast,
  onSent,
  onCancel
}: {
  csrf: string;
  composeFrom: string;
  initial: ComposeForm;
  inline?: boolean;
  addToast: (message: string, kind?: Toast["kind"]) => number;
  onSent: () => void;
  onCancel?: () => void;
}) {
  const [form, setForm] = useState<ComposeForm>(initial);
  const [showCc, setShowCc] = useState(Boolean(initial.cc));
  const [showBcc, setShowBcc] = useState(Boolean(initial.bcc));
  const [sending, setSending] = useState(false);
  const editorRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    setForm(initial);
    setShowCc(Boolean(initial.cc));
    setShowBcc(Boolean(initial.bcc));
    if (editorRef.current) {
      editorRef.current.innerHTML = initial.body_html || textToHTML(initial.body);
    }
  }, [initial]);

  function setField<K extends keyof ComposeForm>(field: K, value: ComposeForm[K]) {
    setForm((current) => ({ ...current, [field]: value }));
  }

  function applyFormat(command: string, value?: string) {
    editorRef.current?.focus();
    document.execCommand(command, false, value);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const editor = editorRef.current;
    const nextForm: ComposeForm = {
      ...form,
      body: editor?.innerText || "",
      body_html: editor?.innerHTML || ""
    };
    setSending(true);
    try {
      await api.send(csrf, nextForm);
      addToast("Message sent.");
      onSent();
    } catch (err) {
      addToast(messageFromError(err), "error");
    } finally {
      setSending(false);
    }
  }

  return (
    <form className={inline ? "inline-reply" : "compose-window"} onSubmit={submit}>
      {!inline ? (
        <div className="compose-head">
          <span>New Message</span>
          <div className="compose-head-actions">
            <button className="ghost" type="button" title="Minimize" onClick={onCancel}>
              <Icon name="minimize" />
            </button>
            <button className="ghost" type="button" title="Close" onClick={onCancel}>
              <Icon name="close" />
            </button>
          </div>
        </div>
      ) : null}
      {!inline ? (
        <div className="compose-fields">
          <div className="compose-line">
            <span>From</span>
            <strong>{composeFrom}</strong>
          </div>
          <div className="compose-line">
            <span>To</span>
            <input value={form.to} onChange={(event) => setField("to", event.target.value)} required />
            <button className="ghost text-link" type="button" onClick={() => setShowCc((value) => !value)}>Cc</button>
            <button className="ghost text-link" type="button" onClick={() => setShowBcc((value) => !value)}>Bcc</button>
          </div>
          {showCc ? (
            <div className="compose-line">
              <span>Cc</span>
              <input value={form.cc} onChange={(event) => setField("cc", event.target.value)} />
            </div>
          ) : null}
          {showBcc ? (
            <div className="compose-line">
              <span>Bcc</span>
              <input value={form.bcc} onChange={(event) => setField("bcc", event.target.value)} />
            </div>
          ) : null}
          <div className="compose-line">
            <input
              placeholder="Subject"
              value={form.subject}
              onChange={(event) => setField("subject", event.target.value)}
              required
            />
          </div>
        </div>
      ) : (
        <div className="inline-reply-meta">
          <span>To</span>
          <strong>{form.to}</strong>
          <button className="ghost text-link" type="button" onClick={() => setShowCc((value) => !value)}>Cc</button>
          <button className="ghost text-link" type="button" onClick={() => setShowBcc((value) => !value)}>Bcc</button>
          <button className="ghost inline-close" type="button" title="Discard reply" onClick={onCancel}>
            <Icon name="close" />
          </button>
        </div>
      )}
      {inline && showCc ? (
        <div className="inline-reply-meta">
          <span>Cc</span>
          <input value={form.cc} onChange={(event) => setField("cc", event.target.value)} />
        </div>
      ) : null}
      {inline && showBcc ? (
        <div className="inline-reply-meta">
          <span>Bcc</span>
          <input value={form.bcc} onChange={(event) => setField("bcc", event.target.value)} />
        </div>
      ) : null}
      <div className="compose-body">
        <div
          ref={editorRef}
          className="compose-editor"
          contentEditable
          data-placeholder="Write a message"
          suppressContentEditableWarning
        />
      </div>
      <div className="compose-format" aria-label="Formatting">
        <button type="button" title="Bold" onClick={() => applyFormat("bold")}>B</button>
        <button type="button" title="Italic" onClick={() => applyFormat("italic")}><em>I</em></button>
        <button type="button" title="Underline" onClick={() => applyFormat("underline")}><u>U</u></button>
        <button type="button" title="Text color" onClick={() => applyFormat("foreColor", "#d95f3d")}>
          <Icon name="format_color_text" />
        </button>
        <button type="button" title="Bulleted list" onClick={() => applyFormat("insertUnorderedList")}>
          <Icon name="format_list_bulleted" />
        </button>
        <button type="button" title="Numbered list" onClick={() => applyFormat("insertOrderedList")}>
          <Icon name="format_list_numbered" />
        </button>
        <button type="button" title="Quote" onClick={() => applyFormat("formatBlock", "blockquote")}>
          <Icon name="format_quote" />
        </button>
      </div>
      <div className="compose-sendbar">
        <button className="send-button" disabled={sending}>
          {sending ? "Sending..." : "Send"}
        </button>
        <button className="ghost" type="button" title="Discard" onClick={onCancel}>
          <Icon name="delete" />
        </button>
      </div>
    </form>
  );
}

function SettingsView({
  csrf,
  user,
  mailboxes,
  activeSyncRuns,
  refreshChrome,
  addToast
}: {
  csrf: string;
  user: User;
  mailboxes: Mailbox[];
  activeSyncRuns: SyncRun[];
  refreshChrome: () => Promise<Bootstrap | null>;
  addToast: (message: string, kind?: Toast["kind"]) => number;
}) {
  const [account, setAccount] = useState<Account | null>(null);
  const [runs, setRuns] = useState<SyncRun[]>([]);
  const [folders, setFolders] = useState<SyncFolder[]>([]);
  const [storage, setStorage] = useState<StorageStats>({});
  const [storageLoading, setStorageLoading] = useState(true);
  const [storageError, setStorageError] = useState("");
  const [notice, setNotice] = useState("");
  const [accountNeedsPassword, setAccountNeedsPassword] = useState(false);
  const [form, setForm] = useState(() => emptyAccountForm());
  const [profileForm, setProfileForm] = useState(() => ({
    date_locale: user.date_locale || "",
    date_format: user.date_format || "mdy"
  }));
  const [remoteImageBlocklist, setRemoteImageBlocklist] = useState("");
  const [remoteImageBlocklistLoading, setRemoteImageBlocklistLoading] = useState(false);
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

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const data = await api.account();
      setAccount(data.account);
      setRuns(data.sync_runs);
      setFolders(data.sync_folders);
      setNotice(data.notice);
      setAccountNeedsPassword(Boolean(data.account_needs_password));
      setForm(accountToForm(data.account));
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
  }, [loadStorage]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    setProfileForm({
      date_locale: user.date_locale || "",
      date_format: user.date_format || "mdy"
    });
  }, [user.date_locale, user.date_format]);

  useEffect(() => {
    if (!user.is_admin) return;
    let cancelled = false;
    setRemoteImageBlocklistLoading(true);
    api
      .remoteImageBlocklist()
      .then((data) => {
        if (!cancelled) setRemoteImageBlocklist((data.patterns || []).join("\n"));
      })
      .catch((err) => {
        if (!cancelled) addToast(messageFromError(err), "error");
      })
      .finally(() => {
        if (!cancelled) setRemoteImageBlocklistLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [addToast, user.is_admin]);

  useEffect(() => {
    setFolders((current) => current.map((folder) => {
      const mailbox = mailboxes.find((item) => item.id === folder.mailbox.id) || folder.mailbox;
      const activeRun = activeSyncRuns.find((run) =>
        run.status === "running" &&
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

  function setField(field: string, value: string | boolean) {
    setForm((current) => {
      const next = { ...current, [field]: value };
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

  async function save(event: FormEvent) {
    event.preventDefault();
    try {
      await api.saveAccount(csrf, {
        email: form.email,
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
      addToast("Account settings saved.");
      await load();
      await refreshChrome();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
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

  async function saveRemoteImageBlocklist(event: FormEvent) {
    event.preventDefault();
    const patterns = remoteImageBlocklist
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith("#"));
    try {
      const data = await api.saveRemoteImageBlocklist(csrf, patterns);
      setRemoteImageBlocklist((data.patterns || []).join("\n"));
      addToast("Remote image blocklist saved.");
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

  async function setMode(folder: SyncFolder, mode: string) {
    try {
      await api.setFolderMode(csrf, folder.mailbox.id, mode);
      addToast(`${folder.mailbox.name} set to ${mode}.`);
      await load();
      await refreshChrome();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  async function saveFolderSettings(folder: SyncFolder, patch: Partial<Mailbox>) {
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
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
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
    try {
      await api.rebuildFolderIndex(csrf, folder.mailbox.id);
      addToast(`${folder.mailbox.name} index rebuild started.`);
      await refreshChrome();
      await load();
    } catch (err) {
      addToast(messageFromError(err), "error");
    }
  }

  const folderMap = useMemo(() => new Map(folders.map((folder) => [folder.mailbox.id, folder])), [folders]);
  const folderNodes = useMemo(() => folderTree(folders.map((folder) => folder.mailbox)), [folders]);

  function renderFolderRows(nodes: FolderNode[], depth = 0): ReactNode[] {
    return nodes.flatMap((node) => {
      const folder = folderMap.get(node.mailbox.id);
      if (!folder) return [];
      const rows: ReactNode[] = [
        <tr key={folder.mailbox.id}>
          <td>
            <div className="folder-settings-name" style={depth > 0 ? { paddingLeft: `${depth * 18}px` } : undefined}>
              <Icon name={folder.mailbox.icon || "folder"} />
              <div>
                <strong>{node.label}</strong>
                <div className="muted">{folder.mailbox.account_email || "Mail account"} · {folder.mailbox.name}</div>
              </div>
            </div>
          </td>
          <td>
            <div className="sync-percent">
              <div><span style={{ width: `${Math.max(0, Math.min(100, folder.mailbox.sync_percent || 0))}%` }} /></div>
              <small>{folder.mailbox.sync_percent || 0}%</small>
            </div>
          </td>
          <td>{folder.mailbox.message_count.toLocaleString()}</td>
          <td>{folder.mailbox.unread_count.toLocaleString()}</td>
          <td>
            <select value={folder.mailbox.sync_mode} onChange={(event) => saveFolderSettings(folder, { sync_mode: event.target.value })}>
              <option value="inherit">Inherit</option>
              <option value="auto">Auto</option>
              <option value="manual">Manual</option>
              <option value="never">Never</option>
            </select>
          </td>
          <td>
            <select value={folder.mailbox.role || ""} onChange={(event) => saveFolderSettings(folder, { role: event.target.value })}>
              <option value="">Normal</option>
              <option value="inbox">Inbox</option>
              <option value="trash">Trash</option>
            </select>
          </td>
          <td>
            <select value={folder.mailbox.icon || "folder"} onChange={(event) => saveFolderSettings(folder, { icon: event.target.value })}>
              <option value="folder">Folder</option>
              <option value="inbox">Inbox</option>
              <option value="archive">Archive</option>
              <option value="send">Sent</option>
              <option value="draft">Draft</option>
              <option value="delete">Trash</option>
              <option value="label">Label</option>
              <option value="shopping_bag">Purchases</option>
              <option value="report">Spam</option>
            </select>
          </td>
          <td className="folder-toggles">
            <label><input type="checkbox" checked={folder.mailbox.show_in_sidebar} onChange={(event) => saveFolderSettings(folder, { show_in_sidebar: event.target.checked })} /> Sidebar</label>
            <label><input type="checkbox" checked={folder.mailbox.show_in_all_mail} onChange={(event) => saveFolderSettings(folder, { show_in_all_mail: event.target.checked })} /> All Mail</label>
            <label><input type="checkbox" checked={folder.mailbox.include_in_search} onChange={(event) => saveFolderSettings(folder, { include_in_search: event.target.checked })} /> Search</label>
          </td>
          <td>{folder.last_run ? `${folder.last_run.status} ${displayDateTime(folder.last_run.updated_at, user)}` : "Never"}</td>
          <td>
            <div className="folder-actions">
            <button
              className="secondary"
              type="button"
              disabled={!folder.can_sync_now}
              onClick={() => syncFolder(folder)}
            >
              Sync now
            </button>
            <button
              className="secondary"
              type="button"
              disabled={folder.is_running || folder.mailbox.message_count === 0}
              onClick={() => rebuildFolderIndex(folder)}
            >
              Rebuild index
            </button>
            </div>
          </td>
        </tr>
      ];
      return rows.concat(renderFolderRows(node.children, depth + 1));
    });
  }

  return (
    <>
      <div className="content-head">
        <h1>Settings</h1>
        <button type="button" onClick={syncNow}><Icon name="sync" />Sync now</button>
      </div>
      {loading ? <div className="panel muted">Loading settings...</div> : null}
      {notice ? <div className="notice">{notice}</div> : null}
      <form className="panel account-settings" onSubmit={save}>
        <h2>Mail account</h2>
        {account ? <div className="muted">Configured for {account.email}</div> : null}
        <div className="settings-columns">
          <section>
            <h3>IMAP server</h3>
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
            <h3>SMTP server</h3>
            <label><input type="checkbox" checked={form.smtp_same_as_imap} onChange={(event) => setField("smtp_same_as_imap", event.target.checked)} /> Same as IMAP credentials</label>
            <Field label="Host" value={form.smtp_same_as_imap ? form.host : form.smtp_host} onChange={(value) => setField("smtp_host", value)} disabled={form.smtp_same_as_imap} />
            <Field label="Port" value={form.smtp_port} onChange={(value) => setField("smtp_port", value)} type="number" />
            <Field label="Username" value={form.smtp_same_as_imap ? form.username : form.smtp_username} onChange={(value) => setField("smtp_username", value)} disabled={form.smtp_same_as_imap} />
            <Field label="Password" value={form.smtp_same_as_imap ? form.password : form.smtp_password} onChange={(value) => setField("smtp_password", value)} type="password" placeholder={account ? "Leave blank to keep current password" : ""} disabled={form.smtp_same_as_imap} />
            <label><input type="checkbox" checked={form.smtp_same_as_imap ? form.use_tls : form.smtp_use_tls} onChange={(event) => setField("smtp_use_tls", event.target.checked)} disabled={form.smtp_same_as_imap} /> Use TLS / STARTTLS</label>
          </section>
          <section>
            <h3>Sync scope</h3>
            <Field label="Folders" value={form.mailbox} onChange={(value) => setField("mailbox", value)} placeholder="INBOX, Archives, Sent or *" />
            <Field label="Interval minutes" value={form.sync_interval_minutes} onChange={(value) => setField("sync_interval_minutes", value)} type="number" />
          </section>
        </div>
        <div className="actions">
          <button>Save account</button>
        </div>
      </form>
      <form className="panel display-settings" onSubmit={saveProfile}>
        <h2>Display preferences</h2>
        <div className="settings-columns display-settings-grid">
          <section>
            <h3>Date localization</h3>
            <Field
              label="Locale"
              value={profileForm.date_locale}
              onChange={(value) => setProfileForm((current) => ({ ...current, date_locale: value }))}
              placeholder="Browser default, en-US, en-GB, ja-JP"
            />
          </section>
          <section>
            <h3>Date format</h3>
            <label>Date style</label>
            <select
              value={profileForm.date_format}
              onChange={(event) => setProfileForm((current) => ({ ...current, date_format: event.target.value }))}
            >
              <option value="mdy">MM/DD/YY</option>
              <option value="dmy">DD/MM/YY</option>
              <option value="ymd">YY/MM/DD</option>
              <option value="locale">Locale default</option>
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
        <div className="actions">
          <button>Save display</button>
        </div>
      </form>
      {user.is_admin ? (
        <form className="panel remote-blocklist" onSubmit={saveRemoteImageBlocklist}>
          <h2>Remote image blocklist</h2>
          <div className="muted">
            One regular expression per line. Matching remote image URLs are removed when remote assets are shown; stylesheets and fonts can still load.
          </div>
          <textarea
            rows={8}
            spellCheck={false}
            value={remoteImageBlocklist}
            disabled={remoteImageBlocklistLoading}
            onChange={(event) => setRemoteImageBlocklist(event.target.value)}
          />
          <div className="actions">
            <button disabled={remoteImageBlocklistLoading}>Save blocklist</button>
          </div>
        </form>
      ) : null}
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
      <section className="panel folder-settings-panel">
        <h2>Folder sync</h2>
        <table className="folder-settings-table">
          <thead>
            <tr>
              <th>Folder</th>
              <th>Synced</th>
              <th>Messages</th>
              <th>Unread</th>
              <th>Mode</th>
              <th>Role</th>
              <th>Icon</th>
              <th>Visible in</th>
              <th>Last run</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {renderFolderRows(folderNodes)}
          </tbody>
        </table>
      </section>
      <section className="panel">
        <h2>Recent sync runs</h2>
        <table>
          <thead>
            <tr>
              <th>Status</th>
              <th>Folder</th>
              <th>Messages</th>
              <th>Updated</th>
            </tr>
          </thead>
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
    </>
  );
}

function AdminUsersView({ csrf, addToast }: { csrf: string; addToast: (message: string, kind?: Toast["kind"]) => number }) {
  const [users, setUsers] = useState<User[]>([]);
  const [form, setForm] = useState({ email: "", name: "", password: "", is_admin: false });

  const load = useCallback(async () => {
    const data = await api.users();
    setUsers(data.users);
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

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

  return (
    <>
      <div className="content-head"><h1>Users</h1></div>
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
    </>
  );
}

function SyncRunView({
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

function Field({
  label,
  value,
  onChange,
  type = "text",
  placeholder = "",
  disabled = false,
  required = false
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  type?: string;
  placeholder?: string;
  disabled?: boolean;
  required?: boolean;
}) {
  return (
    <div>
      <label>{label}</label>
      <input type={type} value={value} placeholder={placeholder} disabled={disabled} required={required} onChange={(event) => onChange(event.target.value)} />
    </div>
  );
}

function Stat({ label, value, detail }: { label: string; value: string; detail?: string }) {
  return (
    <div className="stat-card">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {detail ? <div className="stat-detail">{detail}</div> : null}
    </div>
  );
}

function ToastStack({ toasts, onDismiss }: { toasts: Toast[]; onDismiss: (id: number) => void }) {
  return (
    <div className="toast-stack">
      {toasts.map((toast) => (
        <button className={`toast ${toast.kind === "error" ? "error" : ""}`} key={toast.id} onClick={() => onDismiss(toast.id)}>
          {toast.kind === "loading" ? <span className="spinner" /> : null}
          <span>{toast.message}</span>
        </button>
      ))}
    </div>
  );
}

function link(event: MouseEvent, navigate: (url: string) => void, url: string) {
  event.preventDefault();
  navigate(url);
}

function emptyAccountForm() {
  return {
    email: "",
    host: "",
    port: "993",
    username: "",
    password: "",
    use_tls: true,
    smtp_host: "",
    smtp_port: "587",
    smtp_username: "",
    smtp_password: "",
    smtp_use_tls: true,
    smtp_same_as_imap: true,
    mailbox: "INBOX",
    sync_interval_minutes: "10"
  };
}

function accountToForm(account: Account | null) {
  if (!account) return emptyAccountForm();
  return {
    email: account.email || "",
    host: account.host || "",
    port: String(account.port || 993),
    username: account.username || "",
    password: "",
    use_tls: account.use_tls,
    smtp_host: account.smtp_host || "",
    smtp_port: String(account.smtp_port || 587),
    smtp_username: account.smtp_username || "",
    smtp_password: "",
    smtp_use_tls: account.smtp_use_tls,
    smtp_same_as_imap: account.smtp_same_as_imap,
    mailbox: account.mailbox || "INBOX",
    sync_interval_minutes: String(account.sync_interval_minutes || 10)
  };
}
