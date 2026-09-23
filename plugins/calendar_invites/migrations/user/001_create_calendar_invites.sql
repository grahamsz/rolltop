CREATE TABLE IF NOT EXISTS plugin_calendar_invites_invites (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  message_id INTEGER NOT NULL DEFAULT 0,
  ics_uid TEXT NOT NULL,
  sequence INTEGER NOT NULL DEFAULT 0,
  method TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  dtstart TEXT NOT NULL DEFAULT '',
  dtend TEXT NOT NULL DEFAULT '',
  organizer_address TEXT NOT NULL DEFAULT '',
  organizer_name TEXT NOT NULL DEFAULT '',
  attendee_address TEXT NOT NULL DEFAULT '',
  attendee_name TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  raw_ics TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(user_id, ics_uid)
);

CREATE INDEX IF NOT EXISTS idx_plugin_calendar_invites_invites_user_status
  ON plugin_calendar_invites_invites(user_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS plugin_calendar_invites_caldav (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  server_url TEXT NOT NULL DEFAULT '',
  username TEXT NOT NULL DEFAULT '',
  encrypted_password TEXT NOT NULL DEFAULT '',
  calendar_url TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE(user_id)
);
