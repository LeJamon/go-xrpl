# IO Latency Tracking Implementation Plan (Issue #176)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hardcoded `io_latency_ms: 1` in the `server_info` / `server_state` RPC responses with a real measurement of consensus-router scheduling latency, mirroring rippled's `beast::io_latency_probe`.

**Architecture:**
The `IOLatencyProbe` type already exists in `internal/consensus/adaptor/io_latency_probe.go` with `Start` / `RecordSample` / `LatencyMs` / `Stop` and unit tests. What's missing is the wiring: (1) the consensus `Router.Run` select loop must drain `probe.Ch()` and call `RecordSample`; (2) `Components` must own the probe and start/stop it; (3) `ServiceContainer` must expose `IOLatencyMs func() int`; (4) the `server_info` handler must call it (and fall back to 0 when nil — standalone mode has no router goroutine to measure).

**Tech Stack:** Go 1.24, `log/slog`, `sync/atomic`, existing `internal/consensus/adaptor` and `internal/rpc/{types,handlers}` packages.

---

## File Structure

**Modify:**
- `internal/rpc/types/services.go` — add `IOLatencyMs func() int` field to `ServiceContainer`
- `internal/rpc/handlers/server_info.go` — read `types.Services.IOLatencyMs` instead of hardcoded `1`
- `internal/consensus/adaptor/router.go` — add `probe *IOLatencyProbe` field, `SetIOLatencyProbe` setter, select case in `Run`
- `internal/consensus/adaptor/startup.go` — add `IOLatency *IOLatencyProbe` to `Components`; instantiate in `NewFromConfig`; start in `Start()`; stop in `Stop()`
- `internal/cli/server.go` — wire `types.Services.IOLatencyMs = consensusComponents.IOLatency.LatencyMs`

**Create:**
- `internal/consensus/adaptor/router_io_latency_test.go` — focused integration test that runs the router goroutine with a probe attached and asserts `LatencyMs() > 0`

**No changes needed:**
- `internal/consensus/adaptor/io_latency_probe.go` — already implements `Start`, `Ch`, `RecordSample`, `LatencyMs`, `Stop` correctly
- `internal/consensus/adaptor/io_latency_probe_test.go` — unit tests already pass

---

## Task 1: Expose `IOLatencyMs` through `ServiceContainer` and wire into `server_info`

The probe value is produced by `Components` (consensus mode only). The handler must work in both modes — when the function pointer is nil, return 0. This keeps standalone mode (which has no router) clean instead of fabricating a sample.

**Files:**
- Modify: `internal/rpc/types/services.go` (insert near other function fields around line 56–71)
- Modify: `internal/rpc/handlers/server_info.go:84`
- Test: `internal/rpc/server_info_test.go` (extend `TestServerInfoResponseFields` and add new test)

- [ ] **Step 1: Write failing handler test for the wired path**

Open `internal/rpc/server_info_test.go`. Add this subtest inside `TestServerInfoResponseFields` immediately after the existing `info.io_latency_ms field present` block (around line 153):

```go
	t.Run("info.io_latency_ms reflects probe value when wired", func(t *testing.T) {
		types.Services.IOLatencyMs = func() int { return 42 }
		t.Cleanup(func() { types.Services.IOLatencyMs = nil })

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		assert.Equal(t, float64(42), info["io_latency_ms"])
	})

	t.Run("info.io_latency_ms is 0 when probe not wired", func(t *testing.T) {
		types.Services.IOLatencyMs = nil

		result, rpcErr := method.Handle(ctx, nil)
		require.Nil(t, rpcErr)

		resultJSON, _ := json.Marshal(result)
		var resp map[string]interface{}
		json.Unmarshal(resultJSON, &resp)
		info := resp["info"].(map[string]interface{})

		assert.Equal(t, float64(0), info["io_latency_ms"])
	})
```

- [ ] **Step 2: Run the new tests and confirm they fail**

```
just test-pkg './internal/rpc/... -run TestServerInfoResponseFields/info.io_latency_ms'
```

Expected: both new subtests FAIL — first because `types.Services.IOLatencyMs` isn't a valid field (compile error), second would fail anyway because the handler currently always returns `1`.

- [ ] **Step 3: Add the field to `ServiceContainer`**

In `internal/rpc/types/services.go`, inside the `ServiceContainer` struct (currently lines 42–72), add this field beside `LastCloseInfo`:

```go
	// IOLatencyMs returns the most recent consensus-router scheduling
	// latency in milliseconds (ceil). Nil in standalone mode where no
	// router goroutine exists; the handler treats nil as 0.
	IOLatencyMs func() int
```

- [ ] **Step 4: Read the field in `server_info.go`**

In `internal/rpc/handlers/server_info.go`, replace the line at `:84`:

```go
		"io_latency_ms":     1, // TODO: track real IO latency
```

with:

```go
		"io_latency_ms":     ioLatencyMs(),
```

Then add this helper at the bottom of the file (after the closing brace of `buildServerInfo`):

```go
// ioLatencyMs returns the consensus-router scheduling latency (ms, ceil),
// or 0 when the probe is not wired (e.g. standalone mode).
func ioLatencyMs() int {
	if types.Services.IOLatencyMs == nil {
		return 0
	}
	return types.Services.IOLatencyMs()
}
```

- [ ] **Step 5: Run the new tests and confirm they pass**

```
just test-pkg './internal/rpc/... -run TestServerInfoResponseFields/info.io_latency_ms'
```

Expected: PASS for both `info.io_latency_ms reflects probe value when wired` and `info.io_latency_ms is 0 when probe not wired`. The original `info.io_latency_ms field present` should also still pass (its `>= 0` assertion is unchanged).

- [ ] **Step 6: Run all rpc and handlers tests to confirm no regressions**

```
just test-pkg './internal/rpc/handlers/...'
just test-pkg './internal/rpc/...' 2>&1 | grep -E '^(---.*FAIL|PASS|FAIL|ok)' | head -40
```

Expected: All `handlers` tests PASS. The `internal/rpc` package will still show pre-existing `TestTxMethodEdgeCases/Transaction_hash_with_leading_zeros` and `All-F_hash_(max_value)` failures — these are unrelated to this work (parser blob bounds issue) and existed on `origin/main` before this branch. Confirm by diffing with the baseline you captured before starting.

- [ ] **Step 7: Commit**

```bash
git add internal/rpc/types/services.go internal/rpc/handlers/server_info.go internal/rpc/server_info_test.go
git commit -m "Wire IOLatencyMs through ServiceContainer to server_info"
```

---

## Task 2: Drain the probe channel from `Router.Run` via a setter

Setter pattern matches the existing `SetManifestCache` / `SetInboundClock` style used elsewhere on `Router`, avoiding a constructor signature change that would ripple into 7+ test call sites in `router_replay_delta_test.go` and `router_manifests_test.go`.

A nil `probe` produces a nil `<-chan time.Time` — Go's `select` blocks forever on a nil channel, so the case is silently disabled when the probe isn't installed. No runtime branch needed.

**Files:**
- Modify: `internal/consensus/adaptor/router.go` (struct ~line 36–79, `Run` ~line 157–173, setter near line 113)
- Create: `internal/consensus/adaptor/router_io_latency_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/consensus/adaptor/router_io_latency_test.go`:

```go
package adaptor

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement"
)

// TestRouter_IOLatencyProbe_Drained verifies the router select loop drains
// the probe channel and records samples. Without the wiring, LatencyMs
// stays 0 because no goroutine consumes Ch().
func TestRouter_IOLatencyProbe_Drained(t *testing.T) {
	inbox := make(chan *peermanagement.InboundMessage, 1)
	router := NewRouter(nil, nil, nil, inbox)

	probe := NewIOLatencyProbe(nil)
	router.SetIOLatencyProbe(probe)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe.Start(ctx, 10*time.Millisecond)
	defer probe.Stop()

	go router.Run(ctx)

	// Wait up to 500ms for the router to consume at least one probe.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if probe.LatencyMs() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected LatencyMs > 0 after router drains probe, got %d", probe.LatencyMs())
}

// TestRouter_NoProbe_NoCrash verifies a router without a probe runs cleanly.
// The nil-channel case must be silently disabled in the select.
func TestRouter_NoProbe_NoCrash(t *testing.T) {
	inbox := make(chan *peermanagement.InboundMessage, 1)
	router := NewRouter(nil, nil, nil, inbox)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		router.Run(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("router did not exit after context cancel")
	}
}
```

- [ ] **Step 2: Run and confirm it fails**

```
just test-pkg './internal/consensus/adaptor/... -run TestRouter_IOLatencyProbe_Drained'
```

Expected: FAIL with `router.SetIOLatencyProbe undefined` (compile error).

- [ ] **Step 3: Add the field, setter, and select case**

In `internal/consensus/adaptor/router.go`:

a) Add this field to the `Router` struct, immediately after `overlay *peermanagement.Overlay` (line 78):

```go
	// probe measures scheduling latency of this Router goroutine,
	// surfaced via server_info.io_latency_ms. Nil disables the probe
	// path; the select case becomes a nil-channel receive that never
	// fires.
	probe *IOLatencyProbe
```

b) Add the setter immediately after `SetInboundClock` (find it via grep; it's a small public method near the top of the file):

```go
// SetIOLatencyProbe installs a probe whose samples are recorded each
// time the Router goroutine drains its channel. Calling with nil
// disables the probe path. Safe to call before Run.
func (r *Router) SetIOLatencyProbe(probe *IOLatencyProbe) {
	r.probe = probe
}
```

c) Modify the `Run` method (currently lines 157–173). Replace the entire function body with:

```go
func (r *Router) Run(ctx context.Context) {
	ticker := time.NewTicker(inboundReplayDeltaTickInterval)
	defer ticker.Stop()

	var probeCh <-chan time.Time
	if r.probe != nil {
		probeCh = r.probe.Ch()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-r.inbox:
			if !ok {
				return
			}
			r.handleMessage(msg)
		case <-ticker.C:
			r.maintenanceTick()
		case ts := <-probeCh:
			r.probe.RecordSample(ts)
		}
	}
}
```

- [ ] **Step 4: Run the new tests and confirm they pass**

```
just test-pkg './internal/consensus/adaptor/... -run "TestRouter_IOLatencyProbe_Drained|TestRouter_NoProbe_NoCrash"'
```

Expected: both PASS.

- [ ] **Step 5: Run all adaptor tests to confirm no regressions**

```
just test-pkg './internal/consensus/adaptor/...'
```

Expected: all PASS, including the existing `TestIOLatencyProbe_*` suite and the router_replay_delta and router_manifests test files (which use `NewRouter` without `SetIOLatencyProbe` — the nil-channel case must keep them green).

- [ ] **Step 6: Commit**

```bash
git add internal/consensus/adaptor/router.go internal/consensus/adaptor/router_io_latency_test.go
git commit -m "Drain IOLatencyProbe channel from consensus router select loop"
```

---

## Task 3: Own the probe in `Components`, start/stop with the lifecycle

The probe lives next to the router because that's the goroutine being measured. Standalone mode skips `NewFromConfig` entirely (the CLI returns `nil` for `consensusComponents`), so no probe is created — exactly what we want for the standalone path.

**Files:**
- Modify: `internal/consensus/adaptor/startup.go` (struct ~line 21–44, `Start` ~line 47–65, `Stop` ~line 68–91, `NewFromConfig` ~line 99–224)

- [ ] **Step 1: Write the failing test**

Append to `internal/consensus/adaptor/router_io_latency_test.go`:

```go
// TestComponents_IOLatencyProbe_StartedAndStopped verifies the probe is
// instantiated, started by Components.Start, and torn down by Stop.
func TestComponents_IOLatencyProbe_StartedAndStopped(t *testing.T) {
	probe := NewIOLatencyProbe(nil)
	components := &Components{
		IOLatency: probe,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe.Start(ctx, 10*time.Millisecond)

	// Confirm the probe ticker is running by reading one tick directly.
	select {
	case <-probe.Ch():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("probe ticker did not produce a tick after Start")
	}

	components.IOLatency.Stop()

	// After Stop, no new ticks should arrive within a short window.
	time.Sleep(50 * time.Millisecond)
	// Drain any buffered tick that landed before Stop.
	select {
	case <-probe.Ch():
	default:
	}
	select {
	case <-probe.Ch():
		t.Error("probe ticker still producing after Stop")
	case <-time.After(100 * time.Millisecond):
	}
}
```

- [ ] **Step 2: Run and confirm it fails**

```
just test-pkg './internal/consensus/adaptor/... -run TestComponents_IOLatencyProbe_StartedAndStopped'
```

Expected: FAIL with `unknown field IOLatency in struct literal of type Components`.

- [ ] **Step 3: Add `IOLatency` to `Components`**

In `internal/consensus/adaptor/startup.go`, in the `Components` struct (currently lines 21–44), add this field immediately after `Archive`:

```go
	// IOLatency measures scheduling latency of the Router goroutine and
	// is exposed to RPC via server_info.io_latency_ms. Nil only when
	// the consensus path is disabled (standalone mode), in which case
	// the RPC handler reports 0.
	IOLatency *IOLatencyProbe
```

- [ ] **Step 4: Construct the probe in `NewFromConfig` and wire it into the router**

In the same file, find the block immediately after `router := NewRouter(...)` and `router.SetManifestCache(...)` (around line 194–195). Insert:

```go
	// Wire the IO latency probe into the router goroutine so the select
	// loop measures its own scheduling delay. Mirrors rippled's
	// beast::io_latency_probe attached to the io_service thread.
	ioLatencyProbe := NewIOLatencyProbe(slog.Default().With("component", "io_latency"))
	router.SetIOLatencyProbe(ioLatencyProbe)
```

Then in the `return &Components{ ... }` literal at the end of the function, add:

```go
		IOLatency:   ioLatencyProbe,
```

immediately after `Archive: validationArchive,`.

- [ ] **Step 5: Start/stop the probe in `Components.Start` / `Stop`**

In `Components.Start` (currently lines 47–65), insert the probe start *before* `go c.Router.Run(routerCtx)` so the probe channel is non-empty when the router begins selecting:

```go
	// Start the IO latency probe ticker. Safe to call before Router.Run
	// — the probe channel buffers one tick, and Router.Run will drain it
	// the moment it begins selecting.
	if c.IOLatency != nil {
		c.IOLatency.Start(routerCtx, DefaultProbePeriod)
	}
```

In `Components.Stop` (currently lines 68–91), add this immediately before the `c.routerCancel()` call:

```go
	if c.IOLatency != nil {
		c.IOLatency.Stop()
	}
```

- [ ] **Step 6: Run the new test and confirm it passes**

```
just test-pkg './internal/consensus/adaptor/... -run TestComponents_IOLatencyProbe_StartedAndStopped'
```

Expected: PASS.

- [ ] **Step 7: Run all adaptor tests**

```
just test-pkg './internal/consensus/adaptor/...'
```

Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/consensus/adaptor/startup.go internal/consensus/adaptor/router_io_latency_test.go
git commit -m "Own IOLatencyProbe in Components; start/stop with lifecycle"
```

---

## Task 4: Wire `Components.IOLatency` into the RPC `ServiceContainer`

This is the final connection. The CLI already plumbs other consensus-mode-only fields like `PeerCount` and `LastCloseInfo` in the same block — we extend the same pattern.

**Files:**
- Modify: `internal/cli/server.go` (around line 262–273)

- [ ] **Step 1: Read the existing wiring block**

Open `internal/cli/server.go` and locate the block that currently reads:

```go
		// Expose node identity, peer count, and consensus stats to RPC handlers
		types.Services.NodePublicKey = consensusComponents.Overlay.Identity().EncodedPublicKey()
		types.Services.PeerCount = consensusComponents.Overlay.PeerCount
		engine := consensusComponents.Engine
		types.Services.LastCloseInfo = func() (int, int) {
			proposers, convergeTime := engine.GetLastCloseInfo()
			return proposers, int(convergeTime.Milliseconds())
		}
```

- [ ] **Step 2: Add the `IOLatencyMs` line**

Immediately after the `LastCloseInfo` assignment (and before the `Manifests` block), add:

```go
		types.Services.IOLatencyMs = consensusComponents.IOLatency.LatencyMs
```

The method-value `consensusComponents.IOLatency.LatencyMs` has type `func() int`, matching the `ServiceContainer` field exactly.

- [ ] **Step 3: Build to confirm the wiring compiles**

```
just build
```

Expected: build succeeds, binary at `../tmp/main`.

- [ ] **Step 4: Run the full test suite for affected packages**

```
just test-pkg './internal/cli/...'
just test-pkg './internal/rpc/handlers/...'
just test-pkg './internal/consensus/adaptor/...'
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/server.go
git commit -m "Plumb Components.IOLatency.LatencyMs into RPC ServiceContainer"
```

---

## Task 5: Verification — vet, lint, and end-to-end check

- [ ] **Step 1: Run `go vet`**

```
just vet
```

Expected: no warnings.

- [ ] **Step 2: Run the linter**

```
just lint
```

Expected: clean. If the linter is missing, the recipe auto-installs it.

- [ ] **Step 3: Confirm the hardcoded 1 is gone**

```
grep -n "io_latency_ms" internal/rpc/handlers/server_info.go
```

Expected: a single line referencing `ioLatencyMs()`, no literal `1`, no `TODO: track real IO latency` comment.

- [ ] **Step 4: Confirm no stray references to the old TODO survive**

```
grep -rn "TODO.*track real IO latency\|TODO.*io_latency" --include="*.go"
```

Expected: no matches.

- [ ] **Step 5: Build a final binary and run the broader test sweep**

```
just build
just test-libs
just test-core
```

Expected: build succeeds; tests PASS modulo the pre-existing `internal/rpc` `TestTxMethodEdgeCases` parser failures noted at branch start (those are unrelated to this work and tracked elsewhere).

- [ ] **Step 6: Final commit if any cleanup landed**

If any incidental fixups appeared from vet/lint, commit them with a focused message. Otherwise, skip.

```bash
git status
# If clean, no commit needed.
```

---

## Self-Review Checklist

- **Spec coverage:** Issue #176 says "IO latency is hardcoded to 1ms, this needs to be implemented." Task 1 removes the hardcode; Tasks 2–4 wire the existing probe end-to-end. Covered.
- **No placeholders:** Every step has exact file paths, full code blocks, exact commands, expected output. No "add error handling" or "similar to above".
- **Type consistency:** `IOLatencyMs func() int` is used uniformly across `services.go` (declaration), `server_info.go` (call), and `cli/server.go` (assignment from `*IOLatencyProbe.LatencyMs`, which is a `func() int` method value). `*IOLatencyProbe` is used uniformly as the probe type.
- **Standalone path:** Probe is owned by `Components`, which is `nil` in standalone mode. The CLI block that sets `IOLatencyMs` lives inside `if !standalone {`, so `types.Services.IOLatencyMs` stays nil there. The handler treats nil as 0. Path covered.
- **Test isolation:** Task 1's tests mutate `types.Services.IOLatencyMs` and use `t.Cleanup` to restore. Other tests calling `setupTestServicesServerInfo` get a fresh `ServiceContainer`, so the field is nil for them — matching the pre-change `>= 0` assertion.
