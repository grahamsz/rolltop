CREATE TABLE IF NOT EXISTS plugin_experimental_spam_bayes_state (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  token_schema TEXT NOT NULL,
  spam_messages INTEGER NOT NULL DEFAULT 0 CHECK (spam_messages >= 0),
  ham_messages INTEGER NOT NULL DEFAULT 0 CHECK (ham_messages >= 0),
  token_count INTEGER NOT NULL DEFAULT 0 CHECK (token_count >= 0),
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS plugin_experimental_spam_bayes_tokens (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash BLOB NOT NULL CHECK (length(token_hash) = 32),
  spam_messages INTEGER NOT NULL DEFAULT 0 CHECK (spam_messages >= 0),
  ham_messages INTEGER NOT NULL DEFAULT 0 CHECK (ham_messages >= 0),
  last_seen INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, token_hash)
);

CREATE TABLE IF NOT EXISTS plugin_experimental_spam_bayes_documents (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  message_fingerprint BLOB NOT NULL CHECK (length(message_fingerprint) = 32),
  token_schema TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, message_fingerprint)
);

CREATE TABLE IF NOT EXISTS plugin_experimental_spam_bayes_learns (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  message_id INTEGER NOT NULL,
  message_fingerprint BLOB NOT NULL CHECK (length(message_fingerprint) = 32),
  label TEXT NOT NULL CHECK (label IN ('spam', 'ham')),
  source TEXT NOT NULL DEFAULT 'explicit' CHECK (source IN ('explicit', 'automatic')),
  token_schema TEXT NOT NULL,
  learned_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, message_id),
  FOREIGN KEY (user_id, message_fingerprint) REFERENCES plugin_experimental_spam_bayes_documents(user_id, message_fingerprint) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_plugin_experimental_spam_bayes_learns_fingerprint
  ON plugin_experimental_spam_bayes_learns(user_id, message_fingerprint, message_id);

CREATE INDEX IF NOT EXISTS idx_plugin_experimental_spam_bayes_learns_expiry
  ON plugin_experimental_spam_bayes_learns(user_id, updated_at, message_id);

CREATE TABLE IF NOT EXISTS plugin_experimental_spam_bayes_learn_tokens (
  user_id INTEGER NOT NULL,
  message_fingerprint BLOB NOT NULL CHECK (length(message_fingerprint) = 32),
  token_hash BLOB NOT NULL CHECK (length(token_hash) = 32),
  PRIMARY KEY (user_id, message_fingerprint, token_hash),
  FOREIGN KEY (user_id, message_fingerprint) REFERENCES plugin_experimental_spam_bayes_documents(user_id, message_fingerprint) ON DELETE CASCADE
);
