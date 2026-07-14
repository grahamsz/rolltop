# Spam model training

The experimental spam filter loads checked-in, derived weights at runtime. It
does not download or train in normal builds, tests, plugin startup, or CI.

From the repository root, explicitly refresh the corpus cache and artifacts:

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

`download` fetches five pinned archives from the official Apache SpamAssassin
public corpus, verifies their hard-coded SHA-256 checksums, and safely extracts
them below the ignored cache. `train` deduplicates the messages, performs an
ordered 70/15/15 split within each dated archive, trains the FTRL logistic model
and a multinomial naive-Bayes baseline, calibrates on validation data, and writes
artifacts only after all quality gates pass. `verify` is offline and is the only
command CI should run.

Corpus messages are parsed with Rolltop's production `backend/mailparse` MIME
pipeline before feature extraction. Encoded text and headers therefore match
runtime classification, while decoded attachment bodies are discarded after
their MIME types are recorded as metadata.

The baseline uses standard multinomial naive Bayes with Laplace smoothing
(`alpha=1`), unsigned hashing, and only feature buckets observed in training as
its vocabulary. It consumes the same bounded normalized feature families as the
logistic model; unseen test features are ignored rather than assigned artificial
probability mass. Score ties are evaluated as a complete threshold group, so
metrics do not depend on corpus iteration order.

Do not add the archive, extracted messages, or feature dumps to Git. Copyright
in corpus messages remains with their original senders. Do not inject corpus
mail into any live mail system; train from these local files only. See the
[SpamAssassin corpus README](https://spamassassin.apache.org/old/publiccorpus/readme.html).
