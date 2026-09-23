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
  calendar with an idempotent `PUT {calendar}/{UID}.ics`. Discovery mirrors
  the `carddav_sync` plugin (well-known, principal, calendar-home-set).
  Passwords are encrypted with the Rolltop master key.

Stale SEQUENCE handling: if a newer version of the invitation arrived after
the user opened the page, the RSVP is rejected with a 409 so the user
answers the latest version.

Experimental and disabled by default. Invites are only detected while the
plugin is enabled; mail imported while it was disabled is not backfilled.
