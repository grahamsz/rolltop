# Gravatar sender icons plugin

Optionally proxies and caches Gravatar images for sender email addresses.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `plugin.go` is the built-in registration shim.
- `gravatar/` contains hash normalization, cache persistence, URL helpers, and migrations.

## Hooks

Message presentation asks the plugin for sender email image URLs. Plugin routes fetch and serve cached avatars under `/plugins/gravatar_sender_icons/avatar/:hash`.
