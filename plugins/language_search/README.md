# Language search plugin

Detects message language during indexing and enables `lang:` search filters.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `plugin.go` is the built-in registration shim.
- `detector/` contains the Lingua-backed detector and tests.
- `schema/` contains plugin-owned migration declarations.

## Hooks

Sync, compose, and search indexing call the detector only when language search is enabled. Search query parsing normalizes `lang:` filters through the same detector package.
