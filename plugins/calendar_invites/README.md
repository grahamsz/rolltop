# Calendar invites plugin (prototype)

Detects iCalendar (`METHOD:REQUEST`) invitations in incoming mail and lets the
user answer them from Rolltop.

- **Detection**: an `IncomingMessageHook` MIME-walks each imported message,
  extracts `text/calendar` parts and `.ics` attachments, and records
  invitations per user. Detection never fails the import it runs in.
- **RSVP**: Accept / Tentative / Decline builds an RFC 5546 `METHOD:REPLY`
  object (same UID, echoed SEQUENCE, organizer preserved, only the answering
  attendee with the chosen PARTSTAT) and queues it through the host's durable
  outbox, so delivery, retries, and the Sent copy follow the normal compose
  path. The reply is matched to one of the user's sending identities.
- **CalDAV push (optional)**: pushes the stored invitation to a CalDAV
  calendar with an idempotent `PUT {calendar}/{sha256(UID)}.ics`. Discovery mirrors
  the `carddav_sync` plugin (well-known, principal, calendar-home-set).
  Passwords are encrypted with the Rolltop master key.

Stale SEQUENCE handling: if a newer version of the invitation arrived after
the user opened the page, the RSVP is rejected with a 409 so the user
answers the latest version.

Experimental and disabled by default. Invites are only detected while the
plugin is enabled; mail imported while it was disabled is not backfilled.

The stored invitation retains timezone definitions, recurrence rules, exception
components, location, description, and other original event properties. CalDAV
uploads remove the scheduling `METHOD` property. RSVP retries reuse the durable
outbox submission; repeating the same response does not send another reply.

CalDAV discovery, redirects, and uploads stay on the configured server origin.
Use the final provider-specific CalDAV endpoint if a provider redirects to a
different hostname. Changing the server or username requires re-entering the
password. HTTP is allowed only for loopback servers.

## Module build

The plugin is loaded through the existing plugin manifest and runtime ABI. Its
backend, settings UI, and user migrations live under `plugins/calendar_invites`;
the host only registers the plugin ID, migration catalog, and build targets.
The table remains `plugin_calendar_invites_invites` with a `user_id` scope.

`npm run build` typechecks the UI and builds `frontend_dist/index.js` and
`frontend_dist/styles.css`. Build the backend with the same Go toolchain, CGO
setting, build tags, and `-trimpath` setting as the host executable:

```sh
CGO_ENABLED=1 go build -buildmode=plugin -trimpath -ldflags='-s -w' \
  -o plugins/calendar_invites/backend/calendar_invites.so ./plugins/calendar_invites/backend
```

CI builds and uploads the backend module. The Docker image includes the module,
manifest, and frontend assets, and CI checks that they are present. A Go
integration test builds the real `.so`, loads it through the plugin manager,
and exercises its import hook, user migrations, and route lifecycle.

Enable Calendar invites in the admin plugin settings, then open Calendar invites
under account settings. This is an invitation inbox with optional one-way CalDAV
push, not a local calendar application or two-way calendar sync. It does not
process cancellation notices or backfill previously imported mail. RSVP delivery
requires a sending identity matching an attendee and configured outbox delivery.
