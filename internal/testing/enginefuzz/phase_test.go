package enginefuzz

import "testing"

func addPhaseSeedCorpus(f *testing.F) {
	for _, seed := range phaseSeedCorpus() {
		f.Add(encodePhaseTrace(seed.Trace))
	}
}

func FuzzEngineCommonFields(f *testing.F) {
	addPhaseSeedCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		runPhaseTrace(t, decodePhaseTrace(data))
	})
}

func TestEngineCommonFieldsSeedCorpus(t *testing.T) {
	for _, seed := range phaseSeedCorpus() {
		t.Run(seed.Name, func(t *testing.T) {
			report := runPhaseTrace(t, decodePhaseTrace(encodePhaseTrace(seed.Trace)))
			for kind := phaseKind(0); kind < numPhaseKinds; kind++ {
				if report.Kinds[kind] == 0 {
					t.Errorf("seed corpus never generates %s", kind)
				}
			}
			if report.Applied == 0 || report.Rejected == 0 || report.TransactionCalls == 0 || report.InvariantChecks == 0 {
				t.Fatalf("phase seed lacks applied/rejected/handler/invariant coverage: %+v", report)
			}
		})
	}
}
