# OIDC Sign-In Plugin

Adds OpenID Connect sign-in for Rolltop users. Enable the plugin in Admin settings, then configure the provider with environment variables.

## Environment

```sh
ROLLTOP_OIDC_ISSUER=https://issuer.example.com
ROLLTOP_OIDC_CLIENT_ID=rolltop
ROLLTOP_OIDC_CLIENT_SECRET=...
ROLLTOP_OIDC_REDIRECT_URL=https://mail.example.com/api/plugins/oidc/callback
ROLLTOP_OIDC_NAME=OIDC
ROLLTOP_OIDC_SCOPES="openid email profile"
ROLLTOP_OIDC_ALLOWED_DOMAINS=example.com
ROLLTOP_OIDC_ALLOWED_EMAILS=person@example.com
ROLLTOP_OIDC_AUTO_CREATE=false
```

`ROLLTOP_OIDC_REDIRECT_URL` is optional when reverse-proxy headers provide the correct public scheme and host. The plugin derives `/api/plugins/oidc/callback`.

By default, OIDC sign-in only works for existing Rolltop users whose email matches the verified OIDC email claim. Set `ROLLTOP_OIDC_AUTO_CREATE=true` to create users automatically; the first auto-created user becomes admin.
