CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_user_id_id
  ON messages(user_id, id);

CREATE TABLE IF NOT EXISTS plugin_experimental_spam_classifications (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  message_id INTEGER NOT NULL,
  model_version TEXT NOT NULL,
  base_probability REAL NOT NULL,
  labeled_neighbor_probability REAL NOT NULL DEFAULT 0.5,
  labeled_neighbor_count INTEGER NOT NULL DEFAULT 0,
  recent_read_support REAL NOT NULL DEFAULT 0,
  final_probability REAL NOT NULL,
  risk_band TEXT NOT NULL CHECK (risk_band IN ('low', 'medium', 'high')),
  content_coverage TEXT NOT NULL DEFAULT 'metadata',
  explanation_json TEXT NOT NULL DEFAULT '{}',
  classified_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, message_id),
  FOREIGN KEY (user_id, message_id) REFERENCES messages(user_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_plugin_experimental_spam_classifications_user_band
  ON plugin_experimental_spam_classifications(user_id, risk_band, message_id);

CREATE TABLE IF NOT EXISTS plugin_experimental_spam_feedback (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  message_id INTEGER NOT NULL,
  label TEXT NOT NULL CHECK (label IN ('spam', 'ham')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, message_id),
  FOREIGN KEY (user_id, message_id) REFERENCES messages(user_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_plugin_experimental_spam_feedback_user_label_recent
  ON plugin_experimental_spam_feedback(user_id, label, updated_at DESC, message_id DESC);

CREATE TABLE IF NOT EXISTS plugin_experimental_spam_backfills (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  model_version TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('idle', 'running', 'complete', 'failed', 'cancelled')),
  requested INTEGER NOT NULL DEFAULT 0,
  processed INTEGER NOT NULL DEFAULT 0,
  failed INTEGER NOT NULL DEFAULT 0,
  last_message_id INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  completed_at INTEGER NOT NULL DEFAULT 0
);
