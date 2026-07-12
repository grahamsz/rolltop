# rolltop

rolltop is a single-container Go app that mirrors multiple IMAP inboxes per local user into local storage for search, viewing, composing, and mailbox moves. Production mail data stays in the user's own Docker instance. Project site: https://rolltop.app, coming soon. Contact: graham@rolltop.app.

## What It Stores

- SQLite metadata at `/data/rolltop.db`
- Bleve search index at `/data/bleve`
- Raw `.eml` and attachment blobs under `/data/blobs/users/{user_id}/...`
- Incremental sync progress in `sync_runs`
- A compiled React + Vite + TypeScript frontend served by the Go process

## Security Model

- Browser routes derive the current user from a server-side session.
- Normal user routes never accept `user_id` from browser input.
- Sessions use opaque random tokens; only SHA-256 token hashes are stored in SQLite.
- Cookies are `HttpOnly` and `SameSite=Lax`.
- POST routes require CSRF tokens.
- App passwords are hashed with Argon2id.
- IMAP passwords are encrypted at rest with `ROLLTOP_MASTER_KEY`.
- Admins can create users, but V1 does not give admins access to other users' mail.
- Message sending uses the configured SMTP server.
- Mailbox moves are explicit user actions and are mirrored to IMAP.
- Read-state sync may update only the IMAP `\Seen` flag when a message is read locally.
- Message authentication badges report bounded SPF, DKIM, and DMARC values found in received headers; Rolltop labels their source and does not claim to verify them independently.

## Configuration

Required:

```sh
test -f .env.rolltop || (
  umask 077
  printf 'ROLLTOP_MASTER_KEY=%s\n' "$(openssl rand -base64 32)" > .env.rolltop
)

set -a
. ./.env.rolltop
set +a
```

Common optional variables:

```sh
export ROLLTOP_ADDR=":8080"
export ROLLTOP_DATA_DIR="/data"
export ROLLTOP_DB_PATH="/data/rolltop.db"
export ROLLTOP_INDEX_PATH="/data/bleve"
export ROLLTOP_SESSION_TTL="720h"
export ROLLTOP_SYNC_INTERVAL="15m"
export ROLLTOP_INBOX_POLL_INTERVAL="1m"
export ROLLTOP_BLOB_RETENTION="336h"
export ROLLTOP_COOKIE_SECURE="false"
export ROLLTOP_WEBHOOK_TOKEN=""
```

Set `ROLLTOP_COOKIE_SECURE=true` when serving over HTTPS.

## Run Locally

```sh
npm install
npm run build
npm run build:plugins
go test ./...
test -f .env.rolltop || (
  umask 077
  printf 'ROLLTOP_MASTER_KEY=%s\n' "$(openssl rand -base64 32)" > .env.rolltop
)
set -a
. ./.env.rolltop
set +a
ROLLTOP_DATA_DIR="./data" go run ./cmd/rolltop
```

Open `http://localhost:8080`. If no users exist, `/setup` creates the first admin.

## Docker

```sh
docker pull ghcr.io/grahamsz/rolltop:latest

test -f .env.rolltop || (
  umask 077
  printf 'ROLLTOP_MASTER_KEY=%s\nROLLTOP_COOKIE_SECURE=false\n' "$(openssl rand -base64 32)" > .env.rolltop
)

docker run --rm -p 8080:8080 \
  --env-file .env.rolltop \
  -v rolltop-data:/data \
  ghcr.io/grahamsz/rolltop:latest
```

Keep `.env.rolltop` with the same care as the Docker volume. Changing or losing `ROLLTOP_MASTER_KEY` makes stored IMAP passwords undecryptable.

## V1 Flow

1. First admin creates the initial account at `/setup`.
2. Admin creates additional local users at `/admin/users`.
3. Each user logs in and configures their own IMAP account at `/settings/account`.
4. The user clicks `Sync now`, chooses per-folder `auto`, `manual`, or `never`, or scheduled sync runs on `ROLLTOP_SYNC_INTERVAL`.
5. Sync runs are planned per mailbox, with INBOX prioritized before background folders. Each mailbox task estimates pending work from IMAP `STATUS`, streams messages in UID batches, and updates current folder, UID, seen, total, stored, and skipped counts.
6. Message bodies, attachment names, and searchable text-like attachments are indexed with the current user's `user_id`.
7. SQLite stores compact body previews; full body search lives in Bleve and message display uses the local raw `.eml` or fetches the message from IMAP by UID when the raw blob has aged out.
8. Raw `.eml` blobs are retained for `ROLLTOP_BLOB_RETENTION` only, defaulting to 14 days. Set it to `0` to keep all raw blobs.
9. Attachment bytes are read from the raw `.eml` while indexing and are not stored as separate blobs for new syncs.
10. `/mail`, folder views, `/search`, and `/messages/{id}` only return current-user records.
11. Folder counts show unread messages.
12. Dragging a message onto a folder immediately removes it from the current view, shows a moving toast, and then applies the IMAP move.
13. Snooze is local and conversation-scoped: future snoozes are hidden from normal lists and search, then resurface at the top without moving or deleting remote mail. A genuinely new incremental reply clears the active snooze.

In account settings, `Folder scope` can be:

- `INBOX` for only inbox.
- `INBOX,Sent` for a comma-separated subset.
- `*` for all selectable IMAP folders.

Search supports Gmail-style operators:

- `has:attachment`
- `filename:pdf` or `filename:"report.csv"`
- `is:read`
- `is:unread`

The web app is installable as a limited offline PWA. It caches the shell and a bounded, user-scoped snapshot of the first All Mail page so the most recent mail list can paint immediately while it refreshes. Browser notifications can be enabled from the top bar and use user-scoped VAPID Web Push subscriptions. The Android app uses the same server sender through UnifiedPush, with an embedded Play Services distributor and a 15-minute authenticated poll as fallbacks. Notifications are driven only by durable recent INBOX arrival events after the mailbox has completed its initial sync, so archive/backfill syncs do not create popups.

Mail lists support selection with batch read/unread and snooze actions plus `j`, `k`, and `x` keyboard navigation; `/` focuses search and thread shortcuts cover reply, reply-all, forward, and return-to-list. Compose keeps a bounded, per-user browser recovery copy, supports reusable local templates, and merges saved contacts with recent tenant-scoped correspondents. Recovery never serializes attachment bodies. Thread views show explicitly source-labeled authentication results and conservative sender/link cautions.

rolltop uses IMAP `IDLE` for INBOX wakeups when the server supports it and keeps the scheduled INBOX poll as a fallback. Remote deletes and moves are reconciled after folder syncs by comparing local UIDs with the server's current UID set.

## License And Contributions

rolltop is intended to be distributed under the AGPL-3.0-or-later. By contributing code, documentation, assets, or other original work to this repository, you agree to license that contribution under AGPL-3.0-or-later unless you have a separate written agreement with the project owner.

## Development Checks

```sh
npm run build
npm run build:plugins
go test ./...
docker build -t rolltop:dev .
```
