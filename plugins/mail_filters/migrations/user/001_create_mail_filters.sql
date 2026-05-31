CREATE TABLE IF NOT EXISTS plugin_mail_filter_rules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL DEFAULT '',
  query TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  scope_mode TEXT NOT NULL DEFAULT 'all_accounts',
  actions_json TEXT NOT NULL DEFAULT '{}',
  position INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_plugin_mail_filter_rules_user_enabled ON plugin_mail_filter_rules(user_id, enabled, position, id);

CREATE TABLE IF NOT EXISTS plugin_mail_filter_rule_accounts (
  rule_id INTEGER NOT NULL REFERENCES plugin_mail_filter_rules(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
  PRIMARY KEY(rule_id, account_id)
);

CREATE INDEX IF NOT EXISTS idx_plugin_mail_filter_rule_accounts_user_account ON plugin_mail_filter_rule_accounts(user_id, account_id);

CREATE TABLE IF NOT EXISTS plugin_mail_filter_evaluations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  rule_id INTEGER NOT NULL REFERENCES plugin_mail_filter_rules(id) ON DELETE CASCADE,
  message_id INTEGER NOT NULL,
  account_id INTEGER NOT NULL DEFAULT 0,
  mailbox_id INTEGER NOT NULL DEFAULT 0,
  phase TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  matched INTEGER NOT NULL DEFAULT 0,
  due_at INTEGER NOT NULL DEFAULT 0,
  evaluated_at INTEGER NOT NULL DEFAULT 0,
  terms_json TEXT NOT NULL DEFAULT '[]',
  fields_json TEXT NOT NULL DEFAULT '[]',
  actions_json TEXT NOT NULL DEFAULT '{}',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_plugin_mail_filter_evaluations_user_rule_time ON plugin_mail_filter_evaluations(user_id, rule_id, evaluated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_plugin_mail_filter_evaluations_due ON plugin_mail_filter_evaluations(status, due_at);
CREATE INDEX IF NOT EXISTS idx_plugin_mail_filter_evaluations_message ON plugin_mail_filter_evaluations(user_id, message_id, id);

CREATE TABLE IF NOT EXISTS plugin_mail_filter_forwarders (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  account_id INTEGER NOT NULL REFERENCES mail_accounts(id) ON DELETE CASCADE,
  forwarder_id TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(user_id, account_id)
);
