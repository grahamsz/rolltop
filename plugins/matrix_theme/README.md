# Matrix theme plugin

Adds the optional Matrix interface theme.

## Layout

- `manifest.json` declares the plugin and its theme bundle.
- `frontend/` contains the small runtime module bundle.
- `frontend_dist/` is the generated browser bundle emitted by `npm run build:plugins`.
- `themes/matrix/theme.scss` is the authored stylesheet source.
- `frontend_dist/themes/matrix/theme.css` is the generated asset served at runtime.

## Hooks

The theme loader exposes declared theme CSS when the plugin is enabled. This plugin has no Go backend package and no database migrations.

## Build

```sh
npm run build:plugins
```
