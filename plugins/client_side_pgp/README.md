# Client-side PGP Plugin

This plugin adds OpenPGP support while keeping private-key operations in the
browser. The core app owns generic message fields such as `is_encrypted` and
`is_signed`; this plugin owns OpenPGP-specific detection, body transforms,
Autocrypt, key APIs, and UI.

## Layout

- `manifest.json` declares the plugin ID, backend shared object, frontend module,
  and CSS bundle.
- `backend/` builds as a Go plugin with `-buildmode=plugin`.
- `frontend/` builds as a Vite ES module loaded only when the plugin is enabled.
- `schema/` contains the shared plugin migration definitions used by the
  runtime loader and store tests.
- `frontend/styles/pgp.css` contains the PGP UI styles loaded with the frontend module.

## Backend

`backend/main.go` is intentionally small. It exports `RolltopPlugin`, identifies
the plugin, and registers protected API routes in `Start`. `backend/register.go`
registers the plugin metadata and shared migrations:

- `/api/plugins/client_side_pgp/private-keys`
- `/api/plugins/client_side_pgp/private-keys/:id`
- `/api/plugins/client_side_pgp/public-keys`

The rest of the backend is split into thin hook adapters plus implementation
packages:

- `hooks_*.go` implements Rolltop plugin interfaces and delegates to packages.
- `api/` handles private-key and public-key protected API routes.
- `keystore/` contains key persistence, duplicate checks, contact creation, and
  Autocrypt defaulting helpers.
- `identity/` exposes identity key metadata and public-key attachments.
- `attachments/` exposes import actions for small `.asc` public-key attachments.
- `autocrypt/` parses/builds Autocrypt headers and stores discovered sender
  keys.
- `pgpmime/` builds PGP/MIME encrypted and signed message bodies.
- `security/` detects OpenPGP messages and transforms stored/display bodies.

The backend implements these generic Rolltop hook interfaces:

- `BackendPlugin`
- `IdentitySecurityProvider`
- `IdentityAttachmentProvider`
- `OutboundMailHeaderProvider`
- `ComposeMIMEBodyProvider`
- `MessageSecurityProvider`
- `IncomingMessageHook`
- `AttachmentActionProvider`

That means the core mail parser does not need OpenPGP-specific detection logic;
another encryption plugin can implement the same generic hooks and populate the
same core message security fields.

## Frontend

`frontend/index.ts` is the runtime module entry. It exports one plugin object
with API methods, dialogs, crypto helpers, and generic message-security UI hooks.

Frontend source is grouped by responsibility:

- `api/keys.ts` calls plugin-owned protected API routes.
- `components/` contains import, generate, and unlock dialogs.
- `crypto/pgp.ts` wraps OpenPGP.js and DOMPurify work.
- `messageSecurity/` provides list-preview text and list-row indicators through
  the generic frontend message-security hook.
- `storage/browserPGPKeys.ts` stores browser-only private keys in IndexedDB.
- `types.ts` is the stable frontend plugin contract used by core views.

The plugin frontend is bundled with `vite.plugins.config.ts` into
`frontend/dist/index.js`. React is provided by the host runtime shim, while
OpenPGP.js and DOMPurify are bundled into the plugin because they are only needed
when this plugin is enabled.

## Private Key Storage

Private keys can be saved in two modes:

- `server`: the private key is encrypted with `MAILMIRROR_MASTER_KEY` at rest and
  returned to the browser only for local unlock.
- `browser`: only public key metadata is saved server-side; the private key is
  stored in the browser's IndexedDB and must be imported separately in each
  browser.

Passphrases are never sent to the server. Unlock state is shared across tabs via
the service worker window state.

## Build And Test

Build the backend shared object:

```sh
GOCACHE=/tmp/rolltop-go-build go build -buildmode=plugin -o plugins/client_side_pgp/backend/client_side_pgp.so ./plugins/client_side_pgp/backend
```

Build the frontend plugin bundle:

```sh
npm run build:plugins
```

Run the project tests:

```sh
go test ./...
npm run typecheck
```
