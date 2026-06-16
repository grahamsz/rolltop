# Mail MCP

Mail MCP adds an OAuth-protected MCP endpoint for local clients that expect
Gmail-like mail tools.

Routes:

- `GET /api/plugins/mail_mcp/.well-known/oauth-authorization-server`
- `GET /api/plugins/mail_mcp/.well-known/oauth-protected-resource`
- `GET /api/plugins/mail_mcp/oauth/authorize`
- `POST /api/plugins/mail_mcp/oauth/token`
- `POST /api/plugins/mail_mcp/mcp`

The authorize route uses the existing Rolltop browser session. The token route
exchanges authorization codes and refresh tokens for bearer tokens. Tokens are
process-local and are invalidated when Rolltop restarts.

The MCP endpoint is read-only. It exposes Gmail-like tools for profile, labels,
message listing, message lookup, thread listing, and thread lookup. It does not
send, delete, move, archive, or mutate remote mail.

Message and thread list `q` arguments support free-text terms plus date filters
such as `after:2026/6/16 before:2026/6/17`.
