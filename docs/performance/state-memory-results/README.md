# Measurement files

- `service-runs.json`: five 200-acceptance processes per side and case, including parsed Go benchmark metrics and whole-process rusage.
- `service-process-and-service.csv`: the same acceptance/process metrics as a flat table.
- `state-runs.json.gz`: unmodified runner JSON, compressed, including all per-ledger samples, root/read digests and process counters.
- `state-per-run.csv`: state-run metrics derived from the raw samples.
- `state-summary.csv`: medians of five per-run values per side; change is `(after / before - 1) * 100`. An empty change cell means the baseline is zero.
- `service-metadata.json` and `state-metadata.json`: versions, settings, source status and binary hashes.
- `raw-logs.tar.gz`: all 60 original process logs and the two state build logs under `service/` and `state/`. Service build logs were not retained. Log filenames referenced by each run resolve within its suite directory.

For state runs, mean phase latency is the arithmetic mean of per-ledger phase samples; throughput is the inverse mean total latency. Quantiles use nearest rank within each run, then the report takes the median of the five per-run quantiles. Allocation counts/bytes and logical Family operations divide the measured-run deltas by the number of measured ledgers. Last-quarter heap is the median of HeapAlloc samples in the last quarter of each run, then the median of those five values. It is not post-GC live heap. Peak RSS and process CPU include fixture setup/teardown. Account peaks and final database sizes also include setup; directory sizes do not measure physical I/O.

Absolute workspace paths in recorded commands identify the original environment; use the reproduction instructions in [the report](../state-memory.md) on another machine. Reported timings are descriptive observations on an interactive laptop, not confidence intervals or production sizing guarantees.
