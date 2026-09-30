package enginefuzz

import "testing"

type ledgerHashes struct {
	ledger [32]byte
	state  [32]byte
}

func buildAndClose(t testing.TB, tr trace) ledgerHashes {
	t.Helper()
	sc, report := executeTrace(t, tr)
	if len(tr.Steps) != 0 && (report.TransactionCalls == 0 || report.InvariantChecks == 0) {
		t.Fatalf("determinism trace did not reach transaction apply and invariants: %+v", report)
	}
	lcl := sc.env.LastClosedLedger()
	stateHash, err := lcl.StateMapHash()
	if err != nil {
		t.Fatalf("StateMapHash: %v", err)
	}
	return ledgerHashes{ledger: lcl.Hash(), state: stateHash}
}

func runDeterminism(t testing.TB, data []byte) {
	t.Helper()
	tr := decodeTrace(data)
	a := buildAndClose(t, tr)
	b := buildAndClose(t, tr)
	if a != b {
		t.Fatalf("non-deterministic ledger build: run1 ledger=%x state=%x; run2 ledger=%x state=%x", a.ledger, a.state, b.ledger, b.state)
	}
}

func FuzzEngineDeterminism(f *testing.F) {
	addSeedCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		runDeterminism(t, data)
	})
}

func TestEngineDeterminismSeedCorpus(t *testing.T) {
	for _, seed := range seedCorpus() {
		t.Run(seed.Name, func(t *testing.T) {
			runDeterminism(t, encodeTrace(seed.Trace))
		})
	}
}
