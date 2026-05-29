# One-click unsubscribe plugin

Detects RFC 8058 unsubscribe links and sends one-click unsubscribe requests.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `backend/` contains the runtime Go plugin entrypoint and registration shim.
- `frontend/` contains the browser UI helper module.
- `frontend_dist/` is the generated browser bundle emitted by `npm run build:plugins`.
- `history/` contains send-history persistence, sender normalization, and migrations.

## Hooks

`backend/register.go` registers the plugin definition, migrations, and hook adapter. `backend/main.go` exports `RolltopPlugin()` and adapts the one-click hook methods:

- `LatestOneClickSend`
- `RecordOneClickSend`

Message details expose unsubscribe actions when matching headers are present. The plugin records recent sends so the UI can avoid duplicate one-click requests. The frontend bundle exposes `OneClickUnsubscribeInlineAction` and `OneClickUnsubscribeMenuAction`.

## Build

```sh
npm run build:plugins
GOCACHE=/tmp/rolltop-go-build go build -buildmode=plugin -o plugins/one_click_unsubscribe/backend/one_click_unsubscribe.so ./plugins/one_click_unsubscribe/backend
```
