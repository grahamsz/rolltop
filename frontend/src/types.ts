export type User = {
  id: number;
  email: string;
  name: string;
  is_admin: boolean;
  date_locale: string;
  date_format: "mdy" | "dmy" | "ymd" | "locale" | string;
  theme: "classic" | "modern" | string;
};

export type Mailbox = {
  id: number;
  account_id: number;
  account_email: string;
  name: string;
  message_count: number;
  unread_count: number;
  sync_mode: "auto" | "manual" | "never" | string;
  role: "inbox" | "trash" | "" | string;
  icon: string;
  show_in_sidebar: boolean;
  show_in_all_mail: boolean;
  include_in_search: boolean;
  last_uid: number;
  remote_message_count: number;
  remote_unread_count: number;
  remote_uid_next: number;
  sync_percent: number;
};

export type Message = {
  id: number;
  mailbox_id: number;
  subject: string;
  from_addr: string;
  to_addr: string;
  cc_addr: string;
  date: string;
  date_short: string;
  is_read: boolean;
  is_starred: boolean;
  has_attachments: boolean;
  snippet: string;
};

export type Conversation = {
  message: Message;
  starred_message_id: number;
  participants: string;
  count: number;
  is_read: boolean;
  has_attachments: boolean;
  attachment_names?: string[];
  attachment_matches?: string[];
  attachment_content_matched?: boolean;
  snippet: string;
  match_terms?: string[];
};

export type Attachment = {
  id: number;
  filename: string;
  content_type: string;
  size: number;
  download_url: string;
  matched?: boolean;
  content_matched?: boolean;
};

export type HeaderDetail = {
  label: string;
  value: string;
};

export type ThreadMessage = {
  message: Message;
  attachments: Attachment[];
  header_details: HeaderDetail[];
  one_click_unsubscribe: boolean;
  one_click_unsubscribe_sent_at: string;
  sender_name: string;
  sender_email: string;
  sender_initial: string;
  recipient_line: string;
  snippet: string;
  body_doc: string;
  full_body_doc: string;
  has_hidden_quoted: boolean;
  has_display_body: boolean;
  body_preview_only: boolean;
  has_remote_images: boolean;
  images_allowed: boolean;
  expanded: boolean;
  reply_subject: string;
};

export type SyncRun = {
  id: number;
  status: string;
  started_at: string;
  finished_at: string;
  updated_at: string;
  messages_seen: number;
  messages_stored: number;
  messages_skipped: number;
  new_messages: number;
  messages_total: number;
  mailboxes_done: number;
  mailboxes_total: number;
  current_mailbox: string;
  current_uid: number;
  error: string;
};

export type SyncFolder = {
  mailbox: Mailbox;
  is_running: boolean;
  last_run: SyncRun | null;
  can_sync_now: boolean;
};

export type StorageStats = Record<string, unknown>;

export type Bootstrap = {
  users_exist: boolean;
  csrf: string;
  user: User | null;
  mailboxes: Mailbox[];
  latest_sync_run?: SyncRun | null;
  active_sync_runs?: SyncRun[];
  sync_running?: boolean;
  account_needs_password?: boolean;
  account_notice?: string;
};

export type ChromeEvent = {
  mailboxes: Mailbox[];
  latest_sync_run: SyncRun | null;
  active_sync_runs: SyncRun[];
  sync_running: boolean;
};

export type ComposeForm = {
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  body: string;
  body_html: string;
  in_reply_to_id: number;
};

export type Account = {
  id: number;
  email: string;
  host: string;
  port: number;
  username: string;
  use_tls: boolean;
  smtp_host: string;
  smtp_port: number;
  smtp_username: string;
  smtp_use_tls: boolean;
  smtp_same_as_imap: boolean;
  mailbox: string;
  sync_interval_minutes: number;
};
