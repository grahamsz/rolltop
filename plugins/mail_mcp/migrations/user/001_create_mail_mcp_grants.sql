CREATE TABLE IF NOT EXISTS plugin_mail_mcp_grants (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  client_id TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL DEFAULT '',
  redirect_uri TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  revoked_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_plugin_mail_mcp_grants_user_active ON plugin_mail_mcp_grants(user_id, revoked_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_plugin_mail_mcp_grants_user_client_scope ON plugin_mail_mcp_grants(user_id, client_id, scope);
