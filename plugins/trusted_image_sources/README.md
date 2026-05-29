# Trusted image sources plugin

Remembers senders whose remote images may load automatically.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `backend/` contains the runtime Go plugin entrypoint and registration shim.
- `frontend/` contains the browser UI helper module.
- `frontend_dist/` is the generated browser bundle emitted by `npm run build:plugins`.
- `sources/` contains trusted-sender persistence, normalization, and migrations.

## Hooks

`backend/register.go` registers the plugin definition, migrations, and hook adapter. `backend/main.go` exports `RolltopPlugin()` and adapts the trusted-image hook methods:

- `TrustImageSender`
- `IsImageSenderTrusted`

Message rendering checks whether a sender is trusted before allowing remote images. The message API records trust decisions when a user allows images for a sender. The frontend bundle exposes `TrustImageSourceAction`.

## Build

```sh
npm run build:plugins
GOCACHE=/tmp/rolltop-go-build go build -buildmode=plugin -o plugins/trusted_image_sources/backend/trusted_image_sources.so ./plugins/trusted_image_sources/backend
```
