package state

import (
	"sync"
	"testing"
)

// TestNumberRounding_ConcurrentDeterministic proves independent immutable
// contexts retain both their arithmetic regime and rounding mode concurrently.
func TestNumberRounding_ConcurrentDeterministic(t *testing.T) {
	a := NewIssuedAmountFromValue(7333333333333333, -16, "USD", "rIssuer") // ~0.7333333333333333
	b := NewIssuedAmountFromValue(3141592653589793, -15, "USD", "rIssuer") // ~3.141592653589793
	universal := NewNumberContext(MantissaScaleSmall, true)
	legacy := NewNumberContext(MantissaScaleSmall, false)

	// Golden values, computed sequentially. The three multiply modes and the
	// two sqrt modes must actually differ, otherwise the test would pass even if
	// the mode were ignored entirely.
	wantNearest := a.MulWithNumberContext(b, universal, false, RoundToNearest)
	wantUp := a.MulWithNumberContext(b, universal, false, RoundUpward)
	wantDown := a.MulWithNumberContext(b, universal, false, RoundDownward)
	wantSqrtDown := b.SqrtWithNumberContext(universal, RoundDownward)
	wantSqrtUp := b.SqrtWithNumberContext(universal, RoundUpward)
	wantLegacy := a.MulWithNumberContext(b, legacy, false, RoundToNearest)

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

	errs := make(chan string, goroutines*2)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				if a.MulWithNumberContext(b, universal, false, RoundToNearest).Compare(wantNearest) != 0 {
					errs <- "mul to-nearest mismatch"
					return
				}
				if a.MulWithNumberContext(b, universal, false, RoundUpward).Compare(wantUp) != 0 {
					errs <- "mul upward mismatch"
					return
				}
				if a.MulWithNumberContext(b, universal, false, RoundDownward).Compare(wantDown) != 0 {
					errs <- "mul downward mismatch"
					return
				}
				if b.SqrtWithNumberContext(universal, RoundDownward).Compare(wantSqrtDown) != 0 {
					errs <- "sqrt downward mismatch"
					return
				}
				if b.SqrtWithNumberContext(universal, RoundUpward).Compare(wantSqrtUp) != 0 {
					errs <- "sqrt upward mismatch"
					return
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				if a.MulWithNumberContext(b, legacy, false, RoundToNearest).Compare(wantLegacy) != 0 {
					errs <- "legacy mul mismatch"
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
