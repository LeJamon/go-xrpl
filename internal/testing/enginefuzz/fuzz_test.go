package enginefuzz

import "testing"

func addSeedCorpus(f *testing.F) {
	for _, seed := range seedCorpus() {
		f.Add(encodeTrace(seed.Trace))
	}
}

func FuzzEngineInvariants(f *testing.F) {
	addSeedCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		runTrace(t, decodeTrace(data))
	})
}

func TestEngineInvariantsSeedCorpus(t *testing.T) {
	seenKinds := make(map[txKind]int)
	appliedKinds := make(map[txKind]int)
	for _, seed := range seedCorpus() {
		t.Run(seed.Name, func(t *testing.T) {
			decoded := decodeTrace(encodeTrace(seed.Trace))
			report := runTrace(t, decoded)
			if report.InvariantChecks == 0 || report.TransactionCalls == 0 {
				t.Fatalf("seed did not reach transaction apply and invariants: %+v", report)
			}
			for kind, count := range report.Kinds {
				seenKinds[kind] += count
			}
			for kind, count := range report.Applied {
				appliedKinds[kind] += count
			}
		})
	}
	for kind := txKind(0); kind < numKinds; kind++ {
		if seenKinds[kind] == 0 {
			t.Errorf("seed corpus never generates %s", kind)
		}
		if appliedKinds[kind] == 0 {
			t.Errorf("seed corpus never applies %s", kind)
		}
	}
}
