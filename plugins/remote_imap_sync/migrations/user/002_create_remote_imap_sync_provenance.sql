CREATE TABLE IF NOT EXISTS plugin_remote_imap_sync_provenance (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  destination_account_id INTEGER NOT NULL,
  destination_mailbox_id INTEGER NOT NULL,
  destination_uidvalidity INTEGER NOT NULL CHECK (destination_uidvalidity > 0),
  destination_uid INTEGER NOT NULL CHECK (destination_uid > 0),
  destination_sha256 TEXT NOT NULL CHECK (
    length(destination_sha256) = 64
    AND destination_sha256 NOT GLOB '*[^0-9a-f]*'
  ),
  synced_at INTEGER NOT NULL CHECK (synced_at > 0),
  PRIMARY KEY (user_id, destination_account_id, destination_mailbox_id, destination_uidvalidity, destination_uid)
) WITHOUT ROWID;
