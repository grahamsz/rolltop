# Language search plugin

Detects message language during indexing and enables `lang:` search filters.

## Layout

- `manifest.json` declares the plugin for runtime settings.
- `backend/` contains the runtime Go plugin entrypoint and registration shim.
- `frontend/` contains the browser autocomplete helper module.
- `frontend_dist/` is the generated browser bundle emitted by `npm run build:plugins`.
- `detector/` contains the Lingua-backed detector and tests.
- `schema/` contains plugin-owned migration declarations.

## Hooks

`backend/register.go` registers the plugin definition, migrations, and hook adapter. `backend/main.go` exports `RolltopPlugin()` and adapts the language hook methods:

- `DetectLanguage`
- `NormalizeLanguageCode`

Sync, compose, and search indexing call the detector only when language search is enabled. Search query parsing normalizes `lang:` filters through the same detector package. The frontend bundle exposes `languageSearchSuggestions` for search autocomplete.

## Build

```sh
npm run build:plugins
GOCACHE=/tmp/rolltop-go-build go build -buildmode=plugin -o plugins/language_search/backend/language_search.so ./plugins/language_search/backend
```
