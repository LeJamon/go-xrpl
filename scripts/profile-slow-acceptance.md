# Slow acceptance profiling

`profile-slow-acceptance.py` is an operator-side recorder for a future slow
acceptance event. It follows the node's existing log, keeps 5-second CPU
profiles in a bounded 60-second ring, and retains that overlap plus a short
post-event execution trace and goroutine snapshot when it sees a matching
`Consensus acceptance timings` record.

The recorder uses only the loopback pprof server. Start it beside an already
running node:

```text
python3 scripts/profile-slow-acceptance.py \
  --log /path/to/goxrpl.log \
  --output /path/to/issue-2105-profiles \
  --threshold 1s \
  --cooldown 60s
```

The output argument is a parent directory. Each invocation creates a unique
`slow-acceptance-*` child and writes only inside that child. The default limits
are three incidents, 256 MiB of incident artifacts and manifests, and 64 MiB
of ring data, including an in-progress CPU window. In-progress response files
are capped by the remaining incident budget. The incident budget keeps modest
manifest headroom and reserves space for one overlap window per incident while
post captures run; with very small `--max-bytes`, a post capture may be skipped
to preserve those bounds, or a trigger may be ignored if even its manifest
headroom cannot fit.
`--max-bytes` is an accounting cap for completed artifacts with reserved
manifest headroom. Manifest updates use bounded atomic replacement, so the
previous manifest and its temporary replacement can briefly coexist; this is
not a strict instantaneous directory-size cap.
Use `--max-incidents`, `--max-bytes`, and `--ring-bytes` to lower or raise
those bounds. The pprof URL must be explicit loopback HTTP, for example
`--pprof-url http://127.0.0.1:6060`; credentials, remote hosts, and URL paths
are rejected. Stop with Ctrl-C or SIGTERM. The script does not restart or
reconfigure the node. Recorder diagnostics go to stderr; the output directory
contains only profiles, manifests, and temporary recorder-owned files.

The trigger accepts both the text logger form
`msg="Consensus acceptance timings" total=2.12s` and JSON with the same
`msg`/`total` fields. JSON numeric durations follow Go slog's nanosecond
encoding; text durations may be compound values such as `1m2.3s`. Log
rotation and copy-truncate events are followed, and
temporary or failed captures are removed only from the recorder's own output
directory. Do not run this recorder concurrently with
`scripts/profile-recorder.sh` against the same node unless the extra pprof
load is intentional; both issue CPU and trace requests. Each incident manifest
records the acceptance log timestamp, per-artifact start/end times, byte count,
and `complete`, `partial`, or `failed` post-capture status.

The timing record is emitted when acceptance completes. The retained ring may
overlap the acceptance and the post-event trace starts after the log line, so
these artifacts cannot establish an acceptance-start trace or attribute the
historical incident's CPU cause. This change was validated with a fake local
HTTP pprof server; no production node was profiled.
