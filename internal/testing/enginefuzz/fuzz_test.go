package enginefuzz

import (
	"bytes"
	"fmt"
	"testing"
)

// FuzzEngineInvariants applies generated transaction sequences through the
// engine and fails if any apply reports an invariant violation or inflates
// total XRP. Run it with, e.g.:
//
//	go test -run x -fuzz FuzzEngineInvariants ./internal/testing/enginefuzz/
//
// See the package doc and issue #682 for the rationale.
func FuzzEngineInvariants(f *testing.F) {
	for _, seed := range seedCorpus() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		run(t, data)
	})
}

// TestEngineInvariants_SeedCorpus runs the seed corpus deterministically so the
// harness is exercised by plain `go test` / CI without the -fuzz flag.
func TestEngineInvariants_SeedCorpus(t *testing.T) {
	for i, seed := range seedCorpus() {
		t.Run(fmt.Sprintf("seed-%d", i), func(t *testing.T) {
			run(t, seed)
		})
	}
}

// seedCorpus returns deterministic byte inputs that drive varied transaction
// sequences. They seed the fuzzer's corpus and back the smoke test above.
func seedCorpus() [][]byte {
	ramp := make([]byte, 256)
	for i := range ramp {
		ramp[i] = byte(i)
	}
	return [][]byte{
		{},
		bytes.Repeat([]byte{0x00}, 16),
		ramp,
		bytes.Repeat([]byte{0x01, 0x40, 0x9a, 0x7f, 0x10, 0x33}, 24),
		bytes.Repeat([]byte{0xff, 0x00, 0x80, 0x2a}, 32),
	}
}
