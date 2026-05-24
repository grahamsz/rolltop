import type {
  Account,
  Bootstrap,
  ComposeForm,
  Conversation,
  StorageStats,
  SyncFolder,
  SyncRun,
  ThreadMessage,
  User
} from "./types";

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

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

export async function getJSON<T>(url: string): Promise<T> {
  return parse<T>(await fetch(url, { headers: { Accept: "application/json" } }));
}

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

export const api = {
  bootstrap: () => getJSON<Bootstrap>("/api/bootstrap"),
  setup: (csrf: string, body: { email: string; name: string; password: string }) =>
    postJSON<{ ok: boolean }>("/api/setup", csrf, body),
  login: (csrf: string, body: { email: string; password: string }) => postJSON<{ ok: boolean }>("/api/login", csrf, body),
  logout: (csrf: string) => postJSON<{ ok: boolean }>("/api/logout", csrf),
  mail: (mailboxID: string | null, page: number) => {
    const q = new URLSearchParams({ page: String(page) });
    if (mailboxID) q.set("mailbox", mailboxID);
    return getJSON<{ conversations: Conversation[]; page: number; has_prev: boolean; has_next: boolean }>(`/api/mail?${q}`);
  },
  search: (query: string, sort: string, page: number) => {
    const q = new URLSearchParams({ q: query, sort, page: String(page) });
    return getJSON<{ conversations: Conversation[]; page: number; has_prev: boolean; has_next: boolean }>(
      `/api/search?${q}`
    );
  },
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
      message: { id: number; subject: string; mailbox_id: number };
      thread: ThreadMessage[];
      compose_from: string;
      mailbox_id: number;
      conversation: number;
    }>(`/api/messages/${id}${q}`);
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
  compose: (query: string) =>
    getJSON<{ compose: ComposeForm; compose_from: string }>(`/api/compose${query ? `?${query}` : ""}`),
  send: (csrf: string, form: ComposeForm) => postJSON<{ ok: boolean; message_id: number }>("/api/compose", csrf, form),
  syncStatus: () => getJSON<{ running: boolean; latest: SyncRun | null }>("/api/sync/status"),
  account: () =>
    getJSON<{
      account: Account | null;
      sync_runs: SyncRun[];
      sync_folders: SyncFolder[];
      storage?: StorageStats;
      notice: string;
      account_needs_password?: boolean;
    }>("/api/account"),
  storage: () => getJSON<StorageStats>("/api/storage"),
  saveProfile: (csrf: string, profile: { date_locale: string; date_format: string; theme: string }) =>
    postJSON<{ user: User }>("/api/profile", csrf, profile),
  saveAccount: (csrf: string, account: Record<string, unknown>) => postJSON<{ ok: boolean }>("/api/account", csrf, account),
  syncAccount: (csrf: string) => postJSON<{ ok: boolean }>("/api/account/sync", csrf),
  setFolderMode: (csrf: string, id: number, syncMode: string) =>
    postJSON<{ ok: boolean }>(`/api/account/folders/${id}/mode`, csrf, { sync_mode: syncMode }),
  saveFolderSettings: (csrf: string, id: number, settings: Record<string, unknown>) =>
    postJSON<{ ok: boolean }>(`/api/account/folders/${id}/settings`, csrf, settings),
  syncFolder: (csrf: string, id: number) => postJSON<{ ok: boolean }>(`/api/account/folders/${id}/sync`, csrf),
  rebuildFolderIndex: (csrf: string, id: number) =>
    postJSON<{ ok: boolean; run_id: number }>(`/api/account/folders/${id}/search-index/rebuild`, csrf),
  users: () => getJSON<{ users: User[] }>("/api/admin/users"),
  createUser: (csrf: string, body: { email: string; name: string; password: string; is_admin: boolean }) =>
    postJSON<{ ok: boolean }>("/api/admin/users", csrf, body),
  remoteImageBlocklist: () => getJSON<{ patterns: string[] }>("/api/admin/remote-image-blocklist"),
  saveRemoteImageBlocklist: (csrf: string, patterns: string[]) =>
    postJSON<{ ok: boolean; patterns: string[] }>("/api/admin/remote-image-blocklist", csrf, { patterns }),
  syncRun: (id: string) => getJSON<{ sync_run: SyncRun }>(`/api/sync-runs/${id}`)
};
