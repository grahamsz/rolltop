# Attachment preview plugin

Adds authenticated browser previews for supported image and PDF attachments.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `plugin.go` is the built-in registration shim.
- `preview/` contains attachment classification, preview metadata, byte limits, and schema declarations.

## Hooks

The web plugin route layer calls `preview.ForAttachment` to add preview actions and serves `/plugins/attachment_preview/attachments/:id/preview` when the plugin is enabled.
