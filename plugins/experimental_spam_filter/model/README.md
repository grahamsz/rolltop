# Rolltop named-rule scorecard

This directory contains the native runtime rule table and its checked-in fitted
scores. Every possible feature has a stable authored ID, description, polarity,
and maximum magnitude in `features.go`. The extractor emits only those named
rules—never hashed words, n-grams, sender domains, URI hosts, or arbitrary token
fragments.

`model.bin` stores 128 bounded floating-point scores plus the intercept and
calibration parameters. `model.json` binds it to the exact rule manifest,
training recipe, corpus checksums, fixed display thresholds, and golden
inference cases. `benchmark.json` records per-rule hit frequencies and scores,
the validation-selected evaluation threshold, held-out recall/false-positive
rate, explicit model limitations, and deterministic quality gates. Local timing
and allocation smoke measurements are printed during training but are not
checksum-bound artifact fields, so all three files are byte-reproducible.

The truthful model identity is **Rolltop named-rule scorecard**. The Apache
SpamAssassin public corpus was used only to mass-check Rolltop rules and fit
their scores; this is not a SpamAssassin model.

See [`../training/README.md`](../training/README.md) for the explicit refresh
and offline verification workflow.
