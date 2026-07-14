# Experimental spam filter

This disabled-by-default plugin adds advisory spam-risk scoring to Rolltop. It
is native Go and SQLite: there is no sidecar process and no network work during
ordinary classification. It has three deliberately separate evidence stages:

- A checked-in **named-rule scorecard** runs deterministic header, MIME,
  structure, URI, and phrase tests. An offline mass-check fits only the rule
  scores; it never learns arbitrary character fragments.
- **Personal Bayes** learns token document frequencies from this user's explicit
  **Spam** and **Not spam** feedback plus an optional, user-confirmed snapshot
  of their own recent mail. Explicit labels always override inferred snapshot
  labels. Bayes is inactive until it has 200 messages of each class and is never
  seeded from a public corpus.
- Tenant-local **recent-read similarity** is bounded reputation evidence. Exact
  sender plus a recurring content template can lower risk more than generic
  overlap, but bare sender identity is never an allowlist.

The plugin never sends mail or initiates a move, archive, hide, delete, or other
remote mutation. When the user successfully moves mail into or out of a folder
Rolltop recognizes as Junk, the plugin observes that completed action as strong
explicit feedback; it does not perform the move itself.

## Why the stages are separate

This follows SpamAssassin's architecture more closely than a single model over
all text. SpamAssassin mass-checks authored rule hits and tunes their scores,
while each user's Bayesian database learns that user's verified ham and spam.
Apache specifically warns against training personal Bayes from public mail
streams because it creates misleading token associations.

Rolltop uses Apache's historical public corpus only as a reproducible regression
and rule-score-fitting input. The artifact is a **Rolltop rule scorecard**, not a
SpamAssassin model. Its metadata and benchmark report say exactly which corpus
was used and which held-out checks passed.

## Personal bootstrap from recent mail

The settings page can build an on-demand personal-Bayes snapshot without asking
the user to press a feedback button 400 times. For each selected account, the
user confirms an Inbox and a Spam/Junk folder, previews the broad candidate
counts, and explicitly starts the run.

The snapshot examines the six-month window ending 48 hours before the run:

- Spam candidates come from the confirmed Spam/Junk folder.
- Not-spam candidates must be marked `\Seen`, be at least 48 hours old, and
  belong to an exact sender with at least three read messages spanning 14 days
  and an 80% read rate in the sampled Inbox history.
- Inferred not-spam must also remain below the independent named-rule spam
  threshold and have no strong spam rule. Encrypted and unparseable messages
  are rejected.
- The sample is bounded to 5,000 metadata candidates per folder, 2,000 examined
  not-spam bodies, 20 not-spam messages per sender, and 500 unique messages per
  class. Spam is reduced when necessary so it never outnumbers accepted ham.

The local cache is not assumed to contain six months of bodies. The confirmed
run logs into the existing IMAP account, selects each folder read-only, searches
metadata, and fetches only selected messages with a 512 KiB `BODY.PEEK` limit.
It does not change `\Seen`, move messages, enable the separate experimental IMAP
sync plugin, create local message/search/blob records, or extend Rolltop's body
cache. Fetched bytes are parsed in memory and discarded after tokenization.

Automatic labels form a replaceable snapshot. A completed run atomically
replaces the previous automatic snapshot; cancellation or failure leaves the
previous snapshot intact. **Reset inferred training** removes only automatic
labels. Explicit button feedback and successful user-initiated moves into or out
of recognized Junk folders remain, and take precedence over automatic labels
for the same message fingerprint.

## Runtime and privacy

New messages are scored after their tenant-scoped Bleve batch commits.
Classification is best-effort: a plugin failure cannot fail incremental mail
sync. Browser routes derive `user_id` from the authenticated session and every
mutation requires Rolltop's CSRF token.

Personal Bayes persists tenant-keyed message counts, source/origin metadata, and
hashed token IDs, not a second copy of message text. Learning is idempotent;
changing an explicit label subtracts the old document counts before adding the
new class. Clearing explicit feedback reveals any underlying automatic snapshot
label for that fingerprint. Neither model output nor read state continuously
auto-trains Bayes: inferred training happens only after the user previews and
confirms a bootstrap run.

Recent-read candidates come from authoritative SQLite `is_read` state and use
IMAP `INTERNALDATE` (or local ingestion time for legacy rows), not the
sender-controlled `Date` header. Same-thread/day repetition is collapsed,
explicit spam feedback vetoes wanted-mail evidence, and every adjustment is
bounded in log-odds space.

The browser API is under `/api/plugins/experimental_spam_filter`. Stored
classification explanations contain named rule IDs, bounded probabilities and
counts, and neighbor message IDs/metadata; they do not contain raw bodies.

## Offline mass-check and checked-in weights

Normal builds, plugin startup, and GitHub Actions verify checked-in artifacts;
they do not download or retrain. A maintainer explicitly refreshes them:

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

Only derived rule weights, provenance, and aggregate benchmark results are
checked in. Corpus archives, extracted mail, and sparse mass-check rows stay in
the ignored cache. See the training README for the fitting and validation
details.

## Development checks

```sh
go test ./plugins/experimental_spam_filter/...
CGO_ENABLED=1 go build -buildmode=plugin -o plugins/experimental_spam_filter/backend/experimental_spam_filter.so ./plugins/experimental_spam_filter/backend
ROLLTOP_PLUGIN_TARGET=experimental_spam_filter vite build --config vite.plugins.config.ts
go test ./...
```
