export type User = {
  id: number;
  email: string;
  name: string;
  is_admin: boolean;
  date_locale: string;
  date_format: "mdy" | "dmy" | "ymd" | "locale" | string;
  theme: "classic" | "classic_dark" | "matrix" | string;
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
  local_message_count?: number;
  local_sync_percent?: number;
  search_indexed_count?: number;
  search_index_total?: number;
  search_index_percent?: number;
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
  preview?: AttachmentPreview;
};

export type AttachmentPreview = {
  available: boolean;
  kind: string;
  url: string;
  status: string;
  plugin_id: string;
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
  sender_visual?: SenderVisual;
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

export type SenderVisual = {
  plugin_id: string;
  kind: string;
  url: string;
};

export type ContactEmail = {
  id?: number;
  label: string;
  email: string;
  is_primary: boolean;
};

export type ContactPhone = {
  id?: number;
  label: string;
  number: string;
  is_primary: boolean;
};

export type ContactAddress = {
  id?: number;
  label: string;
  street: string;
  locality: string;
  region: string;
  postal_code: string;
  country: string;
  is_primary: boolean;
};

export type ContactURL = {
  id?: number;
  label: string;
  url: string;
  is_primary: boolean;
};

export type Contact = {
  id: number;
  name_prefix: string;
  given_name: string;
  additional_name: string;
  family_name: string;
  name_suffix: string;
  display_name: string;
  nickname: string;
  organization: string;
  department: string;
  job_title: string;
  birthday: string;
  notes: string;
  categories: string;
  is_me: boolean;
  is_primary: boolean;
  emails: ContactEmail[];
  phones: ContactPhone[];
  addresses: ContactAddress[];
  urls: ContactURL[];
  icon_url: string;
};

export type ContactAutocomplete = {
  contact_id: number;
  name: string;
  email: string;
  label: string;
  icon_url: string;
};

export type ComposeIdentity = {
  id: number;
  label: string;
  email: string;
  header: string;
  icon_url: string;
  is_primary: boolean;
};

export type PluginSetting = {
  id: string;
  name: string;
  description: string;
  enabled: boolean;
  enabled_by_default: boolean;
  heavy: boolean;
};

export type SyncRun = {
  id: number;
  account_id: number;
  status: string;
  started_at: string;
  finished_at: string;
  updated_at: string;
  messages_seen: number;
  messages_stored: number;
  messages_skipped: number;
  new_messages: number;
  latest_new_from: string;
  latest_new_subject: string;
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
  enabled_plugins?: string[];
};

export type ChromeEvent = {
  mailboxes: Mailbox[];
  latest_sync_run: SyncRun | null;
  active_sync_runs: SyncRun[];
  sync_running: boolean;
};

export type ComposeAttachmentUpload = {
  field: string;
  filename: string;
  content_type: string;
  content_id: string;
  inline: boolean;
  size: number;
  file: File;
};

export type ComposeForm = {
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  body: string;
  body_html: string;
  in_reply_to_id: number;
  from_identity_id: number;
};

export type Account = {
  id: number;
  email: string;
  label: string;
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

export type SMTPAccount = {
  id: number;
  label: string;
  host: string;
  port: number;
  username: string;
  use_tls: boolean;
};

export type MailIdentity = {
  id: number;
  contact_id: number;
  contact_email_id: number;
  smtp_account_id: number;
  email: string;
  display_name: string;
  signature: string;
  is_primary: boolean;
};
