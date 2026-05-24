# MailMirror V1

MailMirror is a single-container Go app that mirrors one IMAP account per local user into local storage for Gmail-like search, viewing, composing, and mailbox moves.

V1 stores:

- SQLite metadata at `/data/mailmirror.db`
- Bleve search index at `/data/bleve`
- Raw `.eml` and attachment blobs under `/data/blobs/users/{user_id}/...`
- Incremental sync progress in `sync_runs`, updated while each folder is processed.
- A compiled React + Vite + TypeScript frontend served by the Go process.

## Security Model

- Browser routes derive the current user from a server-side session.
- Normal user routes never accept `user_id` from browser input.
- Sessions use opaque random tokens; only SHA-256 token hashes are stored in SQLite.
- Cookies are `HttpOnly` and `SameSite=Lax`.
- POST routes require CSRF tokens.
- App passwords are hashed with Argon2id.
- IMAP passwords are encrypted at rest with `MAILMIRROR_MASTER_KEY`.
- Admins can create users, but V1 does not give admins access to other users' mail.
- Message sending uses the configured SMTP server.
- Mailbox moves are explicit user actions and are mirrored to IMAP.
- Read-state sync may update only the IMAP `\Seen` flag when a message is read locally.

## Configuration

Required:

```sh
export MAILMIRROR_MASTER_KEY="$(openssl rand -base64 32)"
```

Common optional variables:

```sh
export MAILMIRROR_ADDR=":8080"
export MAILMIRROR_DATA_DIR="/data"
export MAILMIRROR_DB_PATH="/data/mailmirror.db"
export MAILMIRROR_INDEX_PATH="/data/bleve"
export MAILMIRROR_SESSION_TTL="720h"
export MAILMIRROR_SYNC_INTERVAL="15m"
export MAILMIRROR_INBOX_POLL_INTERVAL="1m"
export MAILMIRROR_BLOB_RETENTION="336h"
export MAILMIRROR_COOKIE_SECURE="false"
export MAILMIRROR_WEBHOOK_TOKEN=""
```

Use `MAILMIRROR_COOKIE_SECURE=true` when serving over HTTPS.

## Run Locally

```sh
npm install
npm run build
go test ./...
MAILMIRROR_MASTER_KEY="$(openssl rand -base64 32)" MAILMIRROR_DATA_DIR="./data" go run ./cmd/mailmirror
```

Open `http://localhost:8080`. If no users exist, `/setup` creates the first admin.

## Docker

```sh
docker build -t mailmirror:dev .
docker run --rm -p 8080:8080 \
  -e MAILMIRROR_MASTER_KEY="$(openssl rand -base64 32)" \
  -e MAILMIRROR_COOKIE_SECURE=false \
  -v mailmirror-data:/data \
  mailmirror:dev
```

Keep the same `MAILMIRROR_MASTER_KEY` for the lifetime of the data volume. Changing it makes stored IMAP passwords undecryptable.

## V1 Flow

1. First admin creates the initial account at `/setup`.
2. Admin creates additional local users at `/admin/users`.
3. Each user logs in and configures their own IMAP account at `/settings/account`.
4. The user clicks `Sync now`, chooses per-folder `auto`, `manual`, or `never`, or scheduled sync runs on `MAILMIRROR_SYNC_INTERVAL`.
5. Sync runs are planned per mailbox, with INBOX prioritized before background folders. Each mailbox task estimates pending work from IMAP `STATUS`, streams messages in UID batches, and updates current folder, UID, seen, total, stored, and skipped counts.
6. Message bodies, attachment names, and searchable text-like attachments are indexed with the current user's `user_id`.
7. SQLite stores compact body previews; full body search lives in Bleve and message display uses the local raw `.eml` or fetches the message from IMAP by UID when the raw blob has aged out.
8. Raw `.eml` blobs are retained for `MAILMIRROR_BLOB_RETENTION` only, defaulting to 14 days. Set it to `0` to keep all raw blobs.
9. Attachment bytes are read from the raw `.eml` while indexing and are not stored as separate blobs for new syncs.
10. `/mail`, folder views, `/search`, and `/messages/{id}` only return current-user records.
11. Folder counts show unread messages.
12. Dragging a message onto a folder immediately removes it from the current view, shows a moving toast, and then applies the IMAP move.

In account settings, `Folder scope` can be:

- `INBOX` for only inbox.
- `INBOX,Sent` for a comma-separated subset.
- `*` for all selectable IMAP folders.

Search supports Gmail-style operators:

- `has:attachment`
- `filename:pdf` or `filename:"report.csv"`
- `is:read`
- `is:unread`

The web app is installable as a limited offline PWA. It caches the shell and recent GET API responses, so previously opened mail/search views can render when offline. Browser notifications can be enabled from the top bar; these are local PWA notifications driven by the app's authenticated server-sent event stream, not VAPID/web-push delivery from a remote push service. Notifications are only counted for recent INBOX arrivals after the mailbox has already completed an initial sync, so archive/backfill syncs do not create browser popups.

MailMirror uses IMAP `IDLE` for INBOX wakeups when the server supports it and keeps the scheduled INBOX poll as a fallback. Remote deletes and moves are reconciled after folder syncs by comparing local UIDs with the server's current UID set.

See `SECURITY_AUDIT.md` for the current route and storage security review.

## Development Checks

```sh
npm run build
go test ./...
docker build -t mailmirror:dev .
```
