package service

import (
	"testing"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
)

// TestInstallAdoptedLedgerLocked_ReturnCanonical pins the
// divergence-prevention contract: the helper returns the canonical
// ledger for the seq AFTER the validated-precedence rule resolves —
// `adopted` when the install proceeded, the existing validated entry
// when the skip fired. Callers MUST source `s.closedLedger` from
// this return; otherwise s.closedLedger and s.ledgerHistory[seq] can
// drift to different hashes for the same seq, breaking GetLedger(seq)
// and engine path-construction.
//
// Found by review of #402 (LeJamon/go-xrpl).
func TestInstallAdoptedLedgerLocked_ReturnCanonical(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("no closed ledger after Start")
	}
	parentSeq := parent.Sequence()

	makeLedger := func(seq uint32, salt byte, validated bool) *ledger.Ledger {
		stateMap, err := svc.genesisLedger.StateMapSnapshot()
		if err != nil {
			t.Fatalf("snapshot state: %v", err)
		}
		txMap, err := svc.genesisLedger.TxMapSnapshot()
		if err != nil {
			t.Fatalf("snapshot tx: %v", err)
		}
		// Vary an arbitrary header field so two distinct ledgers at the
		// same seq hash differently. NewFromHeader → StateValidated;
		// NewOpenWithHeader → StateOpen (IsValidated()==false), the
		// synthetic shape needed to exercise the precedence skip.
		var parentHash, ledgerHash [32]byte
		parentHash[0] = salt
		ledgerHash[0] = salt
		ledgerHash[1] = byte(seq)
		h := header.LedgerHeader{
			LedgerIndex: seq,
			ParentHash:  parentHash,
			Hash:        ledgerHash,
		}
		if validated {
			return ledger.NewFromHeader(h, stateMap, txMap, drops.Fees{})
		}
		return ledger.NewOpenWithHeader(h, stateMap, txMap, drops.Fees{})
	}

	seq := parentSeq + 5

	// Case 1: empty slot. Install proceeds; return == adopted.
	adopted1 := makeLedger(seq, 0xAA, false)
	svc.mu.Lock()
	got := svc.installAdoptedLedgerLocked(seq, adopted1)
	svc.mu.Unlock()
	if got != adopted1 {
		t.Fatalf("empty-slot install: expected return == adopted, got different ledger")
	}
	if svc.ledgerHistory[seq] != adopted1 {
		t.Fatalf("empty-slot install: history not written")
	}

	// Case 2: re-install same hash (still non-validated). No skip; return == new adopted.
	adopted1Again := makeLedger(seq, 0xAA, false)
	svc.mu.Lock()
	got = svc.installAdoptedLedgerLocked(seq, adopted1Again)
	svc.mu.Unlock()
	if got != adopted1Again {
		t.Fatalf("same-hash re-install: expected return == new adopted")
	}

	// Case 3: pre-populate slot with a VALIDATED entry, then try to
	// install a DIFFERENT hash that is non-validated. Skip must fire,
	// return == existing validated entry, history must keep pointing
	// at the validated entry.
	validatedSeq := parentSeq + 6
	validated := makeLedger(validatedSeq, 0xBB, true)
	svc.mu.Lock()
	svc.ledgerHistory[validatedSeq] = validated
	svc.mu.Unlock()

	wrongAdopt := makeLedger(validatedSeq, 0xCC, false)
	if wrongAdopt.Hash() == validated.Hash() {
		t.Fatal("test setup: wrongAdopt collided with validated; vary salt")
	}

	svc.mu.Lock()
	got = svc.installAdoptedLedgerLocked(validatedSeq, wrongAdopt)
	historyEntry := svc.ledgerHistory[validatedSeq]
	svc.mu.Unlock()

	if got != validated {
		t.Errorf("validated-precedence skip: return must be the existing "+
			"validated entry (so caller's s.closedLedger stays canonical); "+
			"got %p, want %p", got, validated)
	}
	if historyEntry != validated {
		t.Errorf("validated-precedence skip: history must keep validated "+
			"entry; got %p, want %p", historyEntry, validated)
	}
	gotHash := got.Hash()
	validatedHash := validated.Hash()
	if gotHash != validatedHash {
		t.Errorf("validated-precedence skip: return hash %x != validated "+
			"hash %x — divergence between closedLedger and ledgerHistory[seq] "+
			"will follow", gotHash[:8], validatedHash[:8])
	}

	// Case 4: a NEW validated adopt at the same seq should override
	// (rippled validated-vs-validated rule allows latest wins; our
	// helper only blocks non-validated overwrites of validated entries).
	newValidated := makeLedger(validatedSeq, 0xDD, true)
	svc.mu.Lock()
	got = svc.installAdoptedLedgerLocked(validatedSeq, newValidated)
	svc.mu.Unlock()
	if got != newValidated {
		t.Errorf("validated-over-validated: expected new validated to be installed, return ≠ newValidated")
	}
}
