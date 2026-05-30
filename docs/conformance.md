# Conformance

There is no formal XRP Ledger specification. [rippled](https://github.com/XRPLF/rippled),
the C++ reference implementation, *is* the spec — so for goXRPL, "correct" means
"behaves the way rippled behaves". This document explains how that parity is
verified and how to run the conformance suite.

## rippled as the oracle

Every transaction type, ledger entry, RPC method, and edge case is validated
against rippled's observable behavior: the same field ordering, the same TER
result codes, the same state mutations, the same error conditions. When a subtle
behavior is deliberately mirrored, the Go code cites the rippled source file it
follows (e.g. `Transactor.cpp`, `applySteps.h`), so a reviewer can check the port
against the original.

The local `rippled/` tree is the working reference. Its transaction logic lives
under `rippled/src/xrpld/app/tx/detail/`, ledger objects under
`rippled/src/xrpld/ledger/detail/`, protocol definitions under
`rippled/src/libxrpl/protocol/`, and the upstream unit tests — which the Go
conformance suites mirror — under `rippled/src/test/app/`.

## The conformance suite

Conformance tests live in `internal/testing/conformance/` and run rippled-derived
fixtures against the goXRPL transaction engine and ledger. They are exposed as Go
subtests under `TestConformance/app/<Suite>` and `TestConformance/ledger/<Suite>`.

Run them with the summary harness:

```bash
just conformance                 # full suite, per-suite breakdown
just conformance TxQ             # only suites matching a name (TxQ, AMM, Vault, …)
just conformance --failing       # only suites that have failures
just conformance --list-fail     # list every failing test name
just conformance TxQ --list-fail # combine a filter with a listing
```

`just conformance` forwards its arguments to
[`scripts/conformance-summary.sh`](../scripts/conformance-summary.sh), so the raw
script accepts the same flags. The suite timeout defaults to `300s` and can be
overridden with the `CONFORMANCE_TIMEOUT` environment variable.

## Reading the results

The summary prints overall pass/fail counts, then splits them into **in scope**
and **out of scope**, then a per-suite breakdown:

```
=========================================
 CONFORMANCE SUMMARY
=========================================
 Total:     NNN pass /  NN fail /  NNN  (PP.P%)
 In scope:  NNN pass /   0 fail /  NNN  (100.0%)
 Out:        NN pass /  NN fail /   NN
=========================================
```

In the per-suite table, suites are colored green (all pass), yellow (partial), red
(none pass), or dimmed (out of scope). The **in-scope** percentage is the number
that matters: it excludes suites that are intentionally not yet covered (below).

## What is intentionally out of scope

Some suites are excluded from the in-scope totals on purpose — they cover features
that are deliberately unimplemented or only partially covered in the current
release target. The authoritative list is
[`scripts/conformance-out-of-scope.txt`](../scripts/conformance-out-of-scope.txt);
removing a line brings that suite back into scope.

As of this writing the out-of-scope suites fall into two groups:

- **Not implemented yet** — `Vault`, `Batch`, `EscrowToken`, `XChain`, `XChainSim`,
  `Delegate`, `Credentials`. **Vault** and the **XChain** bridge ship as
  *registered stubs by design*: they parse and register but are not active
  (the `XChainBridge` amendment is `SupportedNo`). This is an intentional scope
  boundary for the current release, not a regression.
- **Partially implemented / known gaps** — several `NFTokenBurn*` and `NFToken*`
  variants, `Regression`, `ThinBook`, `Transaction_ordering`, and `ledger/BookDirs`.

A generated, always-current pass/fail snapshot is produced separately — see
[conformance-status.md](conformance-status.md) (regenerate with `just docs-gen`)
rather than hand-maintaining counts here.

## See also

- [architecture.md](architecture.md) — the transaction pipeline these tests exercise.
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — the implement-against-rippled workflow and where to add new suites.
