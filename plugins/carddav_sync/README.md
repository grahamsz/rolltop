# CardDAV sync plugin

Syncs contacts one way from a generic CardDAV address book (Nextcloud or any
RFC 6352 server) into Rolltop contacts with incremental updates.

## Behavior

- Each signed-in user can create multiple routines. All rows are scoped by `user_id`.
- Credentials are encrypted with `ROLLTOP_MASTER_KEY` and never returned by the API.
- Discovery follows `/.well-known/carddav` to the principal's `addressbook-home-set`.
- Repeat runs use the `sync-collection` REPORT with the stored sync token; servers
  without sync support fall back to a full multiget with etag comparison.
- vCards without a UID are skipped. Mapping resolves by `(user_id, routine_id, vcard_uid)`
  first, then by matching email the way the core vCard import does.
- Updates merge like the core import: empty scalar fields are filled and child rows
  (emails, phones, addresses, URLs) are unioned. Nothing already in Rolltop is overwritten.
- A deleted vCard removes its sync link, **not the local contact**. Contacts may
  have local edits or links from another routine, even when this plugin originally
  created them. Run history reports these removals as “Unlinked”.
- Auth failures suspend the routine (`needs_credentials`) instead of retrying with a bad password.
- Remote servers require HTTPS; plain HTTP is allowed only for loopback hosts.

## Build

```sh
CGO_ENABLED=1 go build -buildmode=plugin -trimpath -ldflags="-s -w" -o plugins/carddav_sync/backend/carddav_sync.so ./plugins/carddav_sync/backend
ROLLTOP_PLUGIN_TARGET=carddav_sync npx vite build --config vite.plugins.config.ts
npm run build:carddav-sync-css
```

The frontend settings route is `/settings/account/plugins/carddav-sync`.

The plugin is experimental and disabled by default. This is an additive contact
import: existing non-empty names and notes stay unchanged, and old email/phone
values remain when new ones arrive. It does not write to the CardDAV server.
Photos and contact groups are not imported.

Only complete successful listings can unlink missing remote cards. Per-card
failures or omitted multiget results fail the run without advancing its token.
Expired tokens fall back to a fresh listing, and paginated sync reports are
collected before their final token is committed. Full-listing fallback compares
ETags before downloading changed vCards.

Credentials are sent only to the configured origin (scheme, host, and port).
Use the provider's final DAV endpoint if discovery redirects to another host.
Changing the server or username requires entering a password again. Password
rotation on the same source keeps incremental state; changing the source resets
mappings and the token atomically after the active worker has stopped.

`npm run build` typechecks and packages this plugin's JS and CSS. CI and Docker
build `backend/carddav_sync.so`, using the same toolchain and build options as
the host. CI verifies both DAV plugins' backend and frontend files in the image.
Go tests load the actual module and check its protected routes, migrations,
tenant boundaries, and shutdown. Protocol tests use local DAV servers; no live
provider account was used for validation.
