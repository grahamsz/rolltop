# Gmail MCP

Gmail MCP adds an OAuth-protected MCP endpoint for local clients that expect
Gmail-style mail tools.

Routes:

- `GET /api/plugins/gmail_mcp/oauth/authorize`
- `POST /api/plugins/gmail_mcp/oauth/token`
- `POST /api/plugins/gmail_mcp/mcp`

The authorize route uses the existing Rolltop browser session. The token route
exchanges authorization codes and refresh tokens for bearer tokens. Tokens are
process-local and are invalidated when Rolltop restarts.

The MCP endpoint is read-only. It exposes Gmail-style tools for profile, labels,
message listing, message lookup, thread listing, and thread lookup. It does not
send, delete, move, archive, or mutate remote mail.
