CREATE TABLE IF NOT EXISTS plugin_experimental_spam_bayes_labels (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  message_fingerprint BLOB NOT NULL CHECK (length(message_fingerprint) = 32),
  source TEXT NOT NULL CHECK (source IN ('explicit', 'automatic')),
  origin_key TEXT NOT NULL,
  message_id INTEGER,
  label TEXT NOT NULL CHECK (label IN ('spam', 'ham')),
  token_schema TEXT NOT NULL,
  learned_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, message_fingerprint, source, origin_key),
  FOREIGN KEY (user_id, message_fingerprint) REFERENCES plugin_experimental_spam_bayes_documents(user_id, message_fingerprint) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_plugin_experimental_spam_bayes_labels_message
  ON plugin_experimental_spam_bayes_labels(user_id, message_id)
  WHERE source = 'explicit' AND message_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_plugin_experimental_spam_bayes_labels_effective
  ON plugin_experimental_spam_bayes_labels(user_id, message_fingerprint, source, updated_at, origin_key);

CREATE INDEX IF NOT EXISTS idx_plugin_experimental_spam_bayes_labels_expiry
  ON plugin_experimental_spam_bayes_labels(user_id, source, updated_at, origin_key);

CREATE INDEX IF NOT EXISTS idx_plugin_experimental_spam_bayes_learn_tokens_hash
  ON plugin_experimental_spam_bayes_learn_tokens(user_id, token_hash, message_fingerprint);

INSERT OR IGNORE INTO plugin_experimental_spam_bayes_labels
  (user_id, message_fingerprint, source, origin_key, message_id, label, token_schema, learned_at, updated_at)
SELECT user_id,
       message_fingerprint,
       source,
       'message:' || CAST(message_id AS TEXT),
       CASE WHEN source = 'explicit' THEN message_id ELSE NULL END,
       label,
       token_schema,
       learned_at,
       updated_at
FROM plugin_experimental_spam_bayes_learns;

UPDATE plugin_experimental_spam_bayes_tokens
SET spam_messages = (
      SELECT COUNT(*)
      FROM plugin_experimental_spam_bayes_learn_tokens AS document_token
      JOIN plugin_experimental_spam_bayes_labels AS label
        ON label.user_id = document_token.user_id
       AND label.message_fingerprint = document_token.message_fingerprint
      WHERE document_token.user_id = plugin_experimental_spam_bayes_tokens.user_id
        AND document_token.token_hash = plugin_experimental_spam_bayes_tokens.token_hash
        AND label.label = 'spam'
        AND NOT EXISTS (
          SELECT 1
          FROM plugin_experimental_spam_bayes_labels AS preferred
          WHERE preferred.user_id = label.user_id
            AND preferred.message_fingerprint = label.message_fingerprint
            AND (
              (preferred.source = 'explicit' AND label.source = 'automatic')
              OR (
                preferred.source = label.source
                AND (
                  preferred.updated_at > label.updated_at
                  OR (preferred.updated_at = label.updated_at AND preferred.origin_key > label.origin_key)
                )
              )
            )
        )
    ),
    ham_messages = (
      SELECT COUNT(*)
      FROM plugin_experimental_spam_bayes_learn_tokens AS document_token
      JOIN plugin_experimental_spam_bayes_labels AS label
        ON label.user_id = document_token.user_id
       AND label.message_fingerprint = document_token.message_fingerprint
      WHERE document_token.user_id = plugin_experimental_spam_bayes_tokens.user_id
        AND document_token.token_hash = plugin_experimental_spam_bayes_tokens.token_hash
        AND label.label = 'ham'
        AND NOT EXISTS (
          SELECT 1
          FROM plugin_experimental_spam_bayes_labels AS preferred
          WHERE preferred.user_id = label.user_id
            AND preferred.message_fingerprint = label.message_fingerprint
            AND (
              (preferred.source = 'explicit' AND label.source = 'automatic')
              OR (
                preferred.source = label.source
                AND (
                  preferred.updated_at > label.updated_at
                  OR (preferred.updated_at = label.updated_at AND preferred.origin_key > label.origin_key)
                )
              )
            )
        )
    );

UPDATE plugin_experimental_spam_bayes_state
SET spam_messages = (
      SELECT COUNT(*)
      FROM plugin_experimental_spam_bayes_labels AS label
      WHERE label.user_id = plugin_experimental_spam_bayes_state.user_id
        AND label.label = 'spam'
        AND NOT EXISTS (
          SELECT 1
          FROM plugin_experimental_spam_bayes_labels AS preferred
          WHERE preferred.user_id = label.user_id
            AND preferred.message_fingerprint = label.message_fingerprint
            AND (
              (preferred.source = 'explicit' AND label.source = 'automatic')
              OR (
                preferred.source = label.source
                AND (
                  preferred.updated_at > label.updated_at
                  OR (preferred.updated_at = label.updated_at AND preferred.origin_key > label.origin_key)
                )
              )
            )
        )
    ),
    ham_messages = (
      SELECT COUNT(*)
      FROM plugin_experimental_spam_bayes_labels AS label
      WHERE label.user_id = plugin_experimental_spam_bayes_state.user_id
        AND label.label = 'ham'
        AND NOT EXISTS (
          SELECT 1
          FROM plugin_experimental_spam_bayes_labels AS preferred
          WHERE preferred.user_id = label.user_id
            AND preferred.message_fingerprint = label.message_fingerprint
            AND (
              (preferred.source = 'explicit' AND label.source = 'automatic')
              OR (
                preferred.source = label.source
                AND (
                  preferred.updated_at > label.updated_at
                  OR (preferred.updated_at = label.updated_at AND preferred.origin_key > label.origin_key)
                )
              )
            )
        )
    ),
    token_count = (
      SELECT COUNT(*)
      FROM plugin_experimental_spam_bayes_tokens AS token
      WHERE token.user_id = plugin_experimental_spam_bayes_state.user_id
    );
