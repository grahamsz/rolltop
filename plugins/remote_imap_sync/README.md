# Remote IMAP sync plugin

Copies messages one way from a remote IMAP folder into a folder on an existing Rolltop-connected IMAP account.

## Behavior

- Each signed-in user can create multiple source-to-destination routines.
- Source credentials are encrypted with `ROLLTOP_MASTER_KEY` and never returned by the API.
- Gmail sources use `imap.gmail.com` with an app password. OAuth/XOAUTH2 is not supported yet.
- Initial and periodic runs are incremental. Enabled routines also use IMAP IDLE when the source supports it.
- Messages are appended to the destination without deleting, moving, or changing messages on the source server.
- A transfer ledger makes reconnects and retries idempotent.

## Build

```sh
go build -buildmode=plugin -o plugins/remote_imap_sync/backend/remote_imap_sync.so ./plugins/remote_imap_sync/backend
npm run build:plugins
```

The frontend settings route is `/settings/account/remote-imap-sync`.
