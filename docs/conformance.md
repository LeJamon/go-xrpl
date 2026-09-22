# Conformance

There is no formal XRP Ledger specification. [rippled](https://github.com/XRPLF/rippled),
the C++ reference implementation, *is* the spec — so for go-xrpl, "correct" means
"behaves the way rippled behaves". This document explains how that parity is
verified and how to run the conformance suite.

## rippled as the oracle

Every transaction type, ledger entry, RPC method, and edge case is validated
against rippled's observable behavior: the same field ordering, the same TER
result codes, the same state mutations, the same error conditions. When a subtle
behavior is deliberately mirrored, the Go code cites the rippled source file it
follows (e.g. `Transactor.cpp`, `applySteps.h`), so a reviewer can check the port
against the original.

The pinned local `rippled-worktrees/v3.4.0-oracle/` tree at commit
`4a4fded2eba11427c48ce3f24d9c1aea5e7a9d17` is the working reference. Its
transaction implementations live under `src/libxrpl/tx/transactors/`, headers
under `include/xrpl/tx/transactors/`, ledger code under `src/libxrpl/ledger/`,
protocol definitions under `src/libxrpl/protocol/` and
`include/xrpl/protocol/`, and the upstream unit tests — which the Go conformance
suites mirror — under `src/test/app/`.

## The conformance suite

Conformance tests live in `internal/testing/conformance/` and run rippled-derived
fixtures against the go-xrpl transaction engine and ledger. They are exposed as Go
subtests under `TestConformance/app/<Suite>` and `TestConformance/ledger/<Suite>`.

Final-release conformance requires an explicit v3 corpus recorded from rippled
3.4.0 at the commit above. The corpus manifest pins the oracle repository,
recorder commit, build and amendment configuration, all four
`fixCleanup3_4_0`/`LendingProtocolV1_1` combinations, fixture counts, and every
skip reason. Transaction observations include the execution boundary, symbolic
and numeric TER, applied/queued/fee values, metadata hash, and state root.

Run the corpus with the summary harness:

```bash
just conformance --corpus /path/to/rippled-3.4.0-v3
just conformance --corpus /path/to/rippled-3.4.0-v3 TxQ
just conformance --corpus /path/to/rippled-3.4.0-v3 --failing
just conformance --corpus /path/to/rippled-3.4.0-v3 --list-fail
```

`just conformance` forwards its arguments to
[`scripts/conformance-summary.sh`](../scripts/conformance-summary.sh), so the raw
script accepts the same flags. `GOXRPL_FIXTURES_DIR` may supply the corpus path
instead. A missing, unreadable, empty, stale, malformed, or all-skipped corpus is
a failure; ordinary `go test` skips only the external corpus while still running
the committed harness-contract tests. The suite timeout defaults to `300s` and
can be overridden with the `CONFORMANCE_TIMEOUT` environment variable.

## Reading the results

The summary prints executed pass/fail counts and a per-suite breakdown. Fixtures
declared out of scope are accounted for, with reasons, by the validated manifest
and are not executed:

```
=========================================
 CONFORMANCE SUMMARY
=========================================
 Total:     NNN pass /  NN fail /  NNN  (PP.P%)
 In scope:  NNN pass /   0 fail /  NNN  (100.0%)
=========================================
```

In the per-suite table, suites are colored green (all pass), yellow (partial), or
red (none pass). The in-scope result is the release gate.

## What is intentionally out of scope

Some suites are excluded from the in-scope totals on purpose — they cover features
that are deliberately unimplemented or only partially covered in the current
release target. The authoritative list is
[`scripts/conformance-out-of-scope.txt`](../scripts/conformance-out-of-scope.txt);
removing a line brings that suite back into scope.

As of this writing the out-of-scope suites fall into two groups:

- **Implemented features with incomplete legacy-fixture coverage** — `Vault`,
  `Batch`, and `Delegate`. Their production implementations and
  focused suites are active, but the imported conformance corpus and runner do
  not yet represent all released variants.

A generated, always-current pass/fail snapshot is produced separately — see
[conformance-status.md](conformance-status.md) (regenerate with `just docs-gen`)
rather than hand-maintaining counts here.

## See also

- [architecture.md](architecture.md) — the transaction pipeline these tests exercise.
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — the implement-against-rippled workflow and where to add new suites.
