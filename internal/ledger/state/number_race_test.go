package state

import (
	"sync"
	"testing"
)

// TestNumberRounding_ConcurrentDeterministic is the -race regression guard for
// issue #740: the Number switchover flag and rounding mode must never be shared
// mutable package globals.
//
// Many goroutines perform amount arithmetic under different rounding modes at
// the same time, mirroring a live node where the apply goroutine and the
// concurrent RPC/path-find goroutines both run Number math. Two properties must
// hold:
//
//   - No data race. The switchover flag (atomic.Bool, written only by apply) and
//     the rounding mode (threaded explicitly, never shared) leave nothing to
//     race on. If the rounding mode regressed to a package global, the per-step
//     setround()/getround() of concurrent goroutines would be flagged by -race.
//   - Determinism. Each goroutine rounding the same expression under a fixed
//     mode must reproduce the sequential golden result. A shared global mode
//     would let an Upward goroutine clobber a Downward goroutine mid-computation,
//     producing a different amount — the root cause of the #724 non-deterministic
//     consensus forks.
//
// Run with `go test -race` (CI does) to exercise the race detector.
func TestNumberRounding_ConcurrentDeterministic(t *testing.T) {
	SetNumberSwitchover(true)

	a := NewIssuedAmountFromValue(7333333333333333, -16, "USD", "rIssuer") // ~0.7333333333333333
	b := NewIssuedAmountFromValue(3141592653589793, -15, "USD", "rIssuer") // ~3.141592653589793

	// Golden values, computed sequentially. The three multiply modes and the
	// two sqrt modes must actually differ, otherwise the test would pass even if
	// the mode were ignored entirely.
	wantNearest := a.MulRounded(b, false, RoundToNearest)
	wantUp := a.MulRounded(b, false, RoundUpward)
	wantDown := a.MulRounded(b, false, RoundDownward)
	wantSqrtDown := b.SqrtRounded(RoundDownward)
	wantSqrtUp := b.SqrtRounded(RoundUpward)

	if wantUp.Compare(wantDown) == 0 {
		t.Fatalf("test setup is not mode-sensitive: up == down (%s)", wantUp.Value())
	}
	if wantSqrtUp.Compare(wantSqrtDown) == 0 {
		t.Fatalf("test setup is not mode-sensitive: sqrt up == down (%s)", wantSqrtUp.Value())
	}

	const (
		goroutines = 16
		iterations = 200
	)

	var wg sync.WaitGroup

	// Writers: mirror the apply goroutine establishing the (constant per ledger)
	// switchover. Concurrent with the readers below, this would trip -race
	// against a plain bool global.
	for w := 0; w < goroutines; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				SetNumberSwitchover(true)
			}
		}()
	}

	// Readers: each independently rounds the same expressions and must agree
	// with the golden values bit-for-bit.
	errs := make(chan string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if a.MulRounded(b, false, RoundToNearest).Compare(wantNearest) != 0 {
					errs <- "mul to-nearest mismatch"
					return
				}
				if a.MulRounded(b, false, RoundUpward).Compare(wantUp) != 0 {
					errs <- "mul upward mismatch"
					return
				}
				if a.MulRounded(b, false, RoundDownward).Compare(wantDown) != 0 {
					errs <- "mul downward mismatch"
					return
				}
				if b.SqrtRounded(RoundDownward).Compare(wantSqrtDown) != 0 {
					errs <- "sqrt downward mismatch"
					return
				}
				if b.SqrtRounded(RoundUpward).Compare(wantSqrtUp) != 0 {
					errs <- "sqrt upward mismatch"
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Errorf("non-deterministic rounding under concurrency: %s", msg)
	}
}
