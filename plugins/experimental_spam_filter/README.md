# Experimental spam filter

This disabled-by-default plugin adds advisory spam-risk scoring to Rolltop. It combines a checked-in, corpus-trained logistic model with two tenant-local Bleve signals:

- Explicit **Spam** and **Not spam** feedback is strong personalization evidence.
- Similar messages that are currently read and dated within the last 90 days are weak ham evidence, capped so they can reduce a score by no more than 15%.

The plugin only stores probabilities, bounded explanations, feedback labels, and backfill progress in each user's database. It does not store another copy of message bodies. It never sends mail or moves, archives, hides, deletes, or changes the remote state of a message.

## Runtime behavior

New messages are scored after their Bleve batch commits. Classification is best-effort: a model or similarity failure does not fail message import or incremental sync. The corpus score remains usable when there are too few tenant-local neighbors or similarity is unavailable.

The browser API is under `/api/plugins/experimental_spam_filter`. All routes derive the tenant from the authenticated session, and every mutation requires Rolltop's CSRF token. Browser payloads never accept `user_id`.

Risk bands default to:

- low: below `0.35`
- medium: `0.35` through below `0.80`
- high: `0.80` and above

Explicit feedback overrides the displayed band for that message without rewriting the underlying model evidence.

## Training and checked-in weights

The reproducible trainer lives beside the runtime model. Its normal workflow is:

```sh
go run ./plugins/experimental_spam_filter/training/cmd/spamtrain download \
  --cache .cache/rolltop-spam-corpus

go run ./plugins/experimental_spam_filter/training/cmd/spamtrain train \
  --cache .cache/rolltop-spam-corpus \
  --model plugins/experimental_spam_filter/model/model.bin \
  --metadata plugins/experimental_spam_filter/model/model.json \
  --report plugins/experimental_spam_filter/model/benchmark.json

go run ./plugins/experimental_spam_filter/training/cmd/spamtrain verify \
  --model plugins/experimental_spam_filter/model/model.bin \
  --metadata plugins/experimental_spam_filter/model/model.json \
  --report plugins/experimental_spam_filter/model/benchmark.json
```

Only derived weights, provenance, and aggregate benchmark results are checked in. Corpus archives, extracted messages, and feature dumps stay under the ignored cache directory. Normal builds and GitHub Actions run `verify`; they do not download or retrain the corpus.

## Development checks

```sh
go test ./plugins/experimental_spam_filter/...
CGO_ENABLED=1 go build -buildmode=plugin -o plugins/experimental_spam_filter/backend/experimental_spam_filter.so ./plugins/experimental_spam_filter/backend
ROLLTOP_PLUGIN_TARGET=experimental_spam_filter vite build --config vite.plugins.config.ts
go test ./...
```
