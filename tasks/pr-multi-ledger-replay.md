# Multi-Ledger Replay (Issue #271)

Client-side `SkipListAcquire` + `LedgerReplayTask` so catch-up can fetch
many ledgers in one parallel backward walk instead of one round-trip per
ledger.

## Why

Today `Router.checkBehind` (`internal/consensus/adaptor/router.go:1598`)
arms **one** acquisition per status-gossip event. To catch up by 50
ledgers we currently wait for 50 forward status reports — each
acquisition only starts after the previous adopts and bumps our LCL.
Rippled's `LedgerReplayer` instead asks one peer for the skip-list of
the target tip, then dispatches up to 256 parallel `LedgerDeltaAcquire`
subtasks across all 256 ancestors in that skip-list.

This is a catch-up-speed change, not a correctness change. The F6
cascade closes the local "N+2 before N+1" reorder; this closes "we are
N-50 behind a known-good tip".

## Existing infrastructure (no work needed)

Already wired on `main` after the PR #264 series:

| Piece | Location |
|-------|----------|
| `TMProofPathRequest`/`Response` proto + message wrappers | `internal/peermanagement/proto/ripple.pb.go:2285+`, `internal/peermanagement/message/messages.go:218` |
| Peer-side handler that serves proof-path requests | `internal/peermanagement/overlay.go:1186`, `internal/consensus/adaptor/ledger_provider.go:173` (`GetProofPath`) |
| Merkle verifier (leaf-to-root, matching wire order) | `shamap/proof.go:82` (`VerifyProofPath`), `:185` (`VerifyProofPathWithValue`) |
| `keylet::skip()` rolling-256 keylet | `keylet/keylet.go:105` (`LedgerHashes`) |
| `LedgerHashes` SLE decode (Hashes vector) | `ledger/entry/ledger_hashes.go` |
| `Replayer` coordinator: concurrent ReplayDelta acquisitions, per-peer + global caps (16 global / 2 per peer) | `internal/ledger/inbound/replayer.go:20,33,134` |
| Single-target `ReplayDelta` state machine (verify → apply → divergence-check) | `internal/ledger/inbound/replay_delta.go` |
| Router dispatch of `TypeReplayDeltaResponse` | `internal/consensus/adaptor/router.go:322,1365` |
| Sender helper for replay-delta | `internal/consensus/adaptor/sender.go:266` (`RequestReplayDelta`) |

The two things missing are: the **client-side** of proof-path (we never
send `TMProofPathRequest`), and the policy layer that turns
`(tipHash, depth)` into a parallel backward walk.

## Rippled reference

- `rippled/src/xrpld/app/ledger/LedgerReplayer.h` — constants
  (`TASK_TIMEOUT=500ms`, `SUB_TASK_TIMEOUT=250ms`,
  `SUB_TASK_MAX_TIMEOUTS=10`, `MAX_TASKS=10`, `MAX_QUEUED_TASKS=100`).
- `rippled/src/xrpld/app/ledger/detail/SkipListAcquire.cpp` — sends
  `TMProofPathRequest{LedgerHash, Key=keylet::skip().key,
  Type=lmACCOUNT_STATE}`; on response deserializes the SLE leaf,
  validates the `sfHashes` vector against `target.stateHash` via the
  proof path (line 91-111, 151-165).
- `rippled/src/xrpld/app/ledger/detail/LedgerReplayTask.cpp` — once the
  skip-list arrives (`updateSkipList`, line 135), enumerates ancestor
  hashes **directly from the skip-list array** (not via ParentHash) and
  dispatches one `LedgerDeltaAcquire` per ancestor in
  `createDeltas` (LedgerReplayer.cpp:122).
- `rippled/src/xrpld/app/ledger/detail/LedgerReplayer.cpp:44-109` —
  `replay(reason, finishHash, totalLedgers)` entry point, dedups
  ancestor-of-existing-task and enforces `MAX_TASKS=10`.

The skip-list is the **rolling-256 list** in `keylet::skip()` — every
close appends `parentHash` to that vector (capped at 256). So one proof
fetch unlocks every hash needed for a backward window of up to 256.

## Scope

### Phase 1 — Sender + receiver for `TMProofPathRequest`

1. `internal/consensus/adaptor/sender.go`: add `RequestProofPath(peerID
   uint64, ledgerHash [32]byte, key [32]byte, mapType
   message.LedgerMapType) error`. Mirrors `RequestReplayDelta`.
2. Expose on the `Sender` interface used by the router (look up
   `OverlaySender` interface in adaptor.go).
3. Router: in the `messageRouter` switch (around line 322 of
   router.go), add `case message.TypeProofPathResponse:
   r.handleProofPathResponse(msg)`. The dispatch is to the
   `SkipListAcquire` coordinator (Phase 2 owns the handler).

### Phase 2 — `SkipListAcquire` state machine

New file `internal/ledger/inbound/skiplist_acquire.go`. Mirrors
`replay_delta.go`'s shape:

```go
type SkipListAcquire struct {
    hash      [32]byte // target ledger hash whose skip-list we want
    stateHash [32]byte // target.AccountHash, used to verify the proof
    state     State    // StateWantProof / StateComplete / StateFailed
    hashes    [][32]byte
    subTaskStart time.Time
    retryCount   int
    peerID       uint64
    mu           sync.Mutex
}

func (s *SkipListAcquire) GotResponse(resp *message.ProofPathResponse) error
func (s *SkipListAcquire) Hashes() [][32]byte
func (s *SkipListAcquire) Hash() [32]byte
```

`GotResponse` does:

1. Confirm `resp.LedgerHash == s.hash` and `resp.MapType ==
   LedgerMapAccountState` (charge `replay-skiplist-mismatch` if not).
2. `keylet := keylet.LedgerHashes()` → match against `resp.Key`.
3. `payload := shamap.VerifyProofPathWithValue(s.stateHash,
   keylet.Key, resp.Path)` — `nil` means proof invalid.
4. Decode `payload` as a `LedgerHashes` SLE; extract `Hashes` vector
   (`ledger/entry/ledger_hashes.go`).
5. On success, transition to `StateComplete` and stash the vector.

Caller obtains `target.AccountHash` either from a header it already has
or from a fresh inbound header acquisition. The simplest entry point is
"we already have the target ledger header (peer told us its hash + we
acquired the header) and now want a 256-hash window before it".

### Phase 3 — Coordinator extension: `Replayer.AcquireSkipList`

Extend `Replayer` (`internal/ledger/inbound/replayer.go`) with a
sibling-shaped API:

```go
func (r *Replayer) AcquireSkipList(targetHash, stateHash [32]byte,
    peerID uint64) (*SkipListAcquire, error)
func (r *Replayer) HasSkipList(targetHash [32]byte) bool
func (r *Replayer) HandleSkipListResponse(resp *message.ProofPathResponse)
    (*SkipListAcquire, error)
func (r *Replayer) CompleteSkipList(hash [32]byte)
func (r *Replayer) AbandonSkipList(hash [32]byte)
```

Slot accounting reuses the same `maxInFlight` counter so the global cap
is shared between skip-list and delta acquisitions — these are both
"peer state-fetches in flight".

### Phase 4 — `LedgerReplayTask`

New file `internal/ledger/inbound/replay_task.go`:

```go
type LedgerReplayTask struct {
    tipHash   [32]byte
    tipSeq    uint32
    depth     uint32         // 1..256
    replayer  *Replayer
    sender    SenderIface    // RequestProofPath, RequestReplayDelta
    ledgerSvc LedgerService  // to look up the target header / state hash
    onProgress func(hash [32]byte, seq uint32, l *ledger.Ledger)
    state State              // WantSkipList / RunningDeltas / Complete / Failed
}

func (t *LedgerReplayTask) Start(peerID uint64) error
func (t *LedgerReplayTask) OnSkipList(hashes [][32]byte)
func (t *LedgerReplayTask) OnDelta(hash [32]byte, l *ledger.Ledger)
```

Behaviour:

1. `Start` calls `replayer.AcquireSkipList(...)`, then
   `sender.RequestProofPath(peerID, tipHash, keylet.LedgerHashes().Key,
   LedgerMapAccountState)`.
2. When the skip-list response is verified, `OnSkipList` takes the last
   `depth` ancestor hashes and fans them out via
   `replayer.Acquire(hash, peerID, parent)` — but bounded by the
   existing `DefaultMaxInFlightReplays=16` global cap and
   `MaxPerPeerReplays=2` per-peer cap. Excess hashes queue inside the
   task and are dispatched as in-flight slots free up (in `OnDelta`).
3. `OnDelta` adopts the acquired ledger (callback up to the adaptor),
   then fires the next queued delta. When the queue drains and all
   deltas are adopted, transition to `Complete`.
4. Failure modes:
   - Skip-list proof invalid → fall back: caller continues with the
     existing single-ledger forward acquisition path; task aborts.
   - Any delta fails its divergence check → that delta is abandoned,
     other deltas in the same task are not affected (each
     `ReplayDelta` already verifies its own AccountHash).

### Phase 5 — Router integration

Done in `internal/consensus/adaptor/router_replay_task.go`:

- `Router.StartReplayTask(tipHash, stateHash, tipSeq, depth, anchorParent, peers)`
  is the explicit entry point. It registers an `activeReplayTask` on
  the router and kicks off the underlying `inbound.LedgerReplayTask`.
- `handleProofPathResponse` decodes inbound `TMProofPathResponse`,
  routes to the active task, and populates the chain-hash lookup so
  subsequent replay-delta responses are recognised as task-owned.
- `handleReplayDeltaResponse` consults the chain-hash set
  (`routeDeltaToActiveTask`) BEFORE the legacy `Replayer.HandleResponse`
  path. Task-owned hashes are handled by the task's
  `OnDeltaResponse`; everything else continues through the existing
  Apply+adopt flow untouched.
- `onTaskDeltaVerified` stashes the verified `*ReplayDelta` and runs
  `drainTaskChain`, which walks the chain in oldest-first order and
  calls `rd.SetParent(predecessor) → Apply → adoptVerifiedLedger`
  for each entry whose parent is now adopted. The drain is
  re-entrant via subsequent callbacks but the monotonic
  `nextSeqToAdopt` cursor + mutex guarantee a single advance per
  successful Apply.
- `ReplayDelta.SetParent(parent)` is a small additive method on
  `inbound.ReplayDelta` (refuses overwrite once non-nil) — needed
  because the task acquires intermediates with parent=nil to allow
  parallel framing-verification, then binds the real parent at
  Apply time.

#### Auto-trigger from `checkBehind`: NOT WIRED

The constant `multiLedgerCatchupThreshold = 4` is defined for the
future hook but `checkBehind` continues to call the single-ledger
`startLedgerAcquisition` path unchanged. Reason: `TMStatusChange`
does NOT carry `AccountHash`, and `SkipListAcquire` cannot verify a
peer's skip-list proof without the tip's stateHash. Two ways to
close this:

1. Acquire the tip's header first via the existing inbound flow,
   read `AccountHash` from the verified header, then call
   `StartReplayTask`. Requires inbound.Ledger to surface a
   "header-acquired" hook that doesn't immediately trigger
   single-ledger adoption.
2. Extend our local peer-state tracking so a peer's `AccountHash`
   for its tip seq is recorded out-of-band (e.g., via a follow-up
   exchange after status-change).

Either route is its own design decision — keeping it as a separate
follow-up means Phase 5 lands a clean, fully-tested router seam
without speculative protocol changes.

#### Integration tests (Phase 5)

`internal/consensus/adaptor/router_replay_task_test.go`:

- **`TestRouter_ReplayTask_EndToEnd_3LedgerWalk`** — builds a real
  3-ledger chain via `ledger.NewOpen+Close`, mounts a synthetic
  LedgerHashes SLE for the proof, drives the router through
  StartReplayTask + proof-path response + 3 replay-delta
  responses, and asserts every chain ledger is adopted with the
  correct hash. Exercises the full Apply+adopt path end-to-end —
  the engine actually re-runs the empty close.
- **`TestRouter_ReplayTask_RejectsBadProof`** — tampered proof
  aborts the task cleanly without firing any replay-delta requests.
- **`TestRouter_ReplayTask_DoubleStartRejected`** — second
  StartReplayTask while one is in flight returns an error and
  leaves the first task untouched.
- **`TestRouter_ReplayTask_AnchorSeqMustMatch`** — anchor-seq
  validation at the entry point catches caller miscomputation.

## Acceptance tests

All in `internal/ledger/inbound/` and `internal/consensus/adaptor/`,
following the test-helper patterns in `replay_delta_test.go` and
`router_replay_delta_test.go`:

1. **`TestSkipListAcquire_ValidProof_Accepted`** (`skiplist_acquire_test.go`):
   - Build a `LedgerHashes` SLE with 256 deterministic hashes.
   - Compute its leaf hash, build a proof path against a synthetic
     state map containing only that leaf.
   - Feed `GotResponse` with the matching `stateHash`, key, path.
   - Assert `State==Complete`, `Hashes()` returns the 256 hashes in
     the right order.

2. **`TestSkipListAcquire_InvalidProof_Rejected`** (`skiplist_acquire_test.go`):
   - Two sub-cases:
     - **Tampered leaf**: serialize a different SLE but reuse the
       original proof path. `VerifyProofPathWithValue` returns nil →
       `State==Failed`, peer charge string
       `replay-skiplist-proof-invalid`.
     - **Wrong stateHash**: correct payload, wrong target stateHash →
       same result.
   - Assert no `Hashes()` leak (returns nil/empty).

3. **`TestLedgerReplayTask_50LedgerBackwardWalk`** (`replay_task_test.go`):
   - Fabricate ledger N (tip) and ledgers N-1..N-50 (parents).
   - Local store contains only ledger N-50 (the "anchor").
   - Construct a fake sender that, on `RequestProofPath(N, ...)`,
     fires a synthetic `ProofPathResponse` containing the 256-entry
     skip-list (with the relevant 50 hashes inside it).
   - On `RequestReplayDelta(h_i, ...)`, fires a synthetic
     `ReplayDeltaResponse` with header + tx list for ledger `h_i`.
   - Drive the task, then assert that ledgers N-49..N have all been
     adopted (callback fired 50 times) and that the task is `Complete`.

4. **`TestLedgerReplayTask_ParallelismBoundedByExistingCaps`**
   (`replay_task_test.go`):
   - 50-ledger walk same as above, but the sender records the timing
     of every `RequestReplayDelta`.
   - Assert: at no point are more than `DefaultMaxInFlightReplays=16`
     deltas in flight concurrently, and at no point are more than
     `MaxPerPeerReplays=2` in flight against any single peer.
   - Assert: the task completes all 50 deltas (no deadlock).

A fifth integration test on the router seam:

5. **`TestRouter_DeepCatchup_PrefersReplayTask`**
   (`router_replay_task_test.go`):
   - Set up a recording sender; advertise that peer P has tip seq=100
     while ours is 50.
   - Status-change call to `checkBehind` should result in a single
     `RequestProofPath` (not 50 `RequestReplayDelta` calls), followed
     by the bounded fan-out.
   - Below the threshold (gap=2), `checkBehind` continues to take the
     single-acquisition forward path — assert one
     `RequestReplayDelta` and zero `RequestProofPath` calls in that
     scenario.

## Non-goals (intentional)

- **No** rippled-protocol-level `MAX_TASKS=10` enforcement — we have
  only one driver (`checkBehind`), and dedup happens at the
  `Replayer.HasSkipList(hash)` level. We can revisit if a second
  driver is added.
- **No** task-level retry timer separate from the per-acquisition
  timers. `ReplayDelta` already does peer-swap on
  `SUB_TASK_TIMEOUT`; `SkipListAcquire` will reuse the same per-task
  timer (250ms swap, 10 max). Failure of the skip-list proof aborts the
  whole task — caller falls back to single-acquisition. This is
  simpler than rippled's `TASK_TIMEOUT=500ms` + double-counting and
  matches our coarser timeout model.
- **No** WebSocket `ledger_replay` admin RPC. (Future work.)

## Risk surface

- `SkipListAcquire` is a new peer-trust boundary — every proof must be
  cryptographically verified before any hash is exposed downstream.
  `VerifyProofPathWithValue` returning nil **must** be a hard fail with
  bad-data charge.
- The 256-hash window in `keylet::skip()` is rolling, so for tips at
  seq N the skip-list contains parents of ledgers N..N-255. We must
  verify the proof against `target.AccountHash` (the tip we're trying
  to walk back from), and verify ancestor hashes against the
  per-ledger headers we then acquire via replay-delta — not skip the
  per-ledger verification.

## File-by-file

| New / changed | File |
|---|---|
| new | `internal/ledger/inbound/skiplist_acquire.go` |
| new | `internal/ledger/inbound/skiplist_acquire_test.go` |
| new | `internal/ledger/inbound/replay_task.go` |
| new | `internal/ledger/inbound/replay_task_test.go` |
| edit | `internal/ledger/inbound/replayer.go` (AcquireSkipList + Has/Complete/Abandon variants) |
| edit | `internal/ledger/inbound/replay_delta.go` (`SetParent` for post-acquisition parent binding) |
| edit | `internal/consensus/adaptor/sender.go` (RequestProofPath) |
| edit | `internal/consensus/adaptor/adaptor.go` (Sender interface) |
| edit | `internal/consensus/adaptor/router.go` (TypeProofPathResponse dispatch, task-aware replay-delta routing, Router struct fields for activeTask) |
| new | `internal/consensus/adaptor/router_replay_task.go` (Router task registry + chain-order adoption drain) |
| new | `internal/consensus/adaptor/router_replay_task_test.go` (end-to-end integration tests) |
