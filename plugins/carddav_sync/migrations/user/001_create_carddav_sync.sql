CREATE TABLE IF NOT EXISTS plugin_carddav_sync_routines (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  server_url TEXT NOT NULL,
  username TEXT NOT NULL,
  encrypted_password TEXT NOT NULL,
  addressbook_url TEXT NOT NULL,
  poll_interval_minutes INTEGER NOT NULL DEFAULT 15,
  sync_token TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'paused',
  last_error TEXT NOT NULL DEFAULT '',
  last_started_at INTEGER NOT NULL DEFAULT 0,
  last_completed_at INTEGER NOT NULL DEFAULT 0,
  synced_total INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(user_id, server_url, username, addressbook_url)
);

CREATE INDEX IF NOT EXISTS idx_plugin_carddav_sync_routines_user_enabled
  ON plugin_carddav_sync_routines(user_id, enabled, id);

CREATE TABLE IF NOT EXISTS plugin_carddav_sync_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  routine_id INTEGER NOT NULL REFERENCES plugin_carddav_sync_routines(id) ON DELETE CASCADE,
  trigger TEXT NOT NULL DEFAULT 'scheduled',
  status TEXT NOT NULL DEFAULT 'queued',
  scanned INTEGER NOT NULL DEFAULT 0,
  added INTEGER NOT NULL DEFAULT 0,
  updated INTEGER NOT NULL DEFAULT 0,
  deleted INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL DEFAULT 0,
  completed_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_plugin_carddav_sync_runs_user_routine_time
  ON plugin_carddav_sync_runs(user_id, routine_id, id DESC);

CREATE TABLE IF NOT EXISTS plugin_carddav_sync_contacts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  routine_id INTEGER NOT NULL REFERENCES plugin_carddav_sync_routines(id) ON DELETE CASCADE,
  vcard_uid TEXT NOT NULL,
  href TEXT NOT NULL DEFAULT '',
  etag TEXT NOT NULL DEFAULT '',
  contact_id INTEGER NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
  owns_contact INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  UNIQUE(user_id, routine_id, vcard_uid)
);

CREATE INDEX IF NOT EXISTS idx_plugin_carddav_sync_contacts_user_contact
  ON plugin_carddav_sync_contacts(user_id, contact_id);
