# Attachment preview plugin

Adds authenticated browser previews for supported image and PDF attachments.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `backend/` contains the runtime Go plugin entrypoint and registration shim.
- `frontend/` contains the browser preview launcher and modal viewer.
- `frontend_dist/` is the generated browser bundle emitted by `npm run build:plugins`.
- `preview/` contains attachment classification, preview metadata, byte limits, and schema declarations.

## Hooks

The backend adapter lives in `backend/main.go` and `backend/register.go`.

- `backend/register.go` registers the plugin definition, migrations, and hook adapter.
- `backend/main.go` exports `RolltopPlugin()` and adapts the attachment-preview hook methods:
  - `PreviewForAttachment`
  - `PreviewKind`
  - `MaxPreviewBytes`
  - `CleanPreviewContentType`
  - `SupportedPreviewImageType`
  - `PreviewImageTypeFromName`
  - `ReadPreviewLimited`

The web plugin route layer calls the hook adapter to add preview actions and serves `/plugins/attachment_preview/attachments/:id/preview` when the plugin is enabled. The frontend bundle exposes `AttachmentPreviewSlot`, `AttachmentPreviewAction`, and `PdfAttachmentViewer`.

## Build

```sh
npm run build:plugins
GOCACHE=/tmp/rolltop-go-build go build -buildmode=plugin -o plugins/attachment_preview/backend/attachment_preview.so ./plugins/attachment_preview/backend
```
