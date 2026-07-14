# Named-rule scorecard training

Rolltop's experimental filter is a native Go named-rule scorecard. It is not
Apache SpamAssassin and does not embed or invoke SpamAssassin. The Apache public
corpus supplies historical ham/spam labels used to mass-check Rolltop's authored
rules and fit their bounded scores.

Runtime and CI are offline. They load the compact checked-in `model.bin` and do
not download mail, generate rules, or train weights. A maintainer refresh is an
explicit three-command operation from the repository root:

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

`download` fetches five pinned archives from the official
[SpamAssassin public corpus](https://spamassassin.apache.org/old/publiccorpus/),
checks their hard-coded SHA-256 values, and safely extracts them under the
ignored cache. Never inject these historical messages into a live mail system;
message copyright remains with the original senders.

`train` uses Rolltop's production MIME parser, discards decoded attachment
bodies after recording MIME types, deduplicates messages, and makes a stable
70/15/15 split inside each dated archive. It then:

1. evaluates the fixed table of no more than 128 explicitly named rules;
2. audits each rule's spam and ham hit frequency, disabling low-support or
   polarity-contradicting rules;
3. fits scores with a fixed-seed, bounded sigmoid perceptron using a ham
   preference and per-epoch weight decay;
4. calibrates the resulting additive score on validation data;
5. selects a benchmark threshold on validation data under a 2% false-positive
   budget, then reports recall and false positives once on held-out data; and
6. writes the 128-score binary, provenance metadata, rule-frequency audit,
   behavior checks, and held-out report.

This follows the useful shape of SpamAssassin's
[`mass-check`](https://cwiki.apache.org/confluence/display/SPAMASSASSIN/MassCheck)
and [perceptron score-fitting](https://svn.apache.org/repos/asf/spamassassin/trunk/masses/README.perceptron)
workflow without claiming compatible rules or scores.

The numeric optimizer settings are also Rolltop-specific. Historical
SpamAssassin perceptron defaults were 15 epochs, learning rate 2.0, ham
preference 2.0, and weight decay 1.0. Rolltop pins 30, 1.0, 0.5, and 0.995
because its update has different semantics: it uses a bounded sigmoid gradient,
normalizes by rule-hit mass, expresses ham preference as bounded row repetition,
and calibrates probabilities afterward. A direct substitution of the historical
numbers on this pinned split produced 0.561 held-out recall and a 0.012
false-positive rate at the validation-selected threshold, missing Rolltop's 0.60
recall gate; the checked-in recipe produces 0.691 and 0.017. This comparison
explains why the numbers are not copied verbatim; it is not a claim that the
Rolltop optimizer is equivalent or generally superior. Future parameter changes
must be chosen on validation data before the held-out split is evaluated.

The validation-selected threshold in `benchmark.json` is an evaluation
operating point only. It does not change product presentation. Runtime/UI bands
remain fixed and conservative: medium at probability 0.35 and high at 0.90.
The report includes held-out confusion counts, recall, and false-positive rate
at both fixed display thresholds as well as at the research operating point.

`verify` reads only checked-in artifacts. It validates checksums, identity,
manifest ordering, score signs and ranges, rule audit completeness, held-out
false-positive/recall gates, display thresholds, and golden behavior including
the `AsWeMove`/`movie` substring regression. Weight refreshes should be reviewed
as artifact diffs; raw mail, mass-check rows, and split files must not be added
to Git.

`train` also runs a generous local timing and allocation smoke check before it
writes artifacts. Those machine-dependent observations are printed only; they
are deliberately absent from `benchmark.json` and its quality gates. With a
pinned corpus and toolchain, `model.bin`, `model.json`, and `benchmark.json` are
therefore byte-for-byte reproducible across training runs.
