CREATE TABLE IF NOT EXISTS plugin_experimental_spam_bootstraps (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  status TEXT NOT NULL CHECK (status IN ('idle', 'running', 'complete', 'failed', 'cancelled')),
  cutoff_at INTEGER NOT NULL DEFAULT 0,
  candidate_spam INTEGER NOT NULL DEFAULT 0 CHECK (candidate_spam >= 0),
  candidate_ham INTEGER NOT NULL DEFAULT 0 CHECK (candidate_ham >= 0),
  examined INTEGER NOT NULL DEFAULT 0 CHECK (examined >= 0),
  accepted_spam INTEGER NOT NULL DEFAULT 0 CHECK (accepted_spam >= 0),
  accepted_ham INTEGER NOT NULL DEFAULT 0 CHECK (accepted_ham >= 0),
  rejected INTEGER NOT NULL DEFAULT 0 CHECK (rejected >= 0),
  current_mailbox TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  completed_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS plugin_experimental_spam_pending_move_labels (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  account_id INTEGER NOT NULL,
  identity_key TEXT NOT NULL,
  label TEXT NOT NULL CHECK (label IN ('spam', 'ham')),
  source_mailbox_id INTEGER NOT NULL,
  destination_mailbox_id INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, account_id, identity_key)
);

CREATE INDEX IF NOT EXISTS idx_plugin_experimental_spam_pending_move_labels_expiry
  ON plugin_experimental_spam_pending_move_labels(user_id, expires_at);
