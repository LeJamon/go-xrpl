# 0009 — Concurrency model

## Status

Accepted. This record documents the design as implemented; it was written after
the code. Lock orderings are recorded only where a comment in the code states
them. Where the code states none, this record says so.

## Context

A node serves untrusted peers and clients while it runs consensus on a fixed
cadence. Three failure modes shape the design:

- A flood on one kind of traffic must not delay proposals and validations.
- One malformed frame must not crash the process.
- Subsystems call into each other in both directions (consensus into the
  ledger, the ledger back into consensus and RPC), which invites lock-order
  inversions.

## Decision

### Goroutine ownership

Every long-lived goroutine has one owning component, which starts it and joins
it on stop. `internal/node/runtime.go` starts and stops the owners in a fixed
order (see [architecture.md](../architecture.md#node-assembly-and-shutdown)),
and bounds each shutdown phase with a timeout.

| Owner | Goroutines | Stopped by |
|-------|-----------|------------|
| `peermanagement.Overlay` | Accept loop, four event loops (best-effort, consensus, consensus control, acquisition), discovery loop, maintenance loop, serve-scheduler workers | Context cancel; `Overlay.Stop` waits for every peer |
| `peermanagement.Peer` | Read, write and ping loops | The first loop to exit closes the peer; the others are joined |
| `rcl.Engine` | One heartbeat loop (1 s ticker) plus ancestry and preferred-recovery workers | `Engine.Stop` cancels and waits |
| `adaptor.Router` | The run loop; a tx worker pool; serve pools; one manifest worker; one trusted and one untrusted validation worker; the acquisition work lane | `stopLifecycle` cancels and waits; lanes are stopped and drained |
| Catch-up replay coordinator | Detached replay and completion tasks; one acquisition store worker | Joined before the work and store lanes are drained |
| `adaptor.Adaptor` | One ledger-accept worker, started on first use | `Engine.Stop` stops it after the heartbeat loop exits |
| `service.Service` | One persistence worker, one event dispatcher, a node-store sweeper, detached transition and replay tasks | `Service.Stop` waits for in-flight work, then drains persistence, then stops the dispatcher |
| `shamapstore.Rotator`, `cleaner` | One background worker each | Their `Stop` methods |
| `rpc` WebSocket server | Three goroutines per connection (read, send, ping) | Joined on server shutdown |
| `watchdog` | One monitor loop | Context cancel |

`internal/txq` and `internal/membudget` start no goroutines.

### Single writers

Some state is confined to one goroutine instead of being locked:

- **Router run loop.** Proposal, validation and ledger-acquisition handling run
  on the router's single `Run` goroutine. Timeouts are handled on the same
  goroutine by `maintenanceTick`, so a peer reply and a timeout for the same
  ledger cannot race.
- **Overlay event loop.** Per-peer state changes in `handleEvent` run on one
  goroutine. Connect and disconnect callbacks run there and must not block.
- **Single workers.** Ledger persistence, ledger-event publication, ledger
  acceptance after a round, acquisition node persistence and manifest handling
  each have exactly one worker, which makes their order the arrival order.

The consensus engine is different: its state is guarded by `Engine.mu`, which
the heartbeat takes on every tick. Atomics mirror the mode and
related fields for lock-free reads from RPC.

Work that is slow or CPU-bound is moved off the single writers: transaction
parsing and signature checks to the tx worker pool, serving peers' requests to
the serve pools, SHAMap walks to the acquisition work lane, and applying an
accepted ledger to the ledger-accept worker.

### Inbound lanes

Peer traffic is split by class into bounded channels at every hop, so each class
has its own capacity and its own overflow rule.

**Peer read loop.** Before reading a large frame (unknown type, or a type whose
maximum payload exceeds 64 KiB), the read loop reserves the frame's bytes in an
overlay-wide read budget. The default is three maximum-size messages. The
reservation blocks until bytes are released or the peer closes.

**Peer to overlay.**

| Lane | Traffic | Capacity | When full |
|------|---------|----------|-----------|
| Consensus events | Proposals, validations, validator-list collections | 256 | Read loop blocks |
| Consensus control events | Status changes, have-set | 256 | Read loop blocks |
| Acquisition events | Ledger data, replay-delta and proof-path replies, get-objects | 16 | Read loop blocks |
| Manifest messages | Manifests | `MaxPeers` | Read loop blocks |
| Best-effort events | Everything else | 256 | Dropped and counted |

**Overlay to router.**

| Lane | Capacity | When full |
|------|----------|-----------|
| Consensus messages | 256 | Blocks |
| Consensus control messages | 256 | Blocks |
| Ledger data | 64 | Blocks, which propagates backpressure to the peer |
| Transactions | `max_transactions` (default 250) | Shed and counted (`jq_trans_overflow`) |
| Service messages | 256 | Dropped and counted |

A blocked read loop stops reading from that one peer; closing the peer releases
it. The sender of a shed transaction resends it, and other peers relay it.

**Inside the router.** Each loop turn first drains up to 32 trusted validation
results and 32 consensus messages, then selects over all lanes. The ledger-data
lane is not read while the acquisition work lane cannot accept more.

| Queue | Workers | Depth | When full |
|-------|---------|-------|-----------|
| Tx jobs | `max(4, GOMAXPROCS)` | 1024 | Dropped and counted |
| Serve pools | `max(2, GOMAXPROCS/2)` | 256, split between ledger and tx-set serving | Dropped and counted |
| Trusted validations | 1 | 256, at most 64 per peer | Shed and counted |
| Untrusted validations | 1 | 64 | Shed and counted |

Requests a peer asks the overlay to serve go through the `ServeScheduler`:
4 workers, a queue of 64, at most 8 queued and 2 running per peer. A request
refused because the scheduler is saturated is charged to the peer.

### Panic boundaries

A panic reachable from peer or client input is recovered at the boundary where
that input enters, so it costs one frame, one peer or one request.

| Boundary | Where | After recovery |
|----------|-------|----------------|
| Router frame dispatch | `adaptor/router_recover.go`, deferred at each dispatch entry point (run loop, tx jobs, serves, manifests, tx-set learning) | Logged with sender and message type; the peer is charged for bad data; the frame is dropped |
| Peer loops | `peermanagement/peer.go` `runWorker` | Logged with a stack; the panic becomes the loop's error and the peer is closed |
| Inbound handshake | `peermanagement/overlay_inbound.go` `handleInbound` | Logged; the connection is closed |
| Serve tasks | `peermanagement/serve_scheduler.go` | Logged; the requesting peer is closed |
| Validation tracking | `rcl/validations.go`, `rcl/validations_trie.go` | Logged; the consensus goroutine continues |
| HTTP and WebSocket | `rpc/server.go`, `rpc/server_dispatch.go`, `rpc/websocket_dispatch.go` | The request fails; the connection's read loop survives |
| gRPC | `node/grpc.go` unary interceptor | The call returns `codes.Internal` |

The transaction engine (`internal/tx`) and the queue (`internal/txq`) contain
no recovery: a panic while applying a transaction is not caught there.

### Lock ordering

**Ledger service** (`internal/ledger/service/service.go`, field comments). The
order is:

```text
openLedgerMu -> mu -> historyComponent.mu -> component locks (TxQ, configCacheMu)
```

- `openLedgerMu` serializes open-ledger submission with lifecycle transitions.
  It is taken before `mu` so a transition waiting on transaction application
  does not block closed-ledger readers.
- `mu` guards the open/closed/validated frontier and cross-component state.
- `historyComponent.mu` owns ledger history and indexes. History-only queries
  take it without `mu`.
- The transaction queue has its own mutexes, taken after `mu`. Queue code never
  takes a ledger lock while holding a queue lock.
- `configCacheMu` is taken after `mu`.

**Catch-up replay coordinator**
(`internal/consensus/adaptor/catchup_replay_coordinator.go`, type comment):

- `replayCommitMu` is taken before `acquisitionMu`. Publication holds
  `replayCommitMu` only through storage.
- `headerDiscoveryMu` is taken before `catchupMu` or `seqHashMu`.
- `acquisitionMu` guards replay generations, retries, recovery and drain
  ownership; `catchupMu` the target and cooldowns; `seqHashMu` ancestry;
  `historyMu` backfill; `peersMu` peer ledger observations.
- Acquisition retirement and worker waits happen after these locks are released.
- `lifecycleMu` serializes task admission with cancellation.

**Storage.** The rotating Pebble store (`storage/kvstore/pebble/rotating.go`)
takes `mu`, then `archiveMu` when needed, then its mutation stripes in ascending
order.

**Discovery** (`internal/peermanagement/discovery.go`): `d.mu` before the boot
cache's `mu`.

**Rules stated instead of an order.** Several components avoid nesting rather
than define an order:

- The engine calls `Adaptor.GetLedger` while holding `Engine.mu`; that call
  answers from memory and must not block on the database.
- Relay and validation-tracker callbacks are invoked after releasing the lock
  that guards the structure, so they cannot invert against the overlay's peer
  map.
- Overlay code resolves peers under `peersMu`, then releases it before
  enqueueing to a peer.

**Not documented in code.** No comment states an order for:

- The `Overlay` mutexes (`providersMu`, `peersMu`, `peerStartMu`,
  `bootstrapLeaseMu`, `relayedIndexMu`, `listenerMu`, `lifecycleMu`) or the
  `Peer` mutexes (`mu`, `sendMu`, `squelchMu`, `usageMu`, `txQueueMu` and
  others), relative to each other.
- The `Router` mutexes (`manifestFrameMu`, `txSetAcquireMu`, `lifecycleMu`) and
  the `Adaptor` mutexes (`mu`, `trustUpdateMu`, `trustTransitionMu`,
  `peerLCLsMu`, `reqLedgerMu` and others).
- The remaining `Service` mutexes: `lifecycleMu`, `preferredTransitionMu`,
  `persistMu` and `canonicalPersistMu` relative to each other, and the replay,
  verification and re-certification mutexes.
- Any order *between* subsystems, for example `Engine.mu` relative to
  `Service.mu`, or coordinator locks relative to service locks.

No written rule covers these.

## Consequences

- Consensus traffic keeps its own capacity end to end; a transaction or
  service-message flood is shed at a counted boundary instead of delaying it.
- Backpressure on acquisition traffic slows the sending peer rather than growing
  memory, at the cost of stalling that peer's other traffic until the lane
  drains.
- A malformed frame costs the sender its standing, not the node its process.
- Single-writer loops must not block. Every new piece of slow work needs a lane
  or a worker, which is why the adaptor has many of them.
- The undocumented orderings above are a maintenance risk: a new nested
  acquisition can introduce a deadlock that review will not catch from comments.
  Issue [#2191](https://github.com/LeJamon/go-xrpl/issues/2191) tracks
  decomposing the three largest lock-holding types — the catch-up coordinator,
  the ledger service and the overlay — which is the point at which their
  orderings should be written down or made unnecessary.
