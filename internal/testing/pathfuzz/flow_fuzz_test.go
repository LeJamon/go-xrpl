package pathfuzz

import (
	"fmt"
	"testing"
)

// FuzzPaymentFlow applies generated cross-currency payment sequences through the
// engine and fails if any apply reports an invariant violation, inflates total
// XRP, exceeds the per-apply time budget, or violates a delivered-amount
// property (delivered <= requested, delivered <= same-currency SendMax, partial
// >= DeliverMin, non-partial success delivers in full). Run it with, e.g.:
//
//	go test -run x -fuzz FuzzPaymentFlow ./internal/testing/pathfuzz/
//
// See the package doc and issue #685.
func FuzzPaymentFlow(f *testing.F) {
	for _, seed := range seedCorpus() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		run(t, data)
	})
}

// TestPaymentFlow_SeedCorpus runs the seed corpus deterministically so the
// harness is exercised by plain `go test` / CI without the -fuzz flag.
func TestPaymentFlow_SeedCorpus(t *testing.T) {
	for i, seed := range seedCorpus() {
		t.Run(fmt.Sprintf("seed-%d", i), func(t *testing.T) {
			run(t, seed)
		})
	}
}
