# MailMirror Security Audit

Date: 2026-05-23

## Scope

Reviewed the V1 local web app security boundary: authentication, sessions, CSRF, user scoping, blob/attachment/message access, IMAP credential handling, sync mutations, search isolation, and browser-facing event updates.

## Findings Addressed

- Browser sync updates now use an authenticated server-sent event stream at `/api/events`, so the client no longer polls `/api/sync/status` and `/api/bootstrap` every few seconds.
- Message move and remote-delete reconciliation now remove the local message, search document, and associated raw blob row/file where present, preventing stale raw message downloads after the message leaves the folder.
- Sync progress events are user-scoped on the server. The browser never sends `user_id`; the event stream derives the user from the server-side session.
- Remote folder reconciliation now compares local UIDs with IMAP UIDs after each folder sync, so remote deletes and moves out of a folder are reflected locally.
- Full body storage has been reduced: SQLite keeps previews, Bleve indexes searchable text, and raw blobs are retained according to `MAILMIRROR_BLOB_RETENTION`.

## Verified Controls

- App passwords use Argon2id hashing.
- Session cookies are opaque random tokens; SQLite stores token hashes only.
- Normal routes derive the current user from the server-side session and do not accept browser-provided `user_id`.
- Message, attachment, blob, search, sync-run, mailbox, and account lookups include `user_id` constraints.
- POST routes use CSRF verification with an HttpOnly CSRF base cookie/session-derived token.
- IMAP and SMTP passwords are encrypted at rest with `MAILMIRROR_MASTER_KEY`.
- Logs include user IDs, mailbox names, and sync status only; they do not log app passwords, IMAP/SMTP passwords, session tokens, or raw message bodies.
- Search always includes a mandatory `user_id` term.

## Residual Risks

- Legacy server-rendered pages still require `'unsafe-inline'` in CSP for inline styles/scripts. The React SPA path is cleaner; removing the legacy templates or moving their inline behavior into static assets would allow a stricter CSP.
- Showing remote images intentionally allows network requests to third-party image hosts. The default remains blocked per message unless the user clicks Show images or trusts the sender.
- IMAP UID reconciliation uses `UID SEARCH 1:*`; very large folders may make that step noticeable. It is correct, but future work should support chunked reconciliation or provider-specific MODSEQ/QRESYNC where available.
- Local administrators can create users but V1 still relies on filesystem/container access controls to protect `/data` from OS-level access.

## Next Hardening Targets

- Add rate limiting for login/setup/admin password creation endpoints.
- Add optional session listing and session revocation.
- Replace legacy inline-template JavaScript so `script-src` can drop `'unsafe-inline'`.
- Add automated route-level authorization tests for every `/api/messages`, `/attachments`, `/blobs`, and `/sync-runs` path.
