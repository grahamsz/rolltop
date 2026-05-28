# One-click unsubscribe plugin

Detects RFC 8058 unsubscribe links and sends one-click unsubscribe requests.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `plugin.go` is the built-in registration shim.
- `history/` contains send-history persistence, sender normalization, and migrations.

## Hooks

Message details expose unsubscribe actions when matching headers are present. The plugin records recent sends so the UI can avoid duplicate one-click requests.
