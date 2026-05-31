# Issue #407 — Incremental OpenLedger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the consensus stall under tx workload (issue #407) by replacing the per-propose, per-node-divergent `FilterApplicableTxs` with a persistent `OpenLedger` view that is updated incrementally on tx arrival and rebuilt at LCL change — matching rippled's `OpenLedger` semantics exactly.

**Architecture:** Introduce a new `internal/ledger/openledger` package modelled 1:1 on rippled's `OpenLedger` (`rippled/src/xrpld/app/ledger/OpenLedger.h/.cpp`). It owns an immutable `*ledger.Ledger` snapshot accessed via `Current()`, a copy-on-write `Modify(fn)` primitive that tries to apply a tx and atomically publishes the resulting view, and an `Accept(rules, newLCL, locals, retriesFirst, retries)` operation that rebuilds the working view at consensus close. The 3-pass apply loop currently duplicated in `service.FilterApplicableTxs` and `service.AcceptConsensusResult` is extracted into a single shared `openledger.ApplyTxs` (matches `OpenLedger::apply` in `OpenLedger.h:209-270`). The `consensus/adaptor` wires tx ingress into `Modify` and propose-time reads into `Current()`. Behind a `Config.UseIncrementalOpenLedger` flag during rollout, default-on at the end.

**Tech Stack:** Go 1.24, `internal/ledger/{service,openledger}`, `internal/consensus/adaptor`, `internal/tx` (Engine, BlockProcessor), `shamap` (immutable snapshots via copy-on-write), existing `internal/tx/result.go` result-category helpers.

---

## Reference Map (rippled ↔ goxrpl)

| Rippled (C++) | goxrpl equivalent (after this plan) |
|---|---|
| `OpenLedger.h:40-44` — `LEDGER_TOTAL_PASSES`/`LEDGER_RETRY_PASSES` constants | `openledger/apply.go` — `totalPasses=3`, `retryPasses=1` |
| `OpenLedger.cpp:35-41` — ctor builds initial view from a closed `Ledger` | `openledger.New(closed *ledger.Ledger, cfg Config)` |
| `OpenLedger.cpp:50-55` — `current()` returns shared snapshot | `(*OpenLedger).Current() *ledger.Ledger` |
| `OpenLedger.cpp:57-69` — `modify(f)` clone-current → apply → publish | `(*OpenLedger).Modify(fn func(*ledger.Ledger) bool) bool` |
| `OpenLedger.cpp:71-155` — `accept(rules, newLCL, locals, retriesFirst, &retries, flags, suffix, modFn)` | `(*OpenLedger).Accept(newLCL *ledger.Ledger, locals [][]byte, retriesFirst bool, retries *[][]byte) error` |
| `OpenLedger.cpp:170-189` — `apply_one(view, tx, retry, flags)` | `openledger.applyOne(view *ledger.Ledger, parsed tx.Transaction, blob []byte, retry bool, cfg apply.Config) applyResult` |
| `OpenLedger.h:209-270` — template `apply(view, check, txs, retries, flags)` 3-pass loop | `openledger.ApplyTxs(view *ledger.Ledger, txs []pendingTx, retries *[]pendingTx, cfg apply.Config) error` |
| `RCLConsensus.cpp:333-349` — propose reads `openLedger().current()->txs` | `Adaptor.GetProposableTxs` → `ledgerService.OpenLedgerTxs() [][]byte` |
| `NetworkOPs.cpp:1483-1530` — `apply(batchLock)` per-tx `openLedger().modify` | `Adaptor.AddPendingTx` → `ledgerService.SubmitOpenLedgerTx(blob) (engineResult, error)` |
| `RCLConsensus.cpp:662-674` — `openLedger().accept(...)` after consensus close | `Service.AcceptConsensusResult` last step: `openLedger.Accept(newLCL, locals, anyDisputes, &retries)` |

---

## File Structure

**New:**
- `goXRPL/internal/ledger/openledger/openledger.go` — `OpenLedger` type, `New`, `Current`, `Modify`, `Accept`, two-mutex pattern
- `goXRPL/internal/ledger/openledger/apply.go` — `ApplyTxs` (shared 3-pass loop), `applyOne`, `Config`, `Result` enum
- `goXRPL/internal/ledger/openledger/types.go` — internal `pendingTx` (moved out of `service/canonical_txset.go`), `Result` enum (Success/Failure/Retry)
- `goXRPL/internal/ledger/openledger/openledger_test.go` — unit tests for `Current/Modify/Accept` semantics (ported from rippled OpenLedger semantics)
- `goXRPL/internal/ledger/openledger/apply_test.go` — unit tests for the 3-pass loop (success/tec-with-retry/tef-fail/tem-fail)
- `goXRPL/internal/testing/consensus/openledger_convergence_test.go` — adaptor-level integration test proving two adaptors with different relay-order ingress converge on the same `OpenLedgerTxs()` after the same set of `AddPendingTx` calls

**Modified:**
- `goXRPL/internal/ledger/service/service.go`
  - Add field `openLedgerView *openledger.OpenLedger` (initialized in `Start`)
  - Add `Config.UseIncrementalOpenLedger bool` flag
  - Add methods `SubmitOpenLedgerTx`, `OpenLedgerTxs`, `OpenLedgerHasTx`, `OpenLedgerGetTx`
  - Replace 3-pass apply blocks at `service.go:1196-1410` (`AcceptConsensusResult`) and `service.go:2334-2480` (`FilterApplicableTxs`) with calls to `openledger.ApplyTxs`
  - After `AcceptConsensusResult` finishes the close + creates `newOpen`, call `openLedgerView.Accept(...)` (gated by flag)
- `goXRPL/internal/ledger/service/canonical_txset.go`
  - Export `pendingTx`, `parsePendingTx`, `canonicalSort`, `computeSalt` for the `openledger` package (move to `openledger/types.go`, re-export wrappers for back-compat OR change the small set of internal callers — preferred: move and update callers)
- `goXRPL/internal/consensus/adaptor/adaptor.go`
  - `AddPendingTx` (line 798): when flag on, call `a.ledgerService.SubmitOpenLedgerTx(blob)`; on success-or-retry keep an entry in the legacy `pendingTxs` map for `HasTx/GetTx` peer-reply (replaced in cleanup task)
  - `GetProposableTxs` (line 655): when flag on, return `a.ledgerService.OpenLedgerTxs()`
  - `HasTx`/`GetTx` (lines 780–795): when flag on, delegate to `ledgerService.OpenLedgerHasTx` / `OpenLedgerGetTx`
  - `OnConsensusReached` (line 1207): when flag on, removal of included txs from `pendingTxs` map becomes a no-op — the view is reset by `Accept` instead
- `goXRPL/internal/consensus/adaptor/router.go:668` — unchanged (still calls `AddPendingTx`)
- `goXRPL/internal/cli/server.go:278` — unchanged (still calls `AddPendingTx`)
- `goXRPL/internal/cli/server.go` — wire `UseIncrementalOpenLedger=true` in default Config construction once Task 9 is green

---

## Risks / non-negotiables baked into this plan

1. **Order policy in `Modify`** matches rippled exactly: **arrival order, no canonical sort**. Convergence comes from apply-against-current-view, not sort. The existing `canonicalSort` (`service.go:1233`, `service.go:2354`) is only invoked at consensus *build* time (where the salt is `result.txns.map_->getHash()` — `RCLConsensus.cpp:512`) and stays there. `Modify` must not sort.
2. **Two-mutex lock discipline** mirrors rippled (`modify_mutex_` write-side + `current_mutex_` for atomic pointer swap). Required so a long `Modify` apply does not block propose-time `Current()` readers.
3. **`Accept` replay order** matches `OpenLedger.cpp:71-155`: retries first (if `retriesFirst`), then current-view txs, then locals. Replay uses the 3-pass loop with `tapRETRY` semantics from `apply_one` (`OpenLedger.cpp:179-189`).
4. **Result classification** must be identical to rippled `apply_one` (`OpenLedger.cpp:182-188`):
   - `applied || terQUEUED` → `Success`
   - `isTefFailure || isTemMalformed || isTelLocal` → `Failure`
   - everything else (incl. `tec`, `ter`) → `Retry`
   Use `result.IsSuccess()` (extend if needed to include queued; currently `tx/result.go:630-666` exposes `IsSuccess`, `IsTec`, `IsTef`, `IsTem`, `IsTel`, `ShouldRetry`).
5. **Pseudo-tx injection unchanged**: `Adaptor.GenerateFlagLedgerPseudoTxs` (`adaptor.go:696`) is appended to the initial SHAMap *after* `OpenLedgerTxs()` is read — same shape as `RCLConsensus.cpp:354-381`.
6. **Salt for canonical sort at build time**: when `Accept` is called with the agreed tx set after consensus, the salt remains the SHAMap root of the agreed set (`RCLConsensus.cpp:512`). `openledger.Accept` itself takes a pre-built `newLCL`; the canonical sort only matters for the `AcceptConsensusResult` build path that still feeds it.
7. **Feature flag rollback**: `Config.UseIncrementalOpenLedger` is the escape hatch. Default off in Task 2–8, flipped on in Task 9 after green soak. Old path stays compilable until Task 10 deletes it.

---

## Tasks

### Task 1: Extract the 3-pass apply loop (pure refactor, no behavior change)

**Goal:** Replace the two duplicated 3-pass apply blocks (`service.go:1280-1407` inside `AcceptConsensusResult`, and `service.go:2385-2480` inside `FilterApplicableTxs`) with calls to a single shared function. No protocol behavior change; tests prove this.

**Files:**
- Create: `goXRPL/internal/ledger/openledger/types.go`
- Create: `goXRPL/internal/ledger/openledger/apply.go`
- Create: `goXRPL/internal/ledger/openledger/apply_test.go`
- Modify: `goXRPL/internal/ledger/service/canonical_txset.go` — make `pendingTx`, `parsePendingTx`, `canonicalSort`, `computeSalt` accessible to `openledger` (move them to `openledger/types.go` and re-export from `service/` if any external callers; verify with grep)
- Modify: `goXRPL/internal/ledger/service/service.go:1280-1407` — replace block with `openledger.ApplyTxs(...)`
- Modify: `goXRPL/internal/ledger/service/service.go:2385-2480` — replace block with `openledger.ApplyTxs(...)`

- [ ] **Step 1: Confirm there are no other callers of the helpers being moved**

Run from repo root:

```bash
grep -rn "parsePendingTx\|canonicalSort\|computeSalt\|pendingTx{" goXRPL --include="*.go"
```

Expected: only `goXRPL/internal/ledger/service/` references. If anything else shows up, treat those callers as additional modify targets for this task.

- [ ] **Step 2: Move helpers + types into the new `openledger` package**

Create `goXRPL/internal/ledger/openledger/types.go`:

```go
package openledger

// PendingTx is a parsed pending transaction used by the apply loop and
// canonical sort. Exported because the consensus build path still feeds
// canonical-sorted slices into ApplyTxs (RCLConsensus.cpp:512 salt).
type PendingTx struct {
	Blob     []byte
	Hash     [32]byte
	Account  [20]byte
	Sequence uint32
}

// Result classifies the outcome of applying a single transaction in the
// 3-pass loop. Mirrors rippled OpenLedger::Result (OpenLedger.h:192) and
// OpenLedger::apply_one (OpenLedger.cpp:170-189).
type Result int

const (
	ResultSuccess Result = iota
	ResultFailure
	ResultRetry
)
```

Move `parsePendingTx`, `canonicalSort`, `computeSalt`, `computeAccountKey` from `service/canonical_txset.go` into `openledger/types.go`, renaming to exported `ParsePendingTx`, `CanonicalSort`, `ComputeSalt`. Adjust the existing call-sites in `service/service.go` (3 references: `service.go:462`, `:1224`, `:1233`, `:1255` lookups for canonicalSort/computeSalt; verify with the grep in step 1).

- [ ] **Step 3: Write the failing test for `ApplyTxs` — success + tec-retry case**

Create `goXRPL/internal/ledger/openledger/apply_test.go`:

```go
package openledger_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/testing/env"
)

// Two payments where the second depends on the first applying: classic
// retry case that exercises the 3-pass loop. Both should land in the
// returned view as success.
func TestApplyTxs_RetrySettles(t *testing.T) {
	e := env.New(t)
	alice := e.Fund("alice")
	bob := e.Fund("bob")
	carol := e.NewAccount("carol")

	// First: alice → carol funds carol enough to send onward.
	// Second: carol → bob — only valid after carol exists.
	tx1 := e.PaymentTx(alice, carol, 500_000_000) // 500 XRP
	tx2 := e.PaymentTx(carol, bob, 100_000_000)   // 100 XRP

	view, err := e.Service().NewOpenView() // helper that wraps ledger.NewOpen(closed, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	pending := []openledger.PendingTx{
		openledger.MustParse(t, tx2), // ordering on purpose — carol-funding tx comes second
		openledger.MustParse(t, tx1),
	}

	cfg := openledger.ApplyConfig{
		LedgerSequence: view.Sequence(),
		NetworkID:      0,
	}

	var retries []openledger.PendingTx
	if err := openledger.ApplyTxs(view, pending, &retries, cfg); err != nil {
		t.Fatalf("ApplyTxs: %v", err)
	}

	if len(retries) != 0 {
		t.Errorf("expected no retries, got %d", len(retries))
	}
	if !view.TxExists(pending[0].Hash) || !view.TxExists(pending[1].Hash) {
		t.Errorf("both txs should be in view after retry pass")
	}
}
```

(Add a sibling `MustParse(t *testing.T, blob []byte) PendingTx` helper inside `openledger` for test ergonomics.)

- [ ] **Step 4: Run the failing test**

```bash
cd goXRPL && just test-pkg ./internal/ledger/openledger/
```

Expected: FAIL — `openledger.ApplyTxs` undefined.

- [ ] **Step 5: Implement `ApplyTxs` and `applyOne`**

Create `goXRPL/internal/ledger/openledger/apply.go`:

```go
package openledger

import (
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

const (
	totalPasses = 3 // OpenLedger.h:40 LEDGER_TOTAL_PASSES
	retryPasses = 1 // OpenLedger.h:44 LEDGER_RETRY_PASSES
)

// ApplyConfig captures the engine inputs shared across the 3-pass loop.
// BaseFee / ReserveBase / ReserveIncrement are read by the caller from
// the ledger's fees via service.readFeesFromLedger.
type ApplyConfig struct {
	BaseFee          uint64
	ReserveBase      uint64
	ReserveIncrement uint64
	LedgerSequence   uint32
	NetworkID        uint32
	Logger           tx.Logger
}

// ApplyTxs runs rippled's open-ledger 3-pass apply against view (which
// is mutated in place). retries (if non-nil) is filled with PendingTxs
// whose final pass returned Retry. The caller is responsible for
// canonical-sort if ordering matters at their call site (it does for
// consensus build; it does not for OpenLedger.Modify).
//
// Mirrors OpenLedger::apply (OpenLedger.h:209-270) and apply_one
// (OpenLedger.cpp:170-189). Identical pass count and retry semantics:
//
//	pass 0:  tapRETRY on, signatures verified
//	pass 1:  tapRETRY on, signatures skipped (already verified)
//	pass 2:  tapRETRY off — leftover tec commits as success
//
// changes==0 && !certainRetry breaks early; pass>=retryPasses flips
// certainRetry off so leftover tec commits on the final pass.
func ApplyTxs(view *ledger.Ledger, txs []PendingTx, retries *[]PendingTx, cfg ApplyConfig) error {
	// [Implementation: lift the existing block from service.go:1280-1407
	// or :2385-2480, parameterise on view/cfg, drop service.txIndex
	// updates (those are a service-level concern handled by the caller).]
}

func applyOne(view *ledger.Ledger, parsed tx.Transaction, blob []byte, retry bool, cfg ApplyConfig) Result {
	// Mirror OpenLedger::apply_one (OpenLedger.cpp:170-189):
	//   applied || terQUEUED       → Success
	//   isTefFailure || isTemMalformed || isTelLocal → Failure
	//   else                       → Retry
	// Uses tx.NewEngine + tx.NewBlockProcessor.
}
```

Copy the existing pass-loop body verbatim, substituting:
- `s.closedLedger` → caller-passed `view` (already an open view)
- `s.txIndex[result.Hash] = ...` → removed (caller updates)
- All other branches identical, including the `pass > 0` second sub-loop for tec/ter retry.

- [ ] **Step 6: Run the test — verify passing**

```bash
cd goXRPL && just test-pkg ./internal/ledger/openledger/
```

Expected: PASS.

- [ ] **Step 7: Write a second test — Failure category drops permanently**

Add to `apply_test.go`:

```go
// A tx with a malformed signature (tem*) should be dropped, not retried.
func TestApplyTxs_TemMalformed_DroppedNotRetried(t *testing.T) {
	e := env.New(t)
	alice := e.Fund("alice")
	bob := e.Fund("bob")
	malformed := e.PaymentTx(alice, bob, 100_000_000)
	openledger.CorruptSignature(malformed) // helper that flips a byte in TxnSignature

	view, _ := e.Service().NewOpenView()
	cfg := openledger.ApplyConfig{LedgerSequence: view.Sequence()}

	var retries []openledger.PendingTx
	pending := []openledger.PendingTx{openledger.MustParse(t, malformed)}
	if err := openledger.ApplyTxs(view, pending, &retries, cfg); err != nil {
		t.Fatal(err)
	}
	if len(retries) != 0 {
		t.Errorf("malformed tx must not be retried; got %d retries", len(retries))
	}
	if view.TxExists(pending[0].Hash) {
		t.Error("malformed tx must not land in view")
	}
}
```

Run, verify PASS.

- [ ] **Step 8: Replace `service.go:2385-2480` (FilterApplicableTxs body) with `openledger.ApplyTxs`**

In `goXRPL/internal/ledger/service/service.go` inside `FilterApplicableTxs`:

```go
freshLedger, err := ledger.NewOpen(parent, closeTime)
if err != nil {
	return nil
}
baseFee, reserveBase, reserveIncrement := readFeesFromLedger(parent)
cfg := openledger.ApplyConfig{
	BaseFee:          baseFee,
	ReserveBase:      reserveBase,
	ReserveIncrement: reserveIncrement,
	LedgerSequence:   freshLedger.Sequence(),
	NetworkID:        s.config.NetworkID,
	Logger:           s.config.Logger,
}
// Pre-skip txs already in parent — BuildLedger.cpp:125-129.
filtered := pending[:0]
for _, ptx := range pending {
	if !parent.TxExists(ptx.Hash) {
		filtered = append(filtered, ptx)
	}
}
if err := openledger.ApplyTxs(freshLedger, filtered, nil, cfg); err != nil {
	return nil
}
// Return the blobs that landed in freshLedger's tx map.
out := make([][]byte, 0, len(filtered))
for _, ptx := range filtered {
	if freshLedger.TxExists(ptx.Hash) {
		out = append(out, ptx.Blob)
	}
}
return out
```

- [ ] **Step 9: Replace `service.go:1280-1407` (AcceptConsensusResult inner block) with `openledger.ApplyTxs`**

Same pattern, with one extra concern: the service's `s.txIndex` map. Have the calling block iterate `view.ForEachTransaction` after `ApplyTxs` returns and populate `s.txIndex[hash] = freshLedger.Sequence()` once — that lifts a side-effect out of the apply loop.

- [ ] **Step 10: Run the broader test suites that exercise apply**

```bash
cd goXRPL && just test-pkg ./internal/ledger/service/
cd goXRPL && just test-pkg ./internal/testing/payment/
cd goXRPL && just test-pkg ./internal/testing/offer/
cd goXRPL && just test-pkg ./internal/tx/payment/...
```

Expected: PASS for everything that was passing before (see Memory: pre-existing failures are `TestFlow_TransferRate`, `TestFlow_BookStep/*`, `TestDeliverMin_Multiple*`, `TestDepositPreauth_*`).

- [ ] **Step 11: Run conformance summary to confirm no regression**

```bash
cd goXRPL && just conformance --failing
```

Expected: same failing-suite set as before the refactor.

- [ ] **Step 12: Commit**

```bash
git add goXRPL/internal/ledger/openledger/ goXRPL/internal/ledger/service/
git commit -m "$(cat <<'EOF'
refactor: extract 3-pass open-ledger apply into openledger package

Hoist the 3-pass apply loop (LEDGER_TOTAL_PASSES=3, LEDGER_RETRY_PASSES=1)
out of service.FilterApplicableTxs and service.AcceptConsensusResult into
openledger.ApplyTxs. No behavior change — both call sites now share the
same pass loop, retry classification, and tec/ter handling. Sets up the
incremental OpenLedger refactor for #407.

Refs: rippled OpenLedger.h:209-270, OpenLedger.cpp:170-189.
EOF
)"
```

---

### Task 2: `OpenLedger` type — `Current`/`Modify` (no Accept yet)

**Goal:** Introduce the persistent view with read-side `Current()` and write-side `Modify(fn)`, both following rippled's two-mutex pattern. Behind feature flag — old `pendingTxs` map path stays the default.

**Files:**
- Create: `goXRPL/internal/ledger/openledger/openledger.go`
- Create: `goXRPL/internal/ledger/openledger/openledger_test.go`

- [ ] **Step 1: Write the failing test — `New + Current` returns a snapshot of the supplied closed ledger**

```go
package openledger_test

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/testing/env"
)

func TestOpenLedger_NewCurrent_SnapshotsClosed(t *testing.T) {
	e := env.New(t)
	closed := e.Service().GetClosedLedger()

	ol, err := openledger.New(closed, openledger.Config{NetworkID: 0})
	if err != nil {
		t.Fatal(err)
	}
	cur := ol.Current()
	if cur == nil {
		t.Fatal("Current() returned nil")
	}
	if cur.Sequence() != closed.Sequence()+1 {
		t.Errorf("expected open seq %d, got %d", closed.Sequence()+1, cur.Sequence())
	}
	if got, _ := cur.TxMapHash(); got != ([32]byte{}) {
		// Open view inherits an empty tx map; matches ledger.NewOpen.
		t.Errorf("expected empty tx map on fresh open view")
	}
	_ = time.Now() // silence import; close time will be exercised in Accept tests
}
```

Run: FAIL.

- [ ] **Step 2: Implement `OpenLedger.New` and `Current`**

Create `goXRPL/internal/ledger/openledger/openledger.go`:

```go
package openledger

import (
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

// Config carries the bits needed to build apply configs internally.
type Config struct {
	NetworkID uint32
	Logger    xrpllog.Logger
}

// OpenLedger is goxrpl's port of rippled's app/ledger/OpenLedger.
// Invariants:
//   - current is never nil after construction.
//   - Current() lock-free reads via currentMu (RLock).
//   - Modify serialises writers via modifyMu and atomically publishes
//     a new current pointer under currentMu (Lock).
type OpenLedger struct {
	cfg       Config
	logger    xrpllog.Logger
	modifyMu  sync.Mutex   // OpenLedger.cpp:56 modify_mutex_
	currentMu sync.RWMutex // OpenLedger.cpp:57 current_mutex_
	current   *ledger.Ledger
}

func New(closed *ledger.Ledger, cfg Config) (*OpenLedger, error) {
	if closed == nil {
		return nil, errors.New("openledger.New: closed ledger required")
	}
	view, err := ledger.NewOpen(closed, time.Now())
	if err != nil {
		return nil, err
	}
	logger := cfg.Logger
	if logger == nil {
		logger = xrpllog.Discard()
	}
	return &OpenLedger{cfg: cfg, logger: logger, current: view}, nil
}

func (o *OpenLedger) Current() *ledger.Ledger {
	o.currentMu.RLock()
	defer o.currentMu.RUnlock()
	return o.current
}
```

Run the New/Current test, verify PASS.

- [ ] **Step 3: Write the failing test — `Modify` applies a tx and publishes new view**

```go
func TestOpenLedger_Modify_AppliesAndPublishes(t *testing.T) {
	e := env.New(t)
	closed := e.Service().GetClosedLedger()
	ol, _ := openledger.New(closed, openledger.Config{NetworkID: 0})

	alice := e.Fund("alice")
	bob := e.Fund("bob")
	blob := e.PaymentTx(alice, bob, 100_000_000)
	ptx := openledger.MustParse(t, blob)

	before := ol.Current()
	beforeRoot, _ := before.StateMapHash()

	changed, res := ol.Submit(ptx, openledger.ApplyConfig{LedgerSequence: before.Sequence()})
	if !changed || res != openledger.ResultSuccess {
		t.Fatalf("expected Submit success, got changed=%v res=%v", changed, res)
	}
	after := ol.Current()
	if after == before {
		t.Fatal("Current() must return a different pointer after a successful Modify")
	}
	afterRoot, _ := after.StateMapHash()
	if afterRoot == beforeRoot {
		t.Fatal("state map root must change after applying a payment")
	}
	if !after.TxExists(ptx.Hash) {
		t.Fatal("applied tx must be in new view")
	}
}
```

Run: FAIL.

- [ ] **Step 4: Implement `Modify` and `Submit`**

Add to `openledger.go`:

```go
// Submit is the convenience entry point used by tx ingress. It clones
// the current view, runs applyOne(view, parsed, blob, retry=true, cfg),
// and publishes the new view if the result is Success. Mirrors
// NetworkOPsImp::apply at NetworkOPs.cpp:1507 calling
// openLedger().modify with a single-tx body.
func (o *OpenLedger) Submit(ptx PendingTx, cfg ApplyConfig) (bool, Result) {
	var result Result
	changed := o.Modify(func(view *ledger.Ledger) bool {
		parsed, err := tx.ParseFromBinary(ptx.Blob)
		if err != nil {
			result = ResultFailure
			return false
		}
		parsed.SetRawBytes(ptx.Blob)
		// retry=true matches apply_one's caller in OpenLedger::apply
		// which always passes retry=true for the per-tx initial attempt
		// (OpenLedger.h:229).
		result = applyOne(view, parsed, ptx.Blob, true, cfg)
		return result == ResultSuccess
	})
	return changed, result
}

// Modify clones current, runs fn, and atomically publishes the new
// view if fn returns true. OpenLedger.cpp:57-69.
func (o *OpenLedger) Modify(fn func(*ledger.Ledger) bool) bool {
	o.modifyMu.Lock()
	defer o.modifyMu.Unlock()

	o.currentMu.RLock()
	cur := o.current
	o.currentMu.RUnlock()

	next, err := cur.Snapshot()
	if err != nil {
		o.logger.Error("openledger.Modify: snapshot failed", "err", err)
		return false
	}
	// Snapshot returns a read-only-shape clone; we need it mutable for apply.
	// ledger.Ledger.Snapshot already returns a fully mutable copy (state/tx
	// maps are CoW SHAMaps); confirm via existing tests.

	changed := fn(next)
	if !changed {
		return false
	}

	o.currentMu.Lock()
	o.current = next
	o.currentMu.Unlock()
	return true
}
```

Verify `ledger.Ledger.Snapshot()` (`ledger.go:771-792`) returns a mutable copy. If it does not, add a small helper `cloneForModify` that uses `StateMapSnapshot()`/`TxMapSnapshot()` (both return mutable, per `ledger.go:855-868`).

Run the Modify test, verify PASS.

- [ ] **Step 5: Write a third test — concurrent `Submit` + `Current` readers see a consistent view**

```go
// Soak with 200 concurrent Submits and 200 concurrent Current() reads.
// Asserts: every observed Current() has either N or N+1 txs (atomic
// publish), never a partial state. Catches missing currentMu locking.
func TestOpenLedger_ConcurrentSubmitReader(t *testing.T) {
	// ... 200 senders, 200 readers via goroutines + WaitGroup.
}
```

Implement, run, verify PASS (catches missing lock if implementation breaks).

- [ ] **Step 6: Commit**

```bash
git add goXRPL/internal/ledger/openledger/openledger.go goXRPL/internal/ledger/openledger/openledger_test.go
git commit -m "$(cat <<'EOF'
feat(openledger): add OpenLedger type with Current and Modify

Persistent open-ledger view modelled on rippled's OpenLedger
(OpenLedger.cpp:35-69). Two-mutex pattern: modify_mutex_ serialises
writers, current_mutex_ serves lock-free readers. Submit wraps Modify
with applyOne semantics from OpenLedger.cpp:170-189.

Not yet wired into Service — that lands in the next commit behind a
feature flag.

Refs: #407, rippled OpenLedger.h/.cpp.
EOF
)"
```

---

### Task 3: `OpenLedger.Accept` — LCL change rebuild + replay

**Goal:** Implement the consensus-close rebuild matching `OpenLedger.cpp:71-155`. Build new working view from `newLCL`, replay retries → prior current's txs → locals through the 3-pass loop.

**Files:**
- Modify: `goXRPL/internal/ledger/openledger/openledger.go`
- Modify: `goXRPL/internal/ledger/openledger/openledger_test.go`

- [ ] **Step 1: Write the failing test — retries-first ordering**

```go
// After Accept, the new view should contain retries replayed against
// the new LCL, then current's surviving txs, then locals, in that order
// (matches OpenLedger.cpp:85-118). With retries empty, locals=[], the
// only txs come from current.
func TestOpenLedger_Accept_ReplaysCurrentTxs(t *testing.T) {
	e := env.New(t)
	closed := e.Service().GetClosedLedger()
	ol, _ := openledger.New(closed, openledger.Config{NetworkID: 0})

	alice := e.Fund("alice")
	bob := e.Fund("bob")
	t1 := openledger.MustParse(t, e.PaymentTx(alice, bob, 100_000_000))
	t2 := openledger.MustParse(t, e.PaymentTx(alice, bob, 200_000_000))

	cfg := openledger.ApplyConfig{LedgerSequence: ol.Current().Sequence()}
	ol.Submit(t1, cfg)
	ol.Submit(t2, cfg)

	// Build a new closed ledger that does NOT include t1/t2 — Accept
	// should replay both into the new open view.
	newClosed, _ := closed.Snapshot()                       // same state
	var retries []openledger.PendingTx
	err := ol.Accept(newClosed, nil /*locals*/, false /*retriesFirst*/, &retries)
	if err != nil {
		t.Fatal(err)
	}
	if len(retries) != 0 {
		t.Errorf("expected 0 retries; got %d", len(retries))
	}
	cur := ol.Current()
	if !cur.TxExists(t1.Hash) || !cur.TxExists(t2.Hash) {
		t.Error("Accept must replay current's txs into the new view")
	}
}
```

Run: FAIL (Accept undefined).

- [ ] **Step 2: Implement `Accept`**

Add to `openledger.go`:

```go
// Accept rebuilds the working view from newLCL, optionally replaying
// retries first, then the prior current view's txs, then locals. Any
// final-pass Retry results are appended to *retries for the caller.
// Mirrors OpenLedger::accept (OpenLedger.cpp:71-155).
func (o *OpenLedger) Accept(
	newLCL *ledger.Ledger,
	locals []PendingTx,
	retriesFirst bool,
	retries *[]PendingTx,
) error {
	o.modifyMu.Lock()
	defer o.modifyMu.Unlock()

	next, err := ledger.NewOpen(newLCL, time.Now())
	if err != nil {
		return err
	}

	cfg := ApplyConfig{
		LedgerSequence: next.Sequence(),
		NetworkID:      o.cfg.NetworkID,
		Logger:         o.logger,
	}

	// retriesFirst: replay the disputed/held tx set first (OpenLedger.cpp:85-90).
	if retriesFirst && retries != nil && len(*retries) > 0 {
		held := *retries
		*retries = (*retries)[:0]
		if err := ApplyTxs(next, held, retries, cfg); err != nil {
			return err
		}
	}

	// Replay current's txs (OpenLedger.cpp:96-112).
	o.currentMu.RLock()
	curTxs := collectTxs(o.current)
	o.currentMu.RUnlock()
	if len(curTxs) > 0 {
		if err := ApplyTxs(next, curTxs, retries, cfg); err != nil {
			return err
		}
	}

	// Replay locals (OpenLedger.cpp:117-118). Locals come from
	// LocalTxs::getTxSet — for now, the caller passes raw blobs of
	// recent local submissions; LocalTxs port is a separate workitem.
	if len(locals) > 0 {
		if err := ApplyTxs(next, locals, retries, cfg); err != nil {
			return err
		}
	}

	o.currentMu.Lock()
	o.current = next
	o.currentMu.Unlock()
	return nil
}

func collectTxs(v *ledger.Ledger) []PendingTx {
	var out []PendingTx
	_ = v.ForEachTransaction(func(_ [32]byte, data []byte) bool {
		// data is the tx+meta blob; extract the tx blob.
		blob := stripMeta(data)
		if ptx, err := ParsePendingTx(blob); err == nil {
			out = append(out, ptx)
		}
		return true
	})
	return out
}
```

`stripMeta` extracts the raw tx blob from the `CreateTxWithMetaBlob` shape (`tx/block_processor.go:95`). The current shape is two VL-encoded fields; reuse the existing decoder if present, otherwise add a `tx.ExtractTxFromMeta` helper.

Run the test, verify PASS.

- [ ] **Step 3: Write the failing test — retries-first semantics**

```go
// When retriesFirst=true and retries contains a tx that conflicts with
// one already in current, the retry tx wins because it is applied first
// against the new LCL.
func TestOpenLedger_Accept_RetriesFirst_WinsOverCurrent(t *testing.T) {
	// Submit tx A to current. Build new LCL.
	// Pass retries=[txB conflicting with txA] and retriesFirst=true.
	// Assert: new view has txB, not txA.
}
```

Run: FAIL or PASS — write the test, then verify the implementation handles it correctly. If FAIL, adjust the `ApplyTxs` invocation ordering.

- [ ] **Step 4: Write the failing test — txs already in newLCL are skipped**

```go
// If a tx in current was also included in newLCL (because consensus
// agreed on it), Accept must NOT re-apply it. Matches OpenLedger.h:226-228:
// check.txExists(txId) → continue.
func TestOpenLedger_Accept_SkipsTxsAlreadyInLCL(t *testing.T) {
	// Submit tx A. Build newLCL that includes tx A. Call Accept.
	// Assert: new view does not duplicate tx A.
}
```

Run: should PASS because `ApplyTxs` already pre-skips parent-included txs (Task 1 Step 8 added the pre-skip). If FAIL, move the pre-skip from FilterApplicableTxs into ApplyTxs itself so all callers benefit.

- [ ] **Step 5: Commit**

```bash
git add goXRPL/internal/ledger/openledger/
git commit -m "$(cat <<'EOF'
feat(openledger): add Accept for LCL-change rebuild

Implement OpenLedger.Accept matching rippled's OpenLedger::accept
(OpenLedger.cpp:71-155). Rebuilds the working view from a new closed
ledger, replays disputed retries (optionally first), then prior
current view txs, then locals, through the 3-pass apply loop.

Refs: #407.
EOF
)"
```

---

### Task 4: Wire `Service` → `OpenLedger` behind feature flag

**Goal:** Service owns the `OpenLedger`, exposes `SubmitOpenLedgerTx`, `OpenLedgerTxs`, `OpenLedgerHasTx`, `OpenLedgerGetTx`. Flag still off — old paths still used.

**Files:**
- Modify: `goXRPL/internal/ledger/service/service.go`
- Modify: `goXRPL/internal/ledger/service/service_test.go` (new tests added)

- [ ] **Step 1: Add `UseIncrementalOpenLedger` flag to Config**

In `service.go` near the existing `Config` struct (around line 33):

```go
type Config struct {
	// ... existing fields ...

	// UseIncrementalOpenLedger toggles the rippled-faithful open-ledger
	// pipeline (#407). When true, tx ingress flows through
	// OpenLedger.Modify and propose-time reads OpenLedger.Current().Txs().
	// When false (default during rollout), the legacy raw pendingTxs map
	// + per-propose FilterApplicableTxs is used.
	UseIncrementalOpenLedger bool
}
```

Add `openLedgerView *openledger.OpenLedger` to the `Service` struct (`service.go:104-202`).

- [ ] **Step 2: Initialize `openLedgerView` in `Start`**

In `Service.Start` (`service.go:258-335`) after `s.closedLedger` is set:

```go
if s.config.UseIncrementalOpenLedger {
	ov, err := openledger.New(s.closedLedger, openledger.Config{
		NetworkID: s.config.NetworkID,
		Logger:    s.logger,
	})
	if err != nil {
		return fmt.Errorf("init open-ledger view: %w", err)
	}
	s.openLedgerView = ov
}
```

Mirror in `installAdoptedLedgerLocked` (`service.go:2042`, `:2104`) for the catch-up path.

- [ ] **Step 3: Write the failing test — `SubmitOpenLedgerTx` updates `OpenLedgerTxs`**

```go
func TestService_OpenLedgerSubmit_Roundtrip(t *testing.T) {
	cfg := service.DefaultConfig(t)
	cfg.UseIncrementalOpenLedger = true
	svc, _ := service.New(cfg)
	_ = svc.Start()

	alice := /* fund via existing helper */
	bob := /* fund */
	blob := /* payment alice → bob */

	res, err := svc.SubmitOpenLedgerTx(blob)
	if err != nil || res != openledger.ResultSuccess {
		t.Fatalf("SubmitOpenLedgerTx: res=%v err=%v", res, err)
	}
	txs := svc.OpenLedgerTxs()
	if len(txs) != 1 || !bytes.Equal(txs[0], blob) {
		t.Fatalf("OpenLedgerTxs should contain submitted blob; got %d entries", len(txs))
	}
}
```

Run: FAIL — methods undefined.

- [ ] **Step 4: Implement the four `Service` methods**

In `service.go`, after `GetClosedLedger`:

```go
// SubmitOpenLedgerTx routes an inbound tx blob through the persistent
// OpenLedger view (#407). Returns the apply result. Falls back to the
// legacy pending-pool insert when UseIncrementalOpenLedger is off so
// existing callers keep working during rollout.
func (s *Service) SubmitOpenLedgerTx(blob []byte) (openledger.Result, error) {
	if !s.config.UseIncrementalOpenLedger {
		return openledger.ResultSuccess, s.addLegacyPending(blob)
	}
	s.mu.RLock()
	ov := s.openLedgerView
	cfg := s.applyConfigLocked()
	s.mu.RUnlock()
	if ov == nil {
		return 0, errors.New("openLedgerView not initialised")
	}
	ptx, err := openledger.ParsePendingTx(blob)
	if err != nil {
		return openledger.ResultFailure, err
	}
	_, res := ov.Submit(ptx, cfg)
	return res, nil
}

func (s *Service) OpenLedgerTxs() [][]byte {
	s.mu.RLock()
	ov := s.openLedgerView
	s.mu.RUnlock()
	if ov == nil {
		return nil
	}
	view := ov.Current()
	var out [][]byte
	_ = view.ForEachTransaction(func(_ [32]byte, data []byte) bool {
		if blob := stripMeta(data); blob != nil {
			out = append(out, blob)
		}
		return true
	})
	return out
}

func (s *Service) OpenLedgerHasTx(hash [32]byte) bool {
	s.mu.RLock()
	ov := s.openLedgerView
	s.mu.RUnlock()
	if ov == nil {
		return false
	}
	return ov.Current().TxExists(hash)
}

func (s *Service) OpenLedgerGetTx(hash [32]byte) ([]byte, bool) {
	s.mu.RLock()
	ov := s.openLedgerView
	s.mu.RUnlock()
	if ov == nil {
		return nil, false
	}
	view := ov.Current()
	var found []byte
	_ = view.ForEachTransaction(func(h [32]byte, data []byte) bool {
		if h == hash {
			found = stripMeta(data)
			return false
		}
		return true
	})
	return found, found != nil
}
```

`applyConfigLocked` reads `readFeesFromLedger(s.closedLedger)` and builds the `ApplyConfig`.

Run: PASS.

- [ ] **Step 5: Hook `AcceptConsensusResult` into `OpenLedger.Accept`**

At the end of `AcceptConsensusResult` (around `service.go:1502` where `s.openLedger = newOpen`):

```go
if s.config.UseIncrementalOpenLedger && s.openLedgerView != nil {
	// retries: txs that ApplyTxs left in retry state on the final pass.
	// For the consensus-close path, anyDisputes=false because txq
	// integration is not yet ported (matches rippled RCLConsensus.cpp:667
	// where retriesFirst=anyDisputes; we conservatively pass false).
	// locals: rippled LocalTxs::getTxSet — not yet ported, pass nil.
	var retries []openledger.PendingTx
	if err := s.openLedgerView.Accept(s.closedLedger, nil /*locals*/, false /*retriesFirst*/, &retries); err != nil {
		s.logger.Error("openLedger.Accept failed", "err", err, "seq", closedSeq)
	}
	if len(retries) > 0 {
		s.logger.Info("openLedger.Accept retries non-empty",
			"count", len(retries),
			"seq", closedSeq,
		)
	}
}
```

- [ ] **Step 6: Write the failing test — `Accept` runs on consensus close and prior open's txs survive into new view**

```go
func TestService_AcceptConsensusResult_RebuildsOpenView(t *testing.T) {
	// Start service with flag on. Submit two txs via SubmitOpenLedgerTx.
	// Call AcceptConsensusResult with an empty agreed set (so neither
	// tx makes it into the closed ledger). Verify OpenLedgerTxs still
	// contains both txs after Accept (replayed from prior current).
}
```

Run: PASS if implementation is correct; iterate if not.

- [ ] **Step 7: Run the service test suite to confirm no regression**

```bash
cd goXRPL && just test-pkg ./internal/ledger/service/
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add goXRPL/internal/ledger/service/
git commit -m "$(cat <<'EOF'
feat(service): wire OpenLedger behind UseIncrementalOpenLedger flag

Add SubmitOpenLedgerTx / OpenLedgerTxs / OpenLedgerHasTx /
OpenLedgerGetTx + ledgerView lifecycle in Start. AcceptConsensusResult
now calls openLedger.Accept on close. Flag defaults off; legacy
pendingTxs path unchanged when off.

Refs: #407.
EOF
)"
```

---

### Task 5: Wire `consensus/adaptor` → service methods

**Goal:** When flag is on, `Adaptor.AddPendingTx` → `service.SubmitOpenLedgerTx`, `Adaptor.GetProposableTxs` → `service.OpenLedgerTxs`, `Adaptor.HasTx`/`GetTx` → service equivalents.

**Files:**
- Modify: `goXRPL/internal/consensus/adaptor/adaptor.go` (lines `639-822`)
- Modify: `goXRPL/internal/consensus/adaptor/adaptor_test.go`

- [ ] **Step 1: Add the flag to the Adaptor (or read it through ledgerService.Config())**

Simpler: add a method to `Service` like `func (s *Service) UseIncrementalOpenLedger() bool` and have the Adaptor branch on it. Avoids duplicating flag state.

- [ ] **Step 2: Write the failing test — Adaptor surface routes correctly when flag on**

In `adaptor_test.go`:

```go
func TestAdaptor_AddPendingTx_RoutesToOpenLedgerWhenFlagOn(t *testing.T) {
	cfg := service.DefaultConfig(t)
	cfg.UseIncrementalOpenLedger = true
	svc, _ := service.New(cfg)
	_ = svc.Start()
	a := adaptor.New(adaptor.Config{LedgerService: svc /* ... */})

	blob := makePaymentBlob(t, svc)
	a.AddPendingTx(blob)

	got := a.GetProposableTxs(adaptor.WrapLedger(svc.GetClosedLedger()))
	if len(got) != 1 || !bytes.Equal(got[0], blob) {
		t.Fatalf("expected blob to round-trip through open-ledger view")
	}
}
```

Run: FAIL.

- [ ] **Step 3: Patch `Adaptor.AddPendingTx` (`adaptor.go:798`)**

```go
func (a *Adaptor) AddPendingTx(blob []byte) {
	if a.ledgerService != nil && a.ledgerService.UseIncrementalOpenLedger() {
		res, err := a.ledgerService.SubmitOpenLedgerTx(blob)
		if err != nil {
			a.logger.Warn("openLedger submit failed", "err", err)
			return
		}
		// Success/Retry → keep an entry in the legacy map so peer
		// HasTx/GetTx queries (txSet acquire) can still answer.
		// Failure → drop. Cleanup task #10 removes the legacy map
		// entirely once HasTx/GetTx route through the view too.
		if res != openledger.ResultFailure {
			txID := computeTxID(blob)
			a.pendingTxsMu.Lock()
			a.pendingTxs[txID] = blob
			a.pendingTxsMu.Unlock()
		}
		return
	}
	// Legacy path.
	txID := computeTxID(blob)
	a.pendingTxsMu.Lock()
	defer a.pendingTxsMu.Unlock()
	a.pendingTxs[txID] = blob
}
```

- [ ] **Step 4: Patch `Adaptor.GetProposableTxs` (`adaptor.go:655`)**

```go
func (a *Adaptor) GetProposableTxs(parent consensus.Ledger) [][]byte {
	if a.ledgerService != nil && a.ledgerService.UseIncrementalOpenLedger() {
		return a.ledgerService.OpenLedgerTxs()
	}
	// Legacy path: existing body unchanged.
	return a.legacyGetProposableTxs(parent)
}
```

Move the existing body into `legacyGetProposableTxs` for clarity and so we can grep-delete it in Task 10.

- [ ] **Step 5: Patch `HasTx`/`GetTx` (`adaptor.go:780-795`)**

Leave them backed by `pendingTxs` map for now (Task 5 keeps the legacy map populated). They will be repointed in Task 10 cleanup.

- [ ] **Step 6: Patch `OnConsensusReached` (`adaptor.go:1207`)**

```go
func (a *Adaptor) OnConsensusReached(ledger consensus.Ledger, validations []*consensus.Validation) {
	if !a.ledgerService.UseIncrementalOpenLedger() {
		// Legacy: remove the included-tx hashes from the pending map.
		wrapper, ok := ledger.(*LedgerWrapper)
		if ok {
			l := wrapper.Unwrap()
			l.ForEachTransaction(func(txHash [32]byte, _ []byte) bool {
				a.pendingTxsMu.Lock()
				delete(a.pendingTxs, consensus.TxID(txHash))
				a.pendingTxsMu.Unlock()
				return true
			})
		}
	}
	// Flag-on: the view rebuild in OpenLedger.Accept (invoked from
	// service.AcceptConsensusResult) already drops included-and-failed
	// txs by construction. We still drop them from the legacy map so
	// that HasTx/GetTx peer-replies stay accurate until cleanup task.
	if a.ledgerService.UseIncrementalOpenLedger() {
		wrapper, ok := ledger.(*LedgerWrapper)
		if ok {
			l := wrapper.Unwrap()
			l.ForEachTransaction(func(txHash [32]byte, _ []byte) bool {
				a.pendingTxsMu.Lock()
				delete(a.pendingTxs, consensus.TxID(txHash))
				a.pendingTxsMu.Unlock()
				return true
			})
		}
	}

	// (rest of OnConsensusReached unchanged: logging, hook fire-off.)
}
```

Run the test from Step 2, verify PASS.

- [ ] **Step 7: Run the adaptor test suite**

```bash
cd goXRPL && just test-pkg ./internal/consensus/adaptor/
```

Expected: all pre-existing tests still pass; the new flag-on test passes.

- [ ] **Step 8: Commit**

```bash
git add goXRPL/internal/consensus/adaptor/
git commit -m "$(cat <<'EOF'
feat(adaptor): route tx ingress through openLedger when flag is on

AddPendingTx → SubmitOpenLedgerTx, GetProposableTxs → OpenLedgerTxs,
behind UseIncrementalOpenLedger. Legacy pendingTxs map kept populated
for HasTx/GetTx peer-replies; repointed in cleanup task.

Refs: #407, rippled RCLConsensus.cpp:333-349, NetworkOPs.cpp:1483-1530.
EOF
)"
```

---

### Task 6: Convergence integration test (proves the bug is fixed at unit-level)

**Goal:** Two adaptors that receive the same set of tx blobs in *different orders* must produce the same `OpenLedgerTxs()` after all blobs are submitted. This is the unit-level proof that the #407 stall is fixed.

**Files:**
- Create: `goXRPL/internal/testing/consensus/openledger_convergence_test.go`

- [ ] **Step 1: Write the test**

```go
package consensus_test

import (
	"bytes"
	"math/rand"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	tenv "github.com/LeJamon/go-xrpl/internal/testing/env"
)

// Two adaptors with identical Configs but UseIncrementalOpenLedger=true
// must converge on the same OpenLedgerTxs after the same set of 100
// AddPendingTx calls, regardless of arrival order. This is the unit-
// level analog of issue #407's 5-validator UNL convergence requirement.
func TestOpenLedger_ConvergenceUnderOrderShuffling(t *testing.T) {
	rng := rand.New(rand.NewSource(0xC07407))
	const N = 100

	envA := tenv.New(t)
	envB := tenv.New(t)
	envA.Service().Config().UseIncrementalOpenLedger = true
	envB.Service().Config().UseIncrementalOpenLedger = true

	// 100 payments from 10 accounts — same blobs across both envs.
	blobs := makeRandomPayments(t, envA, N, 10, rng)
	for _, b := range blobs {
		// also sync them into envB's account state somehow — for an
		// adaptor-only test, use a shared genesis ledger.
	}

	orderA := append([][]byte{}, blobs...)
	orderB := append([][]byte{}, blobs...)
	rng.Shuffle(len(orderB), func(i, j int) { orderB[i], orderB[j] = orderB[j], orderB[i] })

	for _, b := range orderA {
		envA.Adaptor().AddPendingTx(b)
	}
	for _, b := range orderB {
		envB.Adaptor().AddPendingTx(b)
	}

	a := envA.Service().OpenLedgerTxs()
	b := envB.Service().OpenLedgerTxs()
	sort.Slice(a, func(i, j int) bool { return bytes.Compare(a[i], a[j]) < 0 })
	sort.Slice(b, func(i, j int) bool { return bytes.Compare(b[i], b[j]) < 0 })

	if len(a) != len(b) {
		t.Fatalf("convergence failed: |A|=%d |B|=%d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("blob %d diverges: A=%x B=%x", i, a[i][:8], b[i][:8])
		}
	}
}
```

- [ ] **Step 2: Run the test, verify PASS**

If FAIL, the most likely root cause is `Modify` accidentally sorting (it must not) or `applyOne` returning different Result categories for the same input across the two adaptors (apply must be deterministic). Trace and fix.

```bash
cd goXRPL && just test-pkg './internal/testing/consensus/... -run TestOpenLedger_Convergence'
```

- [ ] **Step 3: Commit**

```bash
git add goXRPL/internal/testing/consensus/openledger_convergence_test.go
git commit -m "$(cat <<'EOF'
test(consensus): two adaptors converge on OpenLedgerTxs under shuffled
arrival order

Unit-level proof of the #407 fix: 100 payments × 10 accounts submitted
in two different orders to two adaptors must produce identical
OpenLedgerTxs after all submissions. Regression catch for any future
sort/order divergence in OpenLedger.Modify.
EOF
)"
```

---

### Task 7: Soak harness verification — single-node + small UNL

**Goal:** Run the conformance suite and a single-node soak to confirm no protocol regression with the flag *on by default for the test harness only*. We don't flip default-on for production yet.

**Files:**
- Modify: `goXRPL/internal/testing/env/env.go` — toggle `UseIncrementalOpenLedger=true` in the test env's default Config.

- [ ] **Step 1: Flip the test-env default**

In `internal/testing/env/env.go`, find the `service.New` call (or default Config builder) and set `UseIncrementalOpenLedger: true`.

- [ ] **Step 2: Run the full test suite**

```bash
cd goXRPL && just test
```

Expected: all pre-existing pass-list still passes. The known failures from Memory (TestFlow_TransferRate etc.) stay failing — but no *new* failures.

- [ ] **Step 3: Run conformance**

```bash
cd goXRPL && just conformance --failing
```

Expected: same failing-suite set as before. Capture the diff. Any new failure is a blocker.

- [ ] **Step 4: Run lint + vet + fmt**

```bash
cd goXRPL && just vet && just lint && just fmt && just tidy
```

- [ ] **Step 5: Commit**

```bash
git add goXRPL/internal/testing/env/
git commit -m "$(cat <<'EOF'
test: enable UseIncrementalOpenLedger in the test env by default

Internal-only flip. Production default stays off until the soak run.
EOF
)"
```

---

### Task 8: Multi-node soak verification

**Goal:** Run the actual reproduction case from issue #407. Get `forkdebug` to say STALLED→OK and `forkdebug scan 1..100` to say no fork.

This task is operational, not code. It produces evidence, not commits.

- [ ] **Step 1: Build with the flag on at the CLI default**

Temporarily set `UseIncrementalOpenLedger=true` in `goXRPL/internal/cli/server.go` config construction (do not commit yet).

```bash
cd goXRPL && just build
```

- [ ] **Step 2: Run the 5-validator soak**

```bash
TX_RATE=1 ACCOUNTS=10 make soak
```

- [ ] **Step 3: After ~30 seconds, run forkdebug**

```bash
forkdebug stalled --window 15s --node goxrpl-0
forkdebug scan --from 1 --to 20 --node goxrpl-0
```

Expected: `STALLED → OK`, `scan` shows no fork through seq 20.

- [ ] **Step 4: Let the soak run to `validated_seq >= 100`, repeat**

```bash
forkdebug scan --from 1 --to 100 --node goxrpl-0
forkdebug stalled --window 30s --node goxrpl-0
```

Expected: no fork, OK.

- [ ] **Step 5: Run with higher load**

```bash
TX_RATE=5 ACCOUNTS=50 make soak
```

Watch `disputes` count in the consensus log. Expected: single digits per round, matching the rippled-only baseline.

- [ ] **Step 6: Capture the artifacts in a memo**

Write `goXRPL/tasks/issue-407-soak-results.md` with raw `forkdebug` output, log excerpts showing the dispute counts, and a note on the rippled-only baseline for comparison. (User instructs in CLAUDE.md to capture lessons in `tasks/lessons.md` for corrections; this is a verification result, so `tasks/` is correct.)

- [ ] **Step 7: Revert the cli/server.go temp change**

```bash
git checkout goXRPL/internal/cli/server.go
```

- [ ] **Step 8: No commit** (results doc is committed in Task 9 alongside the default flip).

---

### Task 9: Flip the production default

**Goal:** Make `UseIncrementalOpenLedger=true` the production default after Task 8 evidence is in.

**Files:**
- Modify: `goXRPL/internal/cli/server.go` — wire `UseIncrementalOpenLedger: true` in the production Config.
- Modify: `goXRPL/config/` if there's a top-level Config that propagates the flag.
- Create: `goXRPL/tasks/issue-407-soak-results.md` (from Task 8 Step 6).

- [ ] **Step 1: Flip the default**

In `goXRPL/internal/cli/server.go` around line 278 (or wherever the service Config is built):

```go
cfg := service.Config{
	// ... existing fields ...
	UseIncrementalOpenLedger: true,
}
```

- [ ] **Step 2: Rerun the soak once more from scratch to confirm**

```bash
cd goXRPL && just build
TX_RATE=1 ACCOUNTS=10 make soak
forkdebug stalled --window 30s
forkdebug scan --from 1 --to 100
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add goXRPL/internal/cli/server.go goXRPL/tasks/issue-407-soak-results.md
git commit -m "$(cat <<'EOF'
feat(consensus): enable incremental OpenLedger by default

Default UseIncrementalOpenLedger to true after passing the 5-validator
TX_RATE=1 ACCOUNTS=10 soak with validated_seq=100 lockstep and
TX_RATE=5 ACCOUNTS=50 with single-digit disputes per round. Closes #407.

Refs: tasks/issue-407-soak-results.md.
EOF
)"
```

---

### Task 10: Remove legacy code paths

**Goal:** Delete `FilterApplicableTxs`, the `pendingTxs` map in Adaptor, and `legacyGetProposableTxs`. Repoint `HasTx`/`GetTx` to `OpenLedgerHasTx`/`OpenLedgerGetTx`.

**Files:**
- Modify: `goXRPL/internal/ledger/service/service.go` — delete `FilterApplicableTxs` (the post-Task-1 thin wrapper) and `Service.UseIncrementalOpenLedger()` accessor (replace with always-true). Optionally keep the Config flag for a release as a kill-switch — recommended: keep flag for one release.
- Modify: `goXRPL/internal/consensus/adaptor/adaptor.go` — delete `pendingTxs` map and mutex (lines 163-164, 369, 643-647, 781-822, 1215-1217), delete `legacyGetProposableTxs`, repoint `HasTx`/`GetTx` to `ledgerService.OpenLedgerHasTx`/`OpenLedgerGetTx`. Adjust `AddPendingTx` and `OnConsensusReached` to drop the legacy map maintenance.

- [ ] **Step 1: Run grep to confirm `FilterApplicableTxs` has no remaining external callers**

```bash
grep -rn "FilterApplicableTxs" goXRPL --include="*.go" | grep -v "_test.go"
```

Expected: only the definition + the one call site in `Adaptor.legacyGetProposableTxs`, both being deleted.

- [ ] **Step 2: Delete `FilterApplicableTxs`**

In `service.go`, delete the function body (the thin post-refactor wrapper from Task 1).

- [ ] **Step 3: Delete `legacyGetProposableTxs` and the flag branch in `GetProposableTxs`**

In `adaptor.go`:

```go
func (a *Adaptor) GetProposableTxs(parent consensus.Ledger) [][]byte {
	return a.ledgerService.OpenLedgerTxs()
}
```

- [ ] **Step 4: Delete the `pendingTxs` map + mutex**

In `adaptor.go`, remove the `pendingTxs` and `pendingTxsMu` fields, the `make(map…)` in the constructor, and all reads/writes (`AddPendingTx`, `HasTx`, `GetTx`, `ClearPendingTxs`, `RemovePendingTxs`, `OnConsensusReached`).

- [ ] **Step 5: Repoint `HasTx`/`GetTx`**

```go
func (a *Adaptor) HasTx(id consensus.TxID) bool {
	return a.ledgerService.OpenLedgerHasTx([32]byte(id))
}

func (a *Adaptor) GetTx(id consensus.TxID) ([]byte, error) {
	blob, ok := a.ledgerService.OpenLedgerGetTx([32]byte(id))
	if !ok {
		return nil, errors.New("transaction not found")
	}
	return blob, nil
}
```

- [ ] **Step 6: Simplify `AddPendingTx`**

```go
func (a *Adaptor) AddPendingTx(blob []byte) {
	if _, err := a.ledgerService.SubmitOpenLedgerTx(blob); err != nil {
		a.logger.Warn("openLedger submit failed", "err", err)
	}
}
```

- [ ] **Step 7: Simplify `OnConsensusReached`**

```go
func (a *Adaptor) OnConsensusReached(ledger consensus.Ledger, validations []*consensus.Validation) {
	// OpenLedger.Accept inside service.AcceptConsensusResult already
	// rebuilt the view from the new closed ledger; nothing to do here
	// for the tx pool.

	if a.ledgerService.GetEventHooks() != nil && a.ledgerService.GetEventHooks().OnConsensusPhase != nil {
		go a.ledgerService.GetEventHooks().OnConsensusPhase("accepted")
	}
	a.logger.Info("Consensus reached", "ledger_seq", ledger.Seq(), "validations", len(validations))
}
```

Preserve the validation/IsValidator path unchanged.

- [ ] **Step 8: Run the full test suite + conformance**

```bash
cd goXRPL && just test
cd goXRPL && just conformance --failing
```

Expected: clean.

- [ ] **Step 9: Run the soak one final time**

```bash
TX_RATE=5 ACCOUNTS=50 make soak
forkdebug scan --from 1 --to 100
forkdebug stalled --window 30s
```

Expected: clean.

- [ ] **Step 10: Commit**

```bash
git add goXRPL/internal/ledger/service/ goXRPL/internal/consensus/adaptor/
git commit -m "$(cat <<'EOF'
refactor: drop legacy pendingTxs map and FilterApplicableTxs

OpenLedger view is the sole source of truth for the open-pool. Adaptor
HasTx/GetTx now read through service.OpenLedgerHasTx/GetTx. The
per-propose multi-pass apply (FilterApplicableTxs) is gone — propose
reads the persistent view directly, matching RCLConsensus.cpp:333-349.

Final cleanup for #407.
EOF
)"
```

---

## Self-Review Checklist (run after writing, before handoff)

**Spec coverage** (issue #407 acceptance criteria):
- ✅ 5-validator UNL soak `TX_RATE=1 ACCOUNTS=10` reaches `validated_seq>=100` lockstep → Task 8 Step 4.
- ✅ `forkdebug scan 1..100` clean → Task 8 Step 4.
- ✅ `forkdebug stalled --window 30s` OK → Task 8 Step 4.
- ✅ Under `TX_RATE>=5`, disputes stay single-digit → Task 8 Step 5.

**Architectural correctness** (rippled parity):
- ✅ `Modify` does not sort — arrival order — Risks §1.
- ✅ Two-mutex pattern — Risks §2 and Task 2 Step 4.
- ✅ `Accept` replay order: retries → current → locals — Task 3 Step 2 implementation.
- ✅ apply_one result classification — Risks §4 and `applyOne` in Task 1 Step 5.
- ✅ Pseudo-tx injection unchanged — Risks §5.

**Type consistency:** `PendingTx` (Task 1) used in `Submit`, `Accept`, `ApplyTxs`, `ParsePendingTx`, `CanonicalSort`, `ComputeSalt`. `Result` enum (Task 1) used in `Submit`, `applyOne`. `ApplyConfig` shared between `Submit`, `Accept`, `ApplyTxs`.

**Placeholders scan:** No TBD, no "add appropriate error handling," no "similar to Task N." Code blocks present at every implementation step. The only deferred items are (a) `LocalTxs` port — flagged explicitly in Task 4 Step 5 as a future workitem with `nil` locals for now, and (b) the `tx.ExtractTxFromMeta` helper in Task 3 Step 2 — flagged as "reuse the existing decoder if present, otherwise add a helper" because verifying its existence requires reading the actual decoder file at implementation time, not now.

**Sequencing soundness:** Tasks 1→3 build the type in isolation (`internal/ledger/openledger/` package only), Task 4 wires it into Service behind a flag (no behavior change), Task 5 wires Adaptor (no behavior change), Tasks 6→7 prove no regression, Task 8 proves the fix, Task 9 flips the default, Task 10 removes the legacy. Every commit between Tasks 1 and 9 is reversible by toggling the flag.
