# Measurement files

The measured optimized revision is `8f6b85ac9`; the old engine is `546e974024a77d9dcf80bc07cd4e86930467fff1`. All three suites use these production revisions. The old worktree contains only the archived test harness adaptations. Metadata records exact status, patches and source/binary hashes.

- `service-runs.json`: five 200-acceptance processes per side and case, with Go benchmark metrics and whole-process rusage.
- `cache-runs.json`: five 50-acceptance processes per side and case with 32 MiB total cache; configuration is in `cache-manifest.json`.
- `service-process-and-service.csv` and `cache-process-and-service.csv`: the same metrics as flat tables.
- `retained-runs.json.gz`, `retained-per-run.csv`, `retained-metadata.json` and `retained-manifest.json`: three pairs of the larger retained-state case, using the same aggregation definitions. `retained-heap.svg` shows the per-ledger median and observed range of those three runs.
- `state-runs.json.gz`: unmodified runner JSON, compressed, with all per-ledger samples, root/read digests and process counters.
- `state-per-run.csv`: state metrics derived from each run.
- `state-summary.csv`: medians of five per-run values per side; change is `(after / before - 1) * 100`. An empty change cell means the baseline is zero.
- `*-metadata.json`: versions, manifests, source status, patches' hashes and binary hashes.
- `before-harness.tar.gz` and `cache-before-harness.tar.gz`: exact baseline test sources, to install in a separate old worktree.
- `raw-evidence.tar.gz`: all process/build logs, exact before/after harnesses, worktree patches and original metadata, under `service/`, `state/`, `cache/` and `retained/`. Run log names resolve within their suite directory.

State mean phase latency is the arithmetic mean of per-ledger samples. Throughput is the inverse mean total latency. Quantiles use nearest rank within each run; the report takes the median of five per-run quantiles. Allocation counts/bytes and logical Family operations divide measured-run deltas by measured ledger count. Last-quarter heap is the median of HeapAlloc samples in each run's last quarter, then the median of those five values; it is not post-GC live heap. Peak RSS and process CPU include setup/teardown. Account peaks and final database sizes include setup; file sizes do not measure physical I/O.

Absolute paths identify the original environment. Follow [the report](../state-memory.md) to reproduce elsewhere. Timings are descriptive observations on an interactive laptop, not confidence intervals or production sizing guarantees. Preserve the initial measurements under `state-memory-results/`; these files are a separate paired comparison.
