# BIMI brand icons plugin

Fetches, validates, caches, and serves BIMI SVG logos for sender domains.

This is a good plugin to study first because it is small enough to follow end to end, but it still shows the full plugin shape:

- manifest-driven runtime loading
- Go backend plugin registration
- frontend helper bundle
- shared hook wiring into core mail views
- plugin-owned migrations and cache table

## Layout

- `manifest.json` declares the plugin ID, backend binary, and frontend module.
- `backend/` contains the runtime Go plugin entrypoint and registration shim.
- `frontend/` contains the browser helper module used by the core mail UI.
- `frontend_dist/` is the generated browser bundle emitted by `npm run build:plugins`.
- `bimi/` contains BIMI DNS lookup, SVG validation, cache persistence, migrations, and tests.

## What hooks where

Backend startup loads the plugin entrypoint from `backend/main.go` and the registration shim from `backend/register.go`.

- `backend/register.go`
  - `init()` calls `plugins.Register(...)`
  - registers the plugin metadata:
    - `plugins.BIMIBrandIcons`
    - name: `BIMI brand icons`
    - enabled by default
  - registers plugin migrations from `bimi.Migrations()`
  - registers the backend hook adapter with `plugins.RegisterHooks(...)`
- `backend/main.go`
  - exports `RolltopPlugin()`
  - adapts the core `plugins.BIMI*` interfaces to the concrete `bimi` package
  - `NormalizeDomain(...)`
  - `AssetURL(...)`
  - `GetIcon(...)`
  - `UpsertIcon(...)`
  - `Fetch(...)`

The core app consumes the plugin through generic hook interfaces rather than importing `bimi` directly.

Frontend loading starts from `frontend/src/plugins/bimiBrandIcons/index.ts`, which re-exports the plugin module from `plugins/bimi_brand_icons/frontend`.

- `frontend/index.ts`
  - `senderDomain(value)`
  - `brandDomainKeyForThread(thread, plugins)`
  - `loadBrandIconsForDomains(domainKey)`
  - `bimiSenderVisualURL(item, brandIcons, plugins)`

Those helpers are used by the thread/message views to decide:

- which domains to request
- when BIMI is enabled
- which icon URL should win for a sender

## Hook flow

The runtime path is:

1. Core startup reads `plugins/bimi_brand_icons/manifest.json`.
2. The host loads `backend/bimi_brand_icons.so`.
3. `backend/register.go` registers the plugin metadata, migrations, and hook adapter.
4. Core routes ask the plugin for BIMI icons through the generic `plugins.BIMI*` hooks.
5. The frontend module batches sender domains and calls the backend API for brand icons.
6. The core message UI uses the returned icon URL when rendering sender visuals.

The backend API path used by the plugin is:

- `/api/brand-icons?domain=<domain>`

The cached asset path served by the plugin is:

- `/plugins/bimi_brand_icons/brand-icons/:domain.svg`

## Exact build steps

Build the frontend bundle and the plugin `.so` from the repository root:

```sh
npm run build:plugins
GOCACHE=/tmp/rolltop-go-build go build -buildmode=plugin -o plugins/bimi_brand_icons/backend/bimi_brand_icons.so ./plugins/bimi_brand_icons/backend
```

Build only the frontend bundle if you are iterating on UI helpers:

```sh
ROLLTOP_PLUGIN_TARGET=bimi_brand_icons vite build --config vite.plugins.config.ts
```

Build only the Go plugin if you are iterating on backend behavior:

```sh
GOCACHE=/tmp/rolltop-go-build go build -buildmode=plugin -o plugins/bimi_brand_icons/backend/bimi_brand_icons.so ./plugins/bimi_brand_icons/backend
```

Run the focused tests:

```sh
go test -count=1 ./backend/store ./backend/web
go test ./plugins/bimi_brand_icons/...
```

Run the full project checks:

```sh
go test ./...
npm run typecheck
```

## Files to read first

- `plugins/bimi_brand_icons/backend/register.go`
- `plugins/bimi_brand_icons/backend/main.go`
- `plugins/bimi_brand_icons/frontend/index.ts`
- `plugins/bimi_brand_icons/bimi/bimi.go`
- `plugins/bimi_brand_icons/bimi/migrations.go`
- `plugins/bimi_brand_icons/manifest.json`

## Notes

- This plugin is backend-heavy. The frontend side is a small helper bundle, not a large UI.
- The source bundle lives in `frontend/`; the checked-in build artifact lives in `frontend_dist/`.
- `frontend/src/plugins/bimiBrandIcons/index.ts` stays as the stable import path for the main app.
