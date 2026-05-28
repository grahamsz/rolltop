# Trusted image sources plugin

Remembers senders whose remote images may load automatically.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `plugin.go` is the built-in registration shim.
- `sources/` contains trusted-sender persistence, normalization, and migrations.

## Hooks

Message rendering checks whether a sender is trusted before allowing remote images. The message API records trust decisions when a user allows images for a sender.
