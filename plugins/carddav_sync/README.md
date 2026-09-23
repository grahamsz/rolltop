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
- A deleted vCard removes the Rolltop contact only when the plugin created it
  (`owns_contact`); contacts the user made or imported themselves are never deleted.
- Auth failures suspend the routine (`needs_credentials`) instead of retrying with a bad password.
- Remote servers require HTTPS; plain HTTP is allowed only for loopback hosts.

## Build

```sh
go build -buildmode=plugin -o plugins/carddav_sync/backend/carddav_sync.so ./plugins/carddav_sync/backend
ROLLTOP_PLUGIN_TARGET=carddav_sync npx vite build --config vite.plugins.config.ts
npm run build:carddav-sync-css
```

The frontend settings route is `/settings/account/plugins/carddav-sync`.
