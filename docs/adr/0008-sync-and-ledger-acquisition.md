# 0008 — Sync, catch-up and ledger acquisition

## Status

Accepted. This record documents the design as implemented; it was written after
the code.

## Context

A node is behind the network when it starts, after a restart, and whenever it
falls off the preferred chain. It must obtain ledgers it did not build. Three
costs pull in different directions:

- A full state tree is large. Fetching it is the slowest way to obtain a ledger.
- A ledger whose parent is already held can be rebuilt from its transactions
  alone, which is much cheaper, but only works on an unbroken chain of parents.
- Peer input is untrusted. Every acquired ledger must be verified against a
  hash the node already has reason to trust, and acquisition must stay within
  fixed memory and concurrency bounds.

The code grew several acquisition paths to cover these cases. They were not
written down in one place.

## Decision

Split the work into two layers.

**Passive state machines** in `internal/ledger/inbound` track one unit of work
each. They start no goroutines and send nothing to the network; the caller
feeds them replies and timer ticks and issues the wire requests they ask for.
They do read and write the node store and, for replay, execute transactions.

| Type | Tracks |
|------|--------|
| `inbound.Ledger` | One ledger fetched with `TMGetLedger` / `TMLedgerData`. States: `WantBase`, `WantState`, `Complete`, `Failed`. Reasons: consensus, generic, history. `OnTimer` returns refresh, escalate or failed. |
| `inbound.ReplayDelta` | One `TMReplayDeltaRequest`: verifies the response and re-executes the transactions on the parent. Ends in `ReplayReady`. |
| `inbound.Replayer` | The bounded registry of in-flight replay deltas (16 in total, 2 per peer). |
| `inbound.Tracker` | The set of `TMGetLedger` acquisitions, readable from RPC goroutines. |

**Decisions and I/O** live in `internal/consensus/adaptor`.

| Component | Where | Owns |
|-----------|-------|------|
| Router | `router.go`, `router_dispatch.go` | Reads the overlay's inbound lanes, dispatches peer frames, and drives the coordinator from its run loop and a 100 ms tick. Holds no catch-up state. |
| Catch-up replay coordinator | `catchup_replay_coordinator.go`, `router_catchup.go` | The catch-up target, every acquisition, the replay machinery, ancestry and history backfill. |
| Standard replay pipeline | `router_replay_pipeline.go`, `router_replay_worker.go`, `router_replay_owner.go` | A window of consecutive ledgers fetched transaction-only and applied in order onto an anchor. Preparation runs on workers; only the router advances the frontier. |
| Acquisition work lane | `acquisition_work.go` | One goroutine that walks SHAMaps for in-flight acquisitions off the router loop. |
| Acquisition store lane | `acquisition_store.go` | One goroutine that persists verified inbound nodes in arrival order while keeping them readable. |
| Completion tasks | `acquisition_completion.go` | Final verification and adoption. One full-state and one transaction-only completion may run at a time. |
| Frozen-pivot recovery | `router_recovery_session.go`, `preferred_recovery.go`, `recovery_local_owner.go` | A full-state acquisition of one pinned target ledger, used as the new base when the node cannot replay forward from what it holds. |
| Legacy delta path | `legacy_replay_completion.go` | Detached completion for single-ledger replay outside the pipeline: verified replay deltas and transaction-only acquisitions. |
| Header discovery | `router_ancestry.go` | A serial walk of parent hashes from a trusted target back to the local anchor, to authenticate the chain between them. |
| History policy | `router_history_policy.go` | Backfill of older ledgers, newest first. At most one history acquisition runs, and never in a catch-up slot. |
| Fetch-pack cache | `fetchpack.go` | Briefly holds nodes from a fetch pack so a stalled acquisition can complete locally. |
| Tx-set acquisition | `router_txset.go` | Fetching a proposed transaction set by ID during a consensus round. |

The code uses "legacy" for the `TMGetLedger`-based `inbound.Ledger` path, as
opposed to the replay-delta protocol.

### Phases

A node moves through these phases. The names below are descriptive; see
[Known limitation](#known-limitation) for how the code represents them.

1. **Startup ledger selection** (`internal/ledger/service/startup.go`). The
   startup mode — normal, fresh, load, load-file, replay or network — decides
   the first ledger and whether the node still needs a ledger from the network.
   A fast-loaded local ledger is *provisional* until the network confirms it.
   Standalone nodes never need a network ledger.
2. **Initial sync.** While a network ledger is needed or the fast-loaded ledger
   is provisional, the node acquires the ledger its peers prefer, or the target
   of a trusted validation. It does not adopt a ledger from headers alone.
3. **Catch-up.** The operating mode is below `Full`. The node acquires ledgers
   while a preferred peer ledger is more than one sequence ahead.
4. **Tracking, then Full.** The node reaches `Tracking` when it follows the
   network and `Full` when it is within one ledger of its peers on the same
   chain, or after it completes a consensus round on the validated chain. Only a
   `Full` node proposes and validates.
5. **Falling behind.** A `Full` node that sees a preferred peer ledger more than
   two sequences ahead leaves `Full` before it starts catching up, so it stops
   proposing on a stale ledger. Consensus on the wrong ledger requests the
   correct one directly.

Three things trigger acquisition: a peer status change, a trusted validation at
or above the validated tip, and a wrong-ledger report from the consensus engine.
The 100 ms tick also re-arms catch-up toward the current target.

### Which path is used when

To move toward a target, `armCatchupTowardTargetWithPeer` tries the following in
order and stops at the first that applies:

1. Start header discovery when the parent chain from the target back to the
   local anchor still has to be established.
2. Continue a frozen-pivot recovery that is already running.
3. If the target is at most `maxForwardDeltaGap` (128) ledgers ahead, the anchor
   is known to reach it, and no peer offers the replay-delta protocol for the
   next ledger, arm the standard replay pipeline.
4. For a target taken from peer status, or while the local ledger is
   provisional, acquire the next ledger linked to the local closed ledger.
5. Acquire the next ledger on the authenticated path from the recovery anchor,
   using its parent.
6. Otherwise begin frozen-pivot recovery: fetch the target's full state.

For one ledger, `startLedgerAcquisitionWithParentLocked` chooses:

| Condition | Path |
|-----------|------|
| Parent held, peer supports replay, peer not ranked as bad | Replay delta (`TMReplayDeltaRequest`) |
| Parent held, otherwise | Transaction-only `TMGetLedger`, then local replay |
| No parent | Full-state `TMGetLedger` |

A ledger already held, already being acquired by either path, or in a failure
cooldown is not requested again.

Fallbacks go from cheap to expensive:

- A replay delta that fails verification marks the ledger as needing full state
  and falls back to the `TMGetLedger` path.
- A peer that lacks the ledger or a node is replaced by another replay peer,
  then by a transaction-only fetch, then by a full-state fetch.
- A stalled `TMGetLedger` acquisition escalates on its timer: more peers, then
  one fetch pack (when the child ledger is known), then requests by node hash.
- An unresolved replay fault blocks replay entirely
  (see [../replay-fault-recovery.md](../replay-fault-recovery.md)).

### Bounds

| Limit | Value |
|-------|-------|
| Concurrent catch-up acquisitions (`maxConcurrentCatchup`) | 3, one reserved for consensus wrong-ledger recovery |
| Full-state acquisitions while provisional | 1 |
| Replay pipeline window / apply batch | 8 ledgers / 8 ledgers or 25 ms per turn |
| In-flight replay deltas | 16, 2 per peer, 10 s timeout |
| `TMGetLedger` acquisition timer / retries | 3 s / 6 |
| Header discovery | 256 requests, 10 s deadline |
| Failure cooldown per ledger hash | 5 minutes |
| Acquisition memory budget | `[acquisition] memory_mb`, default 512 MiB |

## Consequences

- The common case is cheap: a node a few ledgers behind replays transactions and
  never fetches state.
- The state machines are testable without a network, and the router loop stays
  the single writer for acquisition state (see
  [ADR 0009](0009-concurrency-model.md)).
- Slow work — SHAMap walks, persistence, replay preparation, final adoption —
  runs on dedicated bounded lanes, so the router loop keeps serving consensus
  traffic during a long sync.
- The strategy has many branches. The order above is encoded as sequential
  checks in one function rather than as a table.

### Known limitation

No single value represents the node's sync phase. It is the combination of:

- `Adaptor.operatingMode` (`consensus.OperatingMode`: disconnected, connected,
  syncing, tracking, full). `OpModeSyncing` is declared and compared against but
  never assigned.
- The consensus `Mode` (proposing, observing, wrong ledger, switched ledger).
- `Service.networkLedgerState` in `internal/ledger/service` (ready, needed,
  fast-load provisional), exposed as `NeedsInitialSync` and
  `IsFastLoadProvisional`.
- The coordinator's catch-up target and its source (peer, validation, quorum),
  the recovery anchor, the pending frozen pivot, and several flags.
- The replay pipeline's own booleans (`active`, `applying`, `pivotReady`,
  `backpressured`, the memory-wait flags).
- Whether a replay fault is unresolved.

Code that needs the phase reads several of these and must agree with every other
reader on how they combine. Adding a path means auditing each combination by
hand. Issue [#2191](https://github.com/LeJamon/go-xrpl/issues/2191) tracks
decomposing the catch-up coordinator, which is where an explicit phase type
would be introduced.
