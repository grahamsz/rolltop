// File overview: Typed browser API client. It centralizes JSON parsing, CSRF-bearing writes,
// ETag-aware GET caching, multipart compose uploads, and endpoint shapes used by views.

import type {
  Account,
  AccountPurgeEstimate,
  Bootstrap,
  Contact,
  ContactAutocomplete,
  ContactPGPKey,
  ComposeAttachmentUpload,
  ComposeForm,
  ComposeIdentity,
  Conversation,
  IdentityPGPPrivateKey,
  MailIdentity,
  MessageOriginalSource,
  PluginSetting,
  SMTPAccount,
  SearchExplanation,
  StorageStats,
  SyncFolder,
  SyncRun,
  ThreadMessage,
  User
} from "./types";

/** Error thrown for non-2xx API responses after the JSON error payload is decoded. */
export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// All API helpers flow through parse so callers see typed payloads on success
// and a consistent ApiError on backend validation/session failures.
async function parse<T>(res: Response): Promise<T> {
  const text = await res.text();
  let data: Record<string, unknown> = {};
  if (text) {
    try {
      data = JSON.parse(text);
    } catch (err) {
      if (!res.ok) {
        throw new ApiError(res.status, text || res.statusText);
      }
      throw err;
    }
  }
  if (!res.ok) {
    throw new ApiError(res.status, typeof data.error === "string" ? data.error : res.statusText);
  }
  return data as T;
}

type MailListResponse = { conversations: Conversation[]; page: number; has_prev: boolean; has_next: boolean };

const getCache = new Map<string, { etag: string; data: unknown }>();
const getInflight = new Map<string, Promise<unknown>>();

async function fetchGET(url: string, init: RequestInit): Promise<Response> {
  try {
    return await fetch(url, init);
  } catch (err) {
    await new Promise((resolve) => window.setTimeout(resolve, 250));
    try {
      return await fetch(url, init);
    } catch {
      throw err;
    }
  }
}

/**
 * GET JSON with lightweight ETag revalidation. The cache is process-local and
 * keyed by URL, so it only avoids repainting unchanged mailbox/search/settings
 * payloads during the current tab session.
 */
export async function getJSON<T>(url: string): Promise<T> {
  const inflight = getInflight.get(url);
  if (inflight) return inflight as Promise<T>;

  const request = (async () => {
    const headers: Record<string, string> = { Accept: "application/json" };
    const cached = getCache.get(url);
    if (cached?.etag) headers["If-None-Match"] = cached.etag;
    const res = await fetchGET(url, { headers });
    if (res.status === 304 && cached) return cached.data as T;
    const data = await parse<T>(res);
    const etag = res.headers.get("ETag") || "";
    if (etag) getCache.set(url, { etag, data });
    else getCache.delete(url);
    return data;
  })();

  getInflight.set(url, request);
  try {
    return await request;
  } finally {
    if (getInflight.get(url) === request) getInflight.delete(url);
  }
}

/** POST JSON to a mutating endpoint with the current CSRF token. */
export async function postJSON<T>(url: string, csrf: string, body: unknown = {}): Promise<T> {
  return parse<T>(
    await fetch(url, {
      method: "POST",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf
      },
      body: JSON.stringify(body)
    })
  );
}

/** PUT JSON to a mutating endpoint with the current CSRF token. */
export async function putJSON<T>(url: string, csrf: string, body: unknown = {}): Promise<T> {
  return parse<T>(
    await fetch(url, {
      method: "PUT",
      headers: {
        Accept: "application/json",
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf
      },
      body: JSON.stringify(body)
    })
  );
}

/** DELETE JSON from a mutating endpoint with the current CSRF token. */
export async function deleteJSON<T>(url: string, csrf: string): Promise<T> {
  return parse<T>(
    await fetch(url, {
      method: "DELETE",
      headers: {
        Accept: "application/json",
        "X-CSRF-Token": csrf
      }
    })
  );
}

/** POST multipart form data without forcing a Content-Type boundary. */
export async function postForm<T>(url: string, csrf: string, body: FormData): Promise<T> {
  return parse<T>(
    await fetch(url, {
      method: "POST",
      headers: {
        Accept: "application/json",
        "X-CSRF-Token": csrf
      },
      body
    })
  );
}

function composeSendPayload(form: ComposeForm): ComposeForm {
  const { available_attachments: _availableAttachments, forward_attachment: _forwardAttachment, ...payload } = form;
  return payload;
}

function mailListURL(mailboxID: string | null, page: number) {
  const q = new URLSearchParams({ page: String(page) });
  if (mailboxID) q.set("mailbox", mailboxID);
  return `/api/mail?${q}`;
}

function searchListURL(query: string, page: number) {
  const q = new URLSearchParams({ q: query, page: String(page) });
  return `/api/search?${q}`;
}

function prefetchJSON<T>(url: string) {
  void getJSON<T>(url).catch(() => undefined);
}

function cachedJSON<T>(url: string): T | null {
  const cached = getCache.get(url);
  return cached ? cached.data as T : null;
}


// The api object is deliberately explicit rather than generated: it documents
// the route surface used by the current frontend and keeps response shapes close
// to the call sites that depend on them.
export const api = {
  bootstrap: () => getJSON<Bootstrap>("/api/bootstrap"),
  setup: (csrf: string, body: { email: string; name: string; password: string }) =>
    postJSON<{ ok: boolean }>("/api/setup", csrf, body),
  login: (csrf: string, body: { email: string; password: string }) => postJSON<{ ok: boolean }>("/api/login", csrf, body),
  logout: (csrf: string) => postJSON<{ ok: boolean }>("/api/logout", csrf),
  mail: (mailboxID: string | null, page: number) =>
    getJSON<MailListResponse>(mailListURL(mailboxID, page)),
  cachedMail: (mailboxID: string | null, page: number) =>
    cachedJSON<MailListResponse>(mailListURL(mailboxID, page)),
  prefetchMail: (mailboxID: string | null, page: number) =>
    prefetchJSON<MailListResponse>(mailListURL(mailboxID, page)),
  search: (query: string, page: number) =>
    getJSON<{ conversations: Conversation[]; page: number; has_prev: boolean; has_next: boolean }>(searchListURL(query, page)),
  prefetchSearch: (query: string, page: number) =>
    prefetchJSON<{ conversations: Conversation[]; page: number; has_prev: boolean; has_next: boolean }>(searchListURL(query, page)),
  brandIcons: (domains: string[]) => {
    const q = new URLSearchParams();
    domains.slice(0, 40).forEach((domain) => q.append("domain", domain));
    return getJSON<{ icons: Record<string, string> }>(`/api/brand-icons?${q}`);
  },
  message: (id: string, showImages: boolean, highlightQuery = "") => {
    const params = new URLSearchParams();
    if (showImages) params.set("images", "1");
    if (highlightQuery.trim()) params.set("q", highlightQuery.trim());
    const q = params.toString() ? `?${params}` : "";
    return getJSON<{
      message: { id: number; account_id: number; subject: string; mailbox_id: number };
      thread: ThreadMessage[];
      compose_from: string;
      from_identities: ComposeIdentity[];
      mailbox_id: number;
      conversation: number;
    }>(`/api/messages/${id}${q}`);
  },
  messageLoadStatus: (id: string) =>
    getJSON<{
      conversation: number;
      imap_fetch_count: number;
      local_blob_count: number;
      indexed_count: number;
      unavailable_count: number;
      source: "imap" | "local_blob" | "local" | "indexed" | "preview" | string;
    }>(`/api/messages/${id}/load-status`),
  messageOriginal: (id: number) => getJSON<MessageOriginalSource>(`/api/messages/${id}/original`),
  searchExplanation: (id: number, query: string, hitID = 0) => {
    const params = new URLSearchParams();
    params.set("q", query.trim());
    if (hitID > 0) params.set("hit", String(hitID));
    return getJSON<SearchExplanation>(`/api/messages/${id}/search-explanation?${params}`);
  },
  trustImages: (csrf: string, id: number) => postJSON<{ ok: boolean }>(`/api/messages/${id}/images/trust`, csrf),
  unsubscribe: (csrf: string, id: number) =>
    postJSON<{ ok: boolean; already_sent: boolean; sent_at: string }>(`/api/messages/${id}/unsubscribe`, csrf),
  setStarred: (csrf: string, id: number, starred: boolean) =>
    postJSON<{ ok: boolean; message: { id: number; is_starred: boolean } }>(`/api/messages/${id}/star`, csrf, { starred }),
  moveMessage: (csrf: string, id: number, mailboxID: number) =>
    postJSON<{ ok: boolean; mailbox: string }>(`/api/messages/${id}/move`, csrf, { mailbox_id: mailboxID }),
  bulkMoveMessages: (csrf: string, ids: number[], mailboxID: number) =>
    postJSON<{ ok: boolean; queued: boolean; moved?: number; run_id?: number; mailbox: string }>("/api/messages/bulk-move", csrf, {
      message_ids: ids,
      mailbox_id: mailboxID
    }),
  bulkCopyMessages: (csrf: string, ids: number[], mailboxID: number) =>
    postJSON<{ ok: boolean; queued: boolean; copied?: number; run_id?: number; mailbox: string }>("/api/messages/bulk-copy", csrf, {
      message_ids: ids,
      mailbox_id: mailboxID
    }),
  compose: (query: string) =>
    getJSON<{ compose: ComposeForm; compose_from: string; from_identities: ComposeIdentity[] }>(`/api/compose${query ? `?${query}` : ""}`),
  // Compose sends pure JSON when possible, then switches to multipart only when
  // there are file bodies. Inline files are represented in the JSON payload by
  // stable form field names and Content-ID metadata.
  send: (csrf: string, form: ComposeForm, attachments: ComposeAttachmentUpload[] = []) => {
    const payload = composeSendPayload(form);
    if (attachments.length === 0) {
      return postJSON<{ ok: boolean; message_id: number }>("/api/compose", csrf, payload);
    }
    const body = new FormData();
    body.append("payload", JSON.stringify({
      ...payload,
      attachments: attachments.map((attachment) => ({
        field: attachment.field,
        filename: attachment.filename,
        content_type: attachment.content_type,
        content_id: attachment.content_id,
        inline: attachment.inline,
        size: attachment.size
      }))
    }));
    attachments.forEach((attachment) => body.append(attachment.field, attachment.file, attachment.filename));
    return postForm<{ ok: boolean; message_id: number }>("/api/compose", csrf, body);
  },
  saveDraft: (csrf: string, form: ComposeForm, attachments: ComposeAttachmentUpload[] = []) => {
    const payload = composeSendPayload(form);
    if (attachments.length === 0) {
      return postJSON<{ ok: boolean; message_id: number }>("/api/compose/draft", csrf, payload);
    }
    const body = new FormData();
    body.append("payload", JSON.stringify({
      ...payload,
      attachments: attachments.map((attachment) => ({
        field: attachment.field,
        filename: attachment.filename,
        content_type: attachment.content_type,
        content_id: attachment.content_id,
        inline: attachment.inline,
        size: attachment.size
      }))
    }));
    attachments.forEach((attachment) => body.append(attachment.field, attachment.file, attachment.filename));
    return postForm<{ ok: boolean; message_id: number }>("/api/compose/draft", csrf, body);
  },
  contacts: (query = "") => {
    const q = query.trim() ? `?${new URLSearchParams({ q: query.trim() })}` : "";
    return getJSON<{ contacts: Contact[] }>(`/api/contacts${q}`);
  },
  contactAutocomplete: (query: string) =>
    getJSON<{ contacts: ContactAutocomplete[] }>(`/api/contacts/autocomplete?${new URLSearchParams({ q: query })}`),
  createContact: (csrf: string, contact: Contact) => postJSON<{ contact: Contact }>("/api/contacts", csrf, contact),
  updateContact: (csrf: string, contact: Contact) => putJSON<{ contact: Contact }>(`/api/contacts/${contact.id}`, csrf, contact),
  deleteContact: (csrf: string, id: number) => deleteJSON<{ ok: boolean }>(`/api/contacts/${id}`, csrf),
  uploadContactIcon: (csrf: string, id: number, file: File) => {
    const form = new FormData();
    form.append("icon", file);
    return postForm<{ contact: Contact }>(`/api/contacts/${id}/icon`, csrf, form);
  },
  deleteContactIcon: (csrf: string, id: number) => deleteJSON<{ contact: Contact }>(`/api/contacts/${id}/icon`, csrf),
  pgpPrivateKeys: () => getJSON<{ keys: IdentityPGPPrivateKey[] }>("/api/account/pgp/private-keys"),
  savePGPPrivateKey: (csrf: string, key: IdentityPGPPrivateKey) =>
    postJSON<{ ok: boolean; key: IdentityPGPPrivateKey }>("/api/account/pgp/private-keys", csrf, key),
  deletePGPPrivateKey: (csrf: string, id: number) => deleteJSON<{ ok: boolean }>(`/api/account/pgp/private-keys/${id}`, csrf),
  pgpPublicKeys: (emails: string[], all = false) => {
    const q = new URLSearchParams();
    emails.forEach((email) => q.append("email", email));
    if (all) q.set("all", "1");
    return getJSON<{ keys: ContactPGPKey[] }>(`/api/pgp/public-keys?${q}`);
  },
  savePGPPublicKey: (csrf: string, key: ContactPGPKey) =>
    postJSON<{ ok: boolean; key: ContactPGPKey }>("/api/pgp/public-keys", csrf, key),
  importContacts: (csrf: string, file: File) => {
    const form = new FormData();
    form.append("file", file);
    return postForm<{ ok: boolean; imported: number; updated: number }>("/api/contacts/import", csrf, form);
  },
  addSenderContact: (csrf: string, id: number) =>
    postJSON<{ contact: Contact; created: boolean }>(`/api/messages/${id}/contacts/add-sender`, csrf),
  syncStatus: () => getJSON<{ running: boolean; latest: SyncRun | null }>("/api/sync/status"),
  account: () =>
    getJSON<{
      imap_accounts: Account[];
      smtp_accounts: SMTPAccount[];
      identities: MailIdentity[];
      me_contacts: Contact[];
      sync_runs: SyncRun[];
      sync_folders: SyncFolder[];
      storage?: StorageStats;
      notice: string;
      account_needs_password?: boolean;
    }>("/api/account"),
  storage: () => getJSON<StorageStats>("/api/storage"),
  plugins: () => getJSON<{ enabled: string[] }>("/api/plugins"),
  saveProfile: (csrf: string, profile: {
    date_locale: string;
    date_format: string;
    theme: string;
    search_preset: string;
    search_recency_bias: string;
    search_fuzzy: string;
    search_sender_boost: boolean;
    search_sender_history: string;
    search_contact_boost: string;
    search_attachment_weight: string;
    search_compact_splitting: boolean;
  }) =>
    postJSON<{ user: User }>("/api/profile", csrf, profile),
  saveIMAPAccount: (csrf: string, account: Record<string, unknown>) =>
    postJSON<{ ok: boolean; account: Account }>("/api/account/imap", csrf, account),
  imapAccountPurgeEstimate: (id: number) =>
    getJSON<AccountPurgeEstimate>(`/api/account/imap/${id}/purge-estimate`),
  deleteIMAPAccount: (csrf: string, id: number, confirm: string) =>
    postJSON<{ ok: boolean; queued: boolean; run_id: number; estimate: AccountPurgeEstimate }>(`/api/account/imap/${id}/delete`, csrf, { confirm }),
  saveSMTPAccount: (csrf: string, account: Record<string, unknown>) =>
    postJSON<{ ok: boolean; smtp_account: SMTPAccount }>("/api/account/smtp", csrf, account),
  deleteSMTPAccount: (csrf: string, id: number) =>
    deleteJSON<{ ok: boolean }>(`/api/account/smtp/${id}`, csrf),
  saveMailIdentity: (csrf: string, identity: Record<string, unknown>) =>
    postJSON<{ ok: boolean; identity: MailIdentity; identities: MailIdentity[] }>("/api/account/identities", csrf, identity),
  syncAccount: (csrf: string) => postJSON<{ ok: boolean }>("/api/account/sync", csrf),
  setFolderMode: (csrf: string, id: number, syncMode: string) =>
    postJSON<{ ok: boolean }>(`/api/account/folders/${id}/mode`, csrf, { sync_mode: syncMode }),
  saveFolderSettings: (csrf: string, id: number, settings: Record<string, unknown>) =>
    postJSON<{ ok: boolean }>(`/api/account/folders/${id}/settings`, csrf, settings),
  syncFolder: (csrf: string, id: number) => postJSON<{ ok: boolean }>(`/api/account/folders/${id}/sync`, csrf),
  purgeFolderSearchIndex: (csrf: string, id: number) =>
    postJSON<{ ok: boolean; queued: boolean; run_id: number }>(`/api/account/folders/${id}/search-index/purge`, csrf),
  purgeFolderLocalReferences: (csrf: string, id: number) =>
    postJSON<{ ok: boolean; queued: boolean; run_id: number }>(`/api/account/folders/${id}/local-references/purge`, csrf),
  users: () => getJSON<{ users: User[] }>("/api/admin/users"),
  createUser: (csrf: string, body: { email: string; name: string; password: string; is_admin: boolean }) =>
    postJSON<{ ok: boolean }>("/api/admin/users", csrf, body),
  adminPlugins: () => getJSON<{ plugins: PluginSetting[] }>("/api/admin/plugins"),
  setAdminPlugin: (csrf: string, id: string, enabled: boolean) =>
    postJSON<{ ok: boolean; plugins: PluginSetting[] }>(`/api/admin/plugins/${encodeURIComponent(id)}`, csrf, { enabled }),
  remoteImageBlocklist: () => getJSON<{ patterns: string[] }>("/api/admin/remote-image-blocklist"),
  saveRemoteImageBlocklist: (csrf: string, patterns: string[]) =>
    postJSON<{ ok: boolean; patterns: string[] }>("/api/admin/remote-image-blocklist", csrf, { patterns }),
  syncRun: (id: string) => getJSON<{ sync_run: SyncRun }>(`/api/sync-runs/${id}`)
};
