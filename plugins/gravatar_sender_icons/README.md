# Gravatar sender icons plugin

Optionally proxies and caches Gravatar images for sender email addresses.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `backend/` contains the runtime Go plugin entrypoint and registration shim.
- `frontend/` contains the browser helper module.
- `frontend_dist/` is the generated browser bundle emitted by `npm run build:plugins`.
- `gravatar/` contains hash normalization, cache persistence, URL helpers, and migrations.

## Hooks

`backend/register.go` registers the plugin definition, migrations, and hook adapter. `backend/main.go` exports `RolltopPlugin()` and adapts the Gravatar hook methods:

- `NormalizeHash`
- `AssetURL`
- `Hash`
- `FetchURL`
- `ErrorTTL`
- `MissingTTL`
- `PositiveTTL`
- `MaxImageBytes`
- `GetImage`
- `UpsertImage`

Message presentation asks the plugin for sender email image URLs. Plugin routes fetch and serve cached avatars under `/plugins/gravatar_sender_icons/avatar/:hash`.

## Build

```sh
npm run build:plugins
GOCACHE=/tmp/rolltop-go-build go build -buildmode=plugin -o plugins/gravatar_sender_icons/backend/gravatar_sender_icons.so ./plugins/gravatar_sender_icons/backend
```
