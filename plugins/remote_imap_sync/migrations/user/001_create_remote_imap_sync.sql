CREATE TABLE IF NOT EXISTS plugin_remote_imap_sync_routines (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  source_provider TEXT NOT NULL DEFAULT 'custom',
  source_host TEXT NOT NULL,
  source_port INTEGER NOT NULL DEFAULT 993,
  source_username TEXT NOT NULL,
  encrypted_source_password TEXT NOT NULL,
  source_use_tls INTEGER NOT NULL DEFAULT 1,
  source_mailbox TEXT NOT NULL DEFAULT 'INBOX',
  destination_account_id INTEGER NOT NULL,
  destination_mailbox_id INTEGER NOT NULL,
  after_date INTEGER NOT NULL DEFAULT 0,
  marker_secret TEXT NOT NULL,
  source_uidvalidity INTEGER NOT NULL DEFAULT 0,
  last_source_uid INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'paused',
  last_error TEXT NOT NULL DEFAULT '',
  last_started_at INTEGER NOT NULL DEFAULT 0,
  last_completed_at INTEGER NOT NULL DEFAULT 0,
  last_activity_at INTEGER NOT NULL DEFAULT 0,
  next_retry_at INTEGER NOT NULL DEFAULT 0,
  transferred_total INTEGER NOT NULL DEFAULT 0,
  skipped_total INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(id, user_id),
  UNIQUE(user_id, source_host, source_port, source_username, source_mailbox, destination_account_id, destination_mailbox_id)
);

CREATE INDEX IF NOT EXISTS idx_plugin_remote_imap_sync_routines_user_enabled
  ON plugin_remote_imap_sync_routines(user_id, enabled, id);

CREATE TABLE IF NOT EXISTS plugin_remote_imap_sync_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  routine_id INTEGER NOT NULL,
  trigger TEXT NOT NULL DEFAULT 'scheduled',
  status TEXT NOT NULL DEFAULT 'queued',
  scanned INTEGER NOT NULL DEFAULT 0,
  transferred INTEGER NOT NULL DEFAULT 0,
  skipped INTEGER NOT NULL DEFAULT 0,
  current_uid INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL DEFAULT 0,
  completed_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  FOREIGN KEY(routine_id, user_id) REFERENCES plugin_remote_imap_sync_routines(id, user_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_plugin_remote_imap_sync_runs_user_routine_time
  ON plugin_remote_imap_sync_runs(user_id, routine_id, id DESC);

CREATE TABLE IF NOT EXISTS plugin_remote_imap_sync_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  routine_id INTEGER NOT NULL,
  source_uidvalidity INTEGER NOT NULL,
  source_uid INTEGER NOT NULL,
  source_fingerprint TEXT NOT NULL,
  marker TEXT NOT NULL,
  destination_uid INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'transferred',
  copied_at INTEGER NOT NULL,
  FOREIGN KEY(routine_id, user_id) REFERENCES plugin_remote_imap_sync_routines(id, user_id) ON DELETE CASCADE,
  UNIQUE(user_id, routine_id, source_uidvalidity, source_uid)
);

CREATE INDEX IF NOT EXISTS idx_plugin_remote_imap_sync_messages_user_marker
  ON plugin_remote_imap_sync_messages(user_id, routine_id, marker);

CREATE INDEX IF NOT EXISTS idx_plugin_remote_imap_sync_messages_user_fingerprint
  ON plugin_remote_imap_sync_messages(user_id, routine_id, source_fingerprint);
