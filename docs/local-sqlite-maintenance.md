# Local SQLite Maintenance

This Rolltop deployment is currently the only running instance of this codebase.
One-time local SQLite changes are acceptable when they simplify plugin-table
migrations or data cleanup.

Before making a one-time local database change:

1. Stop the app.
2. Copy the SQLite database to a timestamped backup.
3. Prefer a checked-in migration when the change should be repeatable.
4. Document any manual-only cleanup in this file or the related change notes.

Disabling a plugin should not drop its data. Data removal should remain an
explicit admin maintenance action.

## Tenant Search Index Reset

Use the checked-in offline command instead of deleting Bleve files manually:

```sh
rolltop reset-search --user-id 1 --confirm-offline
```

The command requires a positive numeric local user ID, takes the same
data-directory lock as the server, and refuses to proceed without an explicit
offline confirmation. It marks only that user's messages in search-visible
mailboxes pending, then atomically renames only
`data/users/<user-id>/bleve`. It does not contact IMAP or delete message,
attachment, or blob records.

Keep the timestamped quarantine directory until the replacement index has been
verified. The command prints both paths; before restarting Rolltop, the old
index can be restored by renaming the quarantine path back to `bleve`.

This command repairs only derived search state. It cannot restore a message
whose SQLite row is missing. Deploy mailbox recovery and let the local row count
reach the remote mailbox count first; use `reset-search` only if Bleve repair is
still stalled. After restart, indexing may retrieve raw messages from IMAP when
the local `.eml` retention window has already expired.

## Mailbox Recovery Status

Generation recovery is serialized to one active mailbox per user. A queue line
lists every durable marker, the active target, and up to eight waiting targets:

```text
recover mailbox generation queue user_id=1 pending_markers=3 active_target={account_id=1 mailbox="Gmail Forward"} queued_targets=[{account_id=1 mailbox="Archive"} {account_id=3 mailbox="INBOX"}] other_mailbox_work_active=false
```

The active turn emits a heartbeat every 15 seconds with its current phase, UID,
durable checkpoint, fetched/stored counts, and the same queue summary. An
unchanged queue is also repeated every two minutes. During recovery, at most one
unrelated live Inbox sync may write alongside the recovery turn; later Inbox
polls log that another live Inbox writer is already running. The
`other_mailbox_work_active` field is true for that concurrent reservation and
for other user-scoped mail work that currently blocks the next recovery turn,
including account-wide sync planning, foreground plugin operations, sender
statistics, and an attachment-index worker that is still exiting.

Remote IMAP migration routines log a start line, a 30-second progress heartbeat,
and a terminal completed, deferred, failed, or canceled line. If mailbox
generation recovery pauses a migration, separate pause heartbeats identify that
wait, so an IDLE subscription is not mistaken for an actively copying routine.
Long migrations release their foreground reservation every 25 scanned messages,
allow a destination mailbox refresh to run, and then reacquire it for the next
bounded turn.
