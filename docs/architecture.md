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
transactions, running consensus rounds, acquiring ledgers, and talking to peers.
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

### Dependency direction

Public packages never import `internal/`. The `public-no-internal` depguard rule
in [`.golangci.yml`](../.golangci.yml) enforces this for every non-test file
outside `internal/`, `cmd/` and `scripts/`. Dependencies therefore point one
way:

```text
cmd/goxrpl -> internal/cli -> internal/node -> internal/* -> public packages
```

`internal/node` is the composition root: it constructs the subsystems and hands
each one its dependencies.

### Public packages (importable by external consumers)

| Package | Responsibility | rippled analogue |
|---------|----------------|------------------|
| [`amendment`](../amendment) | Amendment/feature registry and the per-ledger `Rules` that gate feature-dependent behavior | `Feature.cpp`, `amendmentTable` |
| [`codec/addresscodec`](../codec/addresscodec) | base58 address/seed/key encoding (classic + X-addresses) | `tokens.cpp`, `AccountID.cpp` |
| [`codec/binarycodec`](../codec/binarycodec) | Canonical binary serialization of XRPL objects | `STObject`, `Serializer` |
| [`config`](../config) | `goxrpl.toml` parsing and validation; unknown keys are rejected | `Config.cpp` (`rippled.cfg`) |
| [`crypto`](../crypto) | native libsecp256k1-backed secp256k1 keys/signing/verification plus Ed25519; `common` has SHA-512Half | `PublicKey.cpp`, `SecretKey.cpp` |
| [`drops`](../drops) | Type-safe XRP amount arithmetic and reserve math | `XRPAmount.h` |
| [`keylet`](../keylet) | Derivation of the 256-bit keys identifying ledger entries | `Indexes.cpp` (`keylet::`) |
| [`ledger/entry`](../ledger/entry) | Serializable Ledger Entries (SLEs) for all 40+ object types | `SLE`, `LedgerFormats` |
| [`log`](../log) | Structured logging on `log/slog` with Trace/Fatal levels and named partitions | `Journal`, `Logs` |
| [`protocol`](../protocol) | Protocol constants, hash prefixes, wire-type tags | `HashPrefix.h`, `Protocol.h` |
| [`shamap`](../shamap) | SHA-512Half tree (SHAMap) backing ledger state and tx-set hashing; `shamap/backend` adapts a node store to it | `SHAMap` |
| [`storage`](../storage) | Persistence: `kvstore` (memory/Pebble), `nodestore` (content-addressed state), `relationaldb` (SQLite/PostgreSQL indexes) | `NodeStore`, `SQLiteDatabase` |
| [`version`](../version) | The node's software and build versions | `BuildInfo.cpp` |

### Internal packages

| Package | Responsibility |
|---------|----------------|
| `cmd/goxrpl`, `internal/cli` | CLI entrypoint (Cobra). Subcommands: `server` (the default), `rpc`, `compare`, `generate-config`, `migrate-rotation-state`, `version`, and `replay` / `replay-range` from `internal/replaytool` |
| `internal/node` | Composition root. Builds every subsystem in stages, starts them, and shuts them down in order (`runtime.go`) |
| `internal/tx` | Transaction engine (`engine`), invariant checks (`invariants`), and one subpackage per transaction family; `all` registers them |
| `internal/ledger` | Ledger type and lifecycle: `service`, `openledger`, `inbound`, `shamapstore`, `cleaner`, `genesis`, `header`, `skiplist`, `selector`, `localtxs`, `negativeunl`, `replayfault`, `state` |
| `internal/consensus` | Consensus: the `rcl` engine, the `adaptor` that binds it to the node, the `csf` simulation framework, `ledgertrie`, the vote producers (`feevote`, `amendmentvote`, `negativeunlvote`) and the validation `archive` |
| `internal/txq` | Transaction queue and fee escalation |
| `internal/feetrack` | Local load-fee tracker (rippled's `LoadFeeTrack`) |
| `internal/rpc` | JSON-RPC + WebSocket server, `handlers`, `subscription` |
| `internal/grpc` | gRPC `XRPLedgerAPIService` (`GetLedger`, `GetLedgerEntry`, `GetLedgerData`, `GetLedgerDiff`) |
| `internal/peermanagement` | Peer overlay: handshake, peers, discovery, relay, serving; `peertls` (OpenSSL-backed TLS), `resource` (per-endpoint charging), `cluster`, `message`, `proto` |
| `internal/manifest` | Validator manifest parsing, verification, caching and persistence |
| `internal/validator/list` | Publisher-signed validator lists (rippled's `ValidatorList` + `ValidatorSite`) |
| `internal/watchdog` | Detects stalled event loops and forces terminal recovery at the abort threshold |
| `internal/membudget` | Node-local byte admission pool: each consumer has a protected minimum and shares the remaining capacity |
| `internal/observability` | Process-level metrics surfaced to RPC |
| `internal/cmdexit` | Sentinel error for a non-zero CLI exit without an extra error line |
| `internal/testing` | Behavioral test suites, one directory per feature, and the `conformance` corpus runner |
| `internal/testutil` | Test helpers; `rippled` locates the pinned rippled sources for inventory tests |
| `internal/statecompare` | Client for the xrpl-state-compare replay data plane |

## Transaction flow

Every transaction implements the `Transaction` interface (`internal/tx/transaction.go`)
and moves through a four-stage pipeline. The engine
(`internal/tx/engine`) orchestrates the stages; transaction types opt into the
stateful stages by implementing the corresponding interfaces.

1. **Validate** — `Transaction.Validate()`. Structural, context-free checks on a
   parsed transaction: required fields present, field types correct, flags within
   the legal mask. No ledger access. The engine calls it from preflight.

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
   state, mutating SLEs through the view and producing metadata. On a `tec`
   result the engine discards the transaction's changes, charges the fee and
   sequence, and re-applies only the cleanup rippled performs (removal of
   unfunded and expired offers, AMM trust lines and expired credentials).
   Invariant checks
   (`internal/tx/invariants`) run on the result before it is committed.

Each stage returns a `Result` carrying a TER code. Result codes and their prefixes
(`tem`, `tef`, `ter`, `tec`, `tes`) match rippled's `TER.h`. Each family
subpackage exposes a `Register()` function that calls `tx.Register()` for its
types; `all.RegisterAll()` in `internal/tx/all` runs them all once.

The families are `account`, `amm`, `batch`, `check`, `clawback`, `credential`,
`delegate`, `depositpreauth`, `did`, `escrow`, `ledgerstatefix`, `lending`
(`LoanBroker*` and `Loan*`), `mpt`, `nftoken`, `offer`, `oracle`, `paychan`,
`payment`, `permissioneddomain`, `pseudo`, `signerlist`, `sponsor`
(`SponsorshipSet`, `SponsorshipTransfer`), `ticket`, `trustset`, `vault` and
`xchain`. [supported-transactions.md](supported-transactions.md) is the generated
per-type list.

The `Engine` is single-writer by design: one engine drives one open ledger's
transaction stream in order, mirroring rippled's single-writer `OpenView`
(see [ADR 0003](adr/0003-single-writer-engine.md)).

Submitted transactions do not reach the engine directly. They enter through
`openledger.OpenLedger.Submit`, which asks the queue (`internal/txq`) to
classify them: applied to the open view now, or held with `terQUEUED`. When a
ledger closes, the queue refills the new open ledger.

## Ledger lifecycle

Ledger state is a pair of SHAMaps — the account-state tree and the transaction
tree — whose root hashes, together with the header fields, determine the ledger
hash. `internal/ledger/service` owns this lifecycle and tracks three frontiers:

- **Open ledger** (`openledger`) — the mutable ledger under construction;
  transactions apply here as they arrive.
- **Closed ledger** — at close time the agreed transaction set is applied in
  canonical order and the resulting state and transaction trees are hashed into
  a new header, mirroring rippled's `NetworkOPs`/`LedgerMaster` accept path.
  Consensus delivers its result through `Service.AcceptConsensusResult`;
  standalone mode uses `Service.AcceptLedger`.
- **Validated ledger** — the newest ledger with a quorum of trusted
  validations, set through `Service.SetValidatedLedger`.

Around those frontiers:

- **Persistence worker** — closed ledgers are queued and written by a single
  background goroutine. It writes the state and transaction node deltas to the
  node store before the header, then updates the relational index. Shutdown
  drains the queue.
- **History window** — `ledger_cache_size` (default 256) bounds the ledgers and
  indexes kept in memory. `ledger_history` is a different setting: it sets how
  far back the node acquires history from peers and is cross-checked against
  `online_delete`.
- **Online delete** (`shamapstore`) — with `[node_db] online_delete` set, a
  rotator deletes complete ledgers below the rotation boundary from the node
  store and the relational index once enough validated ledgers have passed.
- **Integrity** — `cleaner` verifies stored ledgers in the background;
  `replayfault` durably records a replay failure, which stops the node from
  proposing and signing validations until it is resolved
  (see [replay-fault-recovery.md](replay-fault-recovery.md)).

## Consensus

`internal/consensus` defines the `Engine` and `Adaptor` interfaces
(`engine.go`). Three packages matter most:

- **`rcl`** — the consensus engine (Ripple Consensus Ledger): proposals,
  disputes, the close-time tie-break, validation tracking, and preferred-ledger
  selection through `ledgertrie`. There is one engine implementation.
- **`adaptor`** — the production implementation of `consensus.Adaptor`. It
  bridges the engine to the ledger service, the peer overlay and the transaction
  queue. It owns the inbound message `Router`, the validator identity, the
  outbound sender, the operating mode, and the catch-up, replay and acquisition
  machinery described in the next section. `NewFromConfig` builds the whole
  group as a `Components` value.
- **`csf`** — the *Consensus Simulation Framework*. It drives the same
  `rcl.Engine` through a simulated adaptor, with a manual tick and no real
  network or clock, for deterministic tests of the algorithm.

Supporting voting subsystems live alongside them: `feevote` (fee voting),
`amendmentvote` (amendment activation), and `negativeunlvote` (Negative UNL).
`archive` persists validations evicted from memory when `[validation_archive]`
is enabled.

## Sync and catch-up

A node that is behind the network acquires ledgers until its closed ledger
matches the validated tip, then follows consensus. The decisions live in
`internal/consensus/adaptor` (the catch-up replay coordinator, driven by the
`Router`); the per-ledger state machines live in `internal/ledger/inbound`.
The coordinator prefers replaying a ledger's transactions on top of a parent it
already holds (replay delta), falls back to a transaction-only fetch, and uses a
full state-tree acquisition when no usable parent exists.

[ADR 0008](adr/0008-sync-and-ledger-acquisition.md) documents the components,
the phases and which path is used when.

## Peer overlay

`internal/peermanagement` implements the XRPL peer protocol:

- **`Overlay`** owns the listener, the peer set, discovery and relay. **`Peer`**
  owns one connection with a read, a write and a ping loop.
- **Handshake** — `peertls` provides TLS over an OpenSSL CGO shim and exposes the
  session's shared value, which the handshake signs
  (see [ADR 0006](adr/0006-cgo-required-for-daemon.md)).
- **Bounded inbound lanes** — messages leave the overlay on separate bounded
  channels per traffic class: consensus, consensus control, ledger data,
  transactions, manifests and service traffic. Consensus and acquisition lanes
  apply backpressure to the sending peer's read loop; the transaction and
  service lanes shed and count the drop.
- **Read budgets** — a peer reserves bytes in an overlay-wide budget before it
  reads a large frame (default three maximum-size messages), and each frame has
  a time budget derived from its size.
- **Resource charging** — `resource` keeps a decaying balance per endpoint;
  misbehaving peers are warned, then dropped.
- **Serving** — requests for ledger data are executed by a bounded
  `ServeScheduler` with per-peer queue and concurrency limits.

[ADR 0009](adr/0009-concurrency-model.md) documents lane capacities, goroutine
ownership, panic boundaries and lock ordering.

## Storage layering

go-xrpl separates content-addressed state from queryable indexes, matching
rippled's split between its NodeStore and its relational databases
(see [ADR 0004](adr/0004-storage-architecture.md)):

- **`storage/kvstore`** — the low-level key/value interface, with an in-memory
  backend (`memorydb`) and a persistent Pebble backend (`pebble`).
- **`storage/nodestore`** — content-addressed storage of serialized ledger
  objects, keyed by their SHA-512Half hash. The SHAMap and ledger layers compute
  the keys; the nodestore treats payloads as opaque and never recomputes them.
  It adds positive and negative caches and batched reads.
- **`shamap`** — reads and writes tree nodes through the `shamap.Family`
  interface. `shamap/backend` adapts a node store to it, so a ledger's trees are
  backed by the node store rather than held fully in memory.
- **Rotation** — when online delete is enabled the node store runs on a
  rotating Pebble store (`nodestore.RotatingKVDatabase`). Deletion happens by
  rotating generations instead of deleting keys one at a time.
- **`storage/relationaldb`** — SQL-backed secondary indexes (ledgers,
  transactions, the account-transaction index, validations, amendment votes) that
  answer history queries the content-addressed store cannot. Default backend is
  `sqlite` (pure-Go, with ledger headers and indexes committed together in `transaction.db`);
  `postgres` is an alternative for shared deployments, selected by a
  `postgres://` `database_path`.

## RPC and gRPC

- **Transports** — `internal/node` binds one listener per configured
  `[port_*]` section: HTTP JSON-RPC, WebSocket, or gRPC. Peer ports belong to
  the overlay.
- **Methods** — `internal/rpc/handlers` holds one handler per method, built into
  a registry. [rpc-methods.md](rpc-methods.md) is the generated catalog.
- **Admission** — each request gets a role (guest, user, admin, identified,
  proxy) from its port configuration and source address. Dispatch checks load
  first, then the method's required role, then charges the client's resource
  balance.
- **Subscriptions** — `internal/rpc/subscription` tracks WebSocket and URL
  subscribers; the ledger service and consensus publish events to it.
- **gRPC** — `internal/grpc` serves the ledger-data API. No gRPC listener
  starts unless a port section with the `grpc` protocol is configured.

## Node assembly and shutdown

`internal/node/runtime.go` builds the node in ten stages, in this order. A
failing stage aborts startup and runs the shutdown sequence for what was built.

1. `configureMemory` — memory limits and budgets.
2. `configureStorage` — node store, relational database, SHAMap family.
3. `configureLedger` — ledger service, started from the selected startup ledger.
4. `configureMaintenance` — online-delete rotator, ledger cleaner, samplers.
5. `configureConsensus` — overlay, engine, adaptor and router (not started yet).
6. `configureWatchdog` — stall watchdog.
7. `bindRPC` — subscription manager, HTTP and WebSocket servers.
8. `bindStreams` — event streams from ledger and consensus to subscribers.
9. `bindTransports` — listeners for the RPC, WebSocket and gRPC ports.
10. `start` — resource manager, then consensus (overlay, engine, router,
    validator-list poller), then the transports, then the watchdog.

Shutdown runs in the reverse direction of data flow, so nothing writes into a
component that has already stopped. Each phase is bounded by a timeout.

1. Stop the watchdog and cancel the run context.
2. Stop the transports (HTTP, WebSocket, gRPC).
3. Stop ledger acquisitions.
4. Stop state-base re-certification, the online-delete rotator and the ledger
   cleaner.
5. Stop the peer connect scheduler, then the consensus components (overlay,
   router, engine).
6. Stop the resource manager.
7. Stop the ledger service, which drains the persistence queue.
8. Prepare the fast-load checkpoint (bounded by
   `[server] checkpoint_shutdown_grace`).
9. Close the node store, then the relational database.

## Where to go next

- [operating.md](operating.md) — running and configuring a node.
- [conformance.md](conformance.md) — how rippled-parity is verified.
- [adr/](adr/) — the design decisions behind this structure.
- [../CONTRIBUTING.md](../CONTRIBUTING.md) — the implement-against-rippled workflow.
- [pkg.go.dev/github.com/LeJamon/go-xrpl](https://pkg.go.dev/github.com/LeJamon/go-xrpl) — full API reference.
