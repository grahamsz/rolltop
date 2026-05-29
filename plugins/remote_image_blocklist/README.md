# Remote image blocklist plugin

Blocks remote tracking images and allows admin-maintained URL block patterns.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `backend/` contains the runtime Go plugin entrypoint and registration shim.
- `frontend/` contains the browser UI helpers.
- `frontend_dist/` is the generated browser bundle emitted by `npm run build:plugins`.
- `rules/` contains default patterns, admin persistence helpers, and migrations.

## Hooks

`backend/register.go` registers the plugin definition, migrations, and hook adapter. `backend/main.go` exports `RolltopPlugin()` and adapts the remote-image blocklist hook methods:

- `ListRemoteImageRules`
- `ListRemoteImagePatterns`
- `ReplaceRemoteImageRules`

Message rendering consults enabled blocklist patterns before loading remote images. Admin plugin routes read and replace the rule set. The frontend bundle exposes `RemoteImageNotice` and `AdminRemoteImageBlocklist`.

## Build

```sh
npm run build:plugins
GOCACHE=/tmp/rolltop-go-build go build -buildmode=plugin -o plugins/remote_image_blocklist/backend/remote_image_blocklist.so ./plugins/remote_image_blocklist/backend
```
