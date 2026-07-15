# Mail filters plugin

Adds search-driven filtering for Rolltop mail.

## Structure

- `manifest.json` declares the runtime backend binary and frontend bundle.
- `backend/` contains the Go plugin entry point, rule engine, protected API routes, and stored-message hook.
- `frontend/` contains the settings UI source.
- `frontend_dist/` contains generated browser assets from `npm run build:plugins`.
- `migrations/user/` contains plugin-owned, user-scoped SQLite tables.

## Behavior

Filters use Rolltop search syntax, including relative age filters such as:

```text
from:studio@example.com older_than:7d
```

Actions can star, move to a folder or source-account Trash, and forward through the user's configured SMTP identity. Forwarded mail receives an opaque `X-Rolltop-Forwarded-By` header, and the plugin refuses to forward a message that already has the same marker.

Every rule evaluation is recorded for 30 days, including matches, misses, skipped account scope, scheduled age checks, action failures, and loop prevention.

## Build

```sh
go build -buildmode=plugin -o plugins/mail_filters/backend/mail_filters.so ./plugins/mail_filters/backend
npm run build:plugins
```

## Hook Locations

- Backend ABI: `backend/plugins/backend.go`
- Sync dispatch: `backend/syncer/autocrypt.go`
- Stored-message call site: `backend/syncer/syncer.go`
- Frontend settings route: `/settings/account/plugins/filters`

## Notes

Relative `older_than:` filters schedule messages when they match the non-age part of the query but have not crossed the age threshold yet. A lightweight plugin worker processes due scheduled rows every 15 minutes while the backend plugin is enabled.
