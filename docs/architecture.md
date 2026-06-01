# Architecture

This document describes how go-xrpl is structured and how a transaction flows
through the node. It complements the package-level API reference on
[pkg.go.dev](https://pkg.go.dev/github.com/LeJamon/go-xrpl) — read this for the
shape of the system, and godoc for the details of any individual package.

> go-xrpl is a native Go implementation of an XRP Ledger node, not a line-by-line
> port of [rippled](https://github.com/XRPLF/rippled). rippled is the de facto
> specification (there is no formal XRPL spec), so behavioral parity with rippled
> is the correctness bar even where the Go structure differs.

## Why Go, why not a port

**Why Go.** A ledger node runs many things at once — serving RPC, processing
transactions, running consensus rounds, and (eventually) talking to peers.
Go's goroutines and channels model that concurrency directly, and the language's
small surface area and strong standard library reduce the room for bugs in
financial infrastructure.

**Why not a direct port.** rippled's C++ idioms — templates, RAII, deep
inheritance hierarchies — do not translate cleanly into Go. go-xrpl instead uses
interfaces, composition, and table-driven designs while preserving the same
protocol semantics: the same field ordering, the same TER result codes, the same
edge-case behavior. The result reads as idiomatic Go and stays behaviorally
equivalent to rippled. Where a subtle behavior is deliberately mirrored, the code
cites the rippled source file it follows.

## Package layout

### Public packages (importable by external consumers)

| Package | Responsibility | rippled analogue |
|---------|----------------|------------------|
| [`amendment`](../amendment) | Amendment/feature registry and the per-ledger `Rules` that gate feature-dependent behavior | `Feature.cpp`, `amendmentTable` |
| [`codec/addresscodec`](../codec/addresscodec) | base58 address/seed/key encoding (classic + X-addresses) | `tokens.cpp`, `AccountID.cpp` |
| [`codec/binarycodec`](../codec/binarycodec) | Canonical binary serialization of XRPL objects | `STObject`, `Serializer` |
| [`config`](../config) | `xrpld.toml` parsing and defaults | `Config.cpp` (`rippled.cfg`) |
| [`crypto`](../crypto) | secp256k1 + Ed25519 keys, signing, DER, multi-sign; `common` has SHA-512Half | `PublicKey.cpp`, `SecretKey.cpp` |
| [`drops`](../drops) | Type-safe XRP amount arithmetic and reserve math | `XRPAmount.h` |
| [`keylet`](../keylet) | Derivation of the 256-bit keys identifying ledger entries | `Indexes.cpp` (`keylet::`) |
| [`ledger/entry`](../ledger/entry) | Serializable Ledger Entries (SLEs) for all 40+ object types | `SLE`, `LedgerFormats` |
| [`protocol`](../protocol) | Protocol constants, hash prefixes, wire-type tags | `HashPrefix.h`, `Protocol.h` |
| [`shamap`](../shamap) | SHA-512Half tree (SHAMap) backing ledger state and tx-set hashing | `SHAMap` |
| [`storage`](../storage) | Persistence: `kvstore` (memory/Pebble), `nodestore` (content-addressed state), `relationaldb` (SQLite/PostgreSQL indexes) | `NodeStore`, `SQLiteDatabase` |

### Internal packages

| Package | Responsibility |
|---------|----------------|
| `cmd/xrpld`, `internal/cli` | CLI entrypoint (Cobra) and subcommands (server, rpc, replay, compare) |
| `internal/tx` | Transaction engine and one subpackage per transaction type; `all` registers them |
| `internal/ledger` | Ledger lifecycle: `genesis`, `header`, `manager`, `service`, `state`, `store`, `openledger` |
| `internal/consensus` | Consensus protocol: `rcl` (real), `csf` (simulation framework), plus fee/amendment/UNL voting |
| `internal/txq` | Transaction queue and fee escalation |
| `internal/rpc` | JSON-RPC + WebSocket server, `handlers`, subscription system |
| `internal/grpc` | gRPC server |
| `internal/peermanagement` | Peer networking and the OpenSSL-backed `peertls` handshake |
| `internal/testing` | Conformance test suites, one directory per feature |
| `internal/statecompare` | State-diff tooling for comparing against rippled |

## Transaction flow

Every transaction implements the `Transaction` interface (`internal/tx/transaction.go`)
and moves through a four-stage pipeline. The engine
(`internal/tx/engine.go`) orchestrates the stages; transaction types opt into the
stateful stages by implementing the corresponding interfaces.

1. **Validate** — `Transaction.Validate()`. Structural, context-free checks on a
   parsed transaction: required fields present, field types correct, flags within
   the legal mask. No ledger access.

2. **Preflight** — common field checks run by the engine before the type's own
   logic (fee is a legal amount, sequence/account well-formed, memos within
   size limits, required amendments enabled via `RequiredAmendments()`).

3. **Preclaim** — `Preclaimer.Preclaim(view, config)`, implemented by types that
   need ledger-aware validation (account exists, sufficient balance, object not
   already present). Runs *after* the engine's sequence/fee/signature checks and
   *before* apply. Subject to the `TapRETRY` gate: `tec` results are not committed
   when retry is set, letting the transaction be retried on a later pass — mirroring
   rippled's `PreclaimResult.likelyToClaimFee` in `applySteps.h`.

4. **Apply** — `Appliable.Apply(ctx)`. Executes the transaction against ledger
   state, mutating SLEs through the view and producing metadata. Types that must
   still take effect on a `tec` result (e.g. expired-credential cleanup) implement
   `TecApplier.ApplyOnTec`, mirroring rippled's `tecEXPIRED` handling.

Each stage returns a `Result` carrying a TER code. Result codes and their prefixes
(`tem`, `tef`, `ter`, `tec`, `tes`) match rippled's `TER.h`. Transaction types
self-register via `init()` + `tx.Register()` in their subpackages; importing
`internal/tx/all` pulls in the full set.

The `Engine` is single-writer by design: one engine drives one open ledger's
transaction stream in order, mirroring rippled's single-writer `OpenView`.

## Ledger lifecycle

Ledger state is a pair of SHAMaps — the account-state tree and the transaction
tree — whose root hashes, together with the header fields, determine the ledger
hash. `internal/ledger` owns this lifecycle:

- **Open ledger** (`openledger`) — the mutable ledger under construction;
  transactions apply here as they arrive.
- **Close** (`service`) — at close time the open ledger's transaction set is
  finalized, applied in canonical order, and the resulting state and transaction
  trees are hashed into a new validated ledger header, mirroring rippled's
  `NetworkOPs`/`LedgerMaster` accept path.
- **Store** (`store`, `shamapstore`) — validated ledgers and their SHAMap nodes
  are persisted through the storage layer.

## Consensus

`internal/consensus` contains two distinct things — do not conflate them:

- **`rcl`** — the *real* consensus engine (Ripple Consensus Ledger): proposals,
  disputes, the close-time tie-break, and the `LedgerTrie` that tracks validation
  support. This is what a running node uses.
- **`csf`** — the *Consensus Simulation Framework*, a Go port of rippled's `csf`
  test harness. It runs deterministic discrete-event simulations of consensus
  with no real network or clock, used to test the algorithm in isolation.

Supporting voting subsystems live alongside them: `feevote` (fee voting),
`amendmentvote` (amendment activation), and `negativeunlvote` (Negative UNL).

## Storage layering

go-xrpl separates content-addressed state from queryable indexes, matching
rippled's split between its NodeStore and its relational databases:

- **`storage/kvstore`** — the low-level key/value interface, with an in-memory
  backend (`memorydb`) and a persistent Pebble backend (`pebble`).
- **`storage/nodestore`** — content-addressed storage of serialized ledger
  objects, keyed by their SHA-512Half hash. The SHAMap and ledger layers compute
  the keys; the nodestore treats payloads as opaque and never recomputes them.
- **`storage/relationaldb`** — SQL-backed secondary indexes (ledgers,
  transactions, the account-transaction index, validations, amendment votes) that
  answer history queries the content-addressed store cannot. Default backend is
  `sqlite` (pure-Go, mirroring rippled's `ledger.db`/`transaction.db` layout);
  `postgres` is an alternative for shared deployments.

## Where to go next

- [operating.md](operating.md) — running and configuring a node.
- [conformance.md](conformance.md) — how rippled-parity is verified.
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — the implement-against-rippled workflow.
- [pkg.go.dev/github.com/LeJamon/go-xrpl](https://pkg.go.dev/github.com/LeJamon/go-xrpl) — full API reference.
