# Remote image blocklist plugin

Blocks remote tracking images and allows admin-maintained URL block patterns.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `plugin.go` is the built-in registration shim.
- `rules/` contains default patterns, admin persistence helpers, and migrations.

## Hooks

Message rendering consults enabled blocklist patterns before loading remote images. Admin plugin routes read and replace the rule set.
