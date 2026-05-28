# BIMI brand icons plugin

Fetches and caches verified BIMI SVG logos for sender domains.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `plugin.go` is the built-in registration shim.
- `bimi/` contains DNS lookup, SVG validation, cache persistence, migrations, and tests.

## Hooks

Message presentation asks the plugin for sender-domain asset URLs. Plugin routes fetch and cache BIMI logos under `/plugins/bimi_brand_icons/brand-icons/:domain.svg`.
