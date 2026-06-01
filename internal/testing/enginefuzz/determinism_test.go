package enginefuzz

import (
	"fmt"
	"testing"
)

// ledgerHashes captures the fork-critical hashes of a closed ledger.
type ledgerHashes struct {
	ledger [32]byte // overall ledger hash
	state  [32]byte // state map hash (account_hash)
	txs    [32]byte // transaction map hash
}

// buildAndClose applies the transaction sequence encoded by data to a fresh
// seeded ledger, closes it once, and returns the closed ledger's hashes. The
// generator is driven purely by the byte stream against an identically seeded
// env, so two calls with the same data apply a byte-identical transaction set;
// only engine nondeterminism can make their outputs differ. All transactions
// land in a single ledger (no mid-stream closes) to maximise the tie-break
// opportunities a fork bug would surface on.
func buildAndClose(t testing.TB, data []byte) ledgerHashes {
	t.Helper()
	sc := newScenario(t)
	s := &stream{data: data}
	for i := 0; i < maxSteps && !s.drained(); i++ {
		sc.step(s)
	}
	sc.env.Close()

	lcl := sc.env.LastClosedLedger()
	state, err := lcl.StateMapHash()
	if err != nil {
		t.Fatalf("StateMapHash: %v", err)
	}
	txs, err := lcl.TxMapHash()
	if err != nil {
		t.Fatalf("TxMapHash: %v", err)
	}
	return ledgerHashes{ledger: lcl.Hash(), state: state, txs: txs}
}

// runDeterminism is the determinism/fork property: closing a ledger built from
// the same transaction set must be reproducible. Two independent runs use fresh
// envs -- hence independently randomized Go-map iteration orders -- so a state
// (account_hash) or transaction-metadata ordering that leaks map iteration order
// into the result will diverge here. A mismatch is a fork bug: the class of
// non-deterministic map tie-break that yielded issues #612 and #678.
func runDeterminism(t testing.TB, data []byte) {
	t.Helper()
	a := buildAndClose(t, data)
	b := buildAndClose(t, data)
	if a != b {
		t.Fatalf("non-deterministic ledger build:\n run1: ledger=%x state=%x txs=%x\n run2: ledger=%x state=%x txs=%x",
			a.ledger, a.state, a.txs, b.ledger, b.state, b.txs)
	}
}

// FuzzEngineDeterminism explores transaction sets and fails if ledger close+build
// is not reproducible across two independent runs. See issue #682 (scope 3).
func FuzzEngineDeterminism(f *testing.F) {
	for _, seed := range seedCorpus() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		runDeterminism(t, data)
	})
}

// TestEngineDeterminism_SeedCorpus runs the determinism property over the seed
// corpus deterministically so it is exercised by plain `go test` / CI.
func TestEngineDeterminism_SeedCorpus(t *testing.T) {
	for i, seed := range seedCorpus() {
		t.Run(fmt.Sprintf("seed-%d", i), func(t *testing.T) {
			runDeterminism(t, seed)
		})
	}
}
