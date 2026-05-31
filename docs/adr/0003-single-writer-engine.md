# 0003 — Single-writer transaction engine

## Status

Accepted.

## Context

A node does many things concurrently — serving RPC, accepting peers, running
consensus, applying transactions. Go's goroutines make that concurrency natural.
But applying transactions to a ledger is inherently ordered: each transaction sees
the state left by the previous one, and the resulting state hash is
consensus-critical. rippled handles this with a single-writer `OpenView`.

## Decision

Keep the transaction engine single-writer. One `Engine` (`internal/tx/engine.go`)
drives one open ledger's transaction stream, in order — it is explicitly **not**
safe for concurrent `Apply`/`ApplyPseudo` calls, mirroring rippled's `OpenView`.
Concurrency lives at the boundaries instead: goroutines serve RPC/WebSocket,
manage peer connections, and run consensus rounds, handing ordered work to the
engine. The few cross-goroutine fields (e.g. the atomic transaction counter) are
for safe observation, not concurrent application.

## Consequences

- Transaction application is deterministic and matches rippled's ordering, which
  is required for identical ledger hashes.
- The engine needs no internal locking on the apply path, keeping it simple and
  fast.
- Callers must serialize access to an engine; this constraint is documented at the
  `Engine` type and enforced by convention, not by the type system.
