# 0001 — rippled as the specification

## Status

Accepted.

## Context

The XRP Ledger has no formal, normative specification. The protocol's behavior —
transaction validation, fee and reserve math, ledger hashing, consensus, the
binary codec — is defined by what the C++ reference implementation,
[rippled](https://github.com/XRPLF/rippled), actually does. Any second
implementation must agree with rippled bit-for-bit to stay on the network, since
ledger and transaction hashes are consensus-critical.

## Decision

Treat rippled as the de facto specification. The correctness bar for any go-xrpl
behavior is "does this match rippled?" — same field ordering, same TER result
codes, same state mutations, same edge cases. A local read-only `rippled/` tree is
kept as the working reference; its unit tests under `src/test/app/` are mirrored
as Go conformance suites. Where a subtle behavior is deliberately copied, the Go
code cites the rippled source file it follows.

## Consequences

- Reviewers can check any port against a concrete reference instead of arguing
  from first principles.
- Parity is measurable — see [../conformance.md](../conformance.md) and the
  generated [../conformance-status.md](../conformance-status.md).
- go-xrpl inherits rippled's quirks where they are consensus-relevant; "the spec"
  moves when rippled moves, so the reference tree is pinned to a target version.
