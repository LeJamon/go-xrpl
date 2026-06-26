package service

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
)

// TestAcceptConsensusResult_FlagLedgerAppliesNegativeUNL is the regression test
// for issue #1091: the live consensus close path must apply the flag-ledger
// NegativeUNL transition (move sfValidatorToDisable into sfDisabledValidators,
// clear the transition field) just like the catchup/replay path, or a
// locally-built flag ledger computes a different account_hash than the rest of
// the network and forks.
//
// The empty-consensus-tx-set close is the gap: AcceptConsensusResult skips
// buildClosedLedgerLocked when the agreed tx set is empty, yet rippled applies
// the transition on every flag ledger (seq % 256 == 0) regardless of whether it
// carries transactions. This drives a flag-ledger close through that path with
// a pending ValidatorToDisable seeded on the parent and asserts the transition
// materialised.
func TestAcceptConsensusResult_FlagLedgerAppliesNegativeUNL(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standalone = false
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	closeTime := time.Unix(1700000000, 0)

	// Build a chain from genesis up to seq 254 so the next ledger (256, built
	// below) is the first flag ledger.
	parent := svc.GetClosedLedger()
	for parent.Sequence() < 254 {
		closeTime = closeTime.Add(2 * time.Second)
		open, err := ledger.NewOpen(parent, closeTime)
		if err != nil {
			t.Fatalf("NewOpen %d: %v", parent.Sequence()+1, err)
		}
		if err := open.Close(closeTime, 0); err != nil {
			t.Fatalf("Close %d: %v", open.Sequence(), err)
		}
		parent = open
	}

	// Hand-build seq 255 carrying a pending ValidatorToDisable, as a UNLModify
	// pseudo-tx on the prior flag ledger would have left it.
	validator := make([]byte, 33)
	validator[0] = 0xED
	for i := 1; i < 33; i++ {
		validator[i] = 0x42
	}
	seed, err := pseudo.SerializeNegativeUNLSLE(&pseudo.NegativeUNLSLE{
		ValidatorToDisable: validator,
	})
	if err != nil {
		t.Fatalf("serialize NegativeUNL seed: %v", err)
	}

	closeTime = closeTime.Add(2 * time.Second)
	open255, err := ledger.NewOpen(parent, closeTime)
	if err != nil {
		t.Fatalf("NewOpen 255: %v", err)
	}
	if err := open255.Insert(keylet.NegativeUNL(), seed); err != nil {
		t.Fatalf("insert NegativeUNL SLE: %v", err)
	}
	if err := open255.Close(closeTime, 0); err != nil {
		t.Fatalf("Close 255: %v", err)
	}
	if open255.Sequence() != 255 {
		t.Fatalf("hand-built parent seq = %d, want 255", open255.Sequence())
	}

	// Build the flag ledger (seq 256) through the consensus path with an EMPTY
	// tx set — the sub-path that skipped the transition before this fix.
	closeTime = closeTime.Add(2 * time.Second)
	seq, err := svc.AcceptConsensusResult(context.TODO(), open255, nil, closeTime, true)
	if err != nil {
		t.Fatalf("flag-ledger consensus close: %v", err)
	}
	if seq != 256 {
		t.Fatalf("flag-ledger seq = %d, want 256", seq)
	}

	closed := svc.GetClosedLedger()
	raw, err := closed.Read(keylet.NegativeUNL())
	if err != nil {
		t.Fatalf("read NegativeUNL SLE from flag ledger: %v", err)
	}
	if raw == nil {
		t.Fatal("NegativeUNL SLE missing from flag ledger")
	}
	sle, err := pseudo.ParseNegativeUNLSLE(raw)
	if err != nil {
		t.Fatalf("parse NegativeUNL SLE: %v", err)
	}

	if len(sle.DisabledValidators) != 1 {
		t.Fatalf("DisabledValidators len = %d, want 1 — flag-ledger transition not applied (issue #1091)", len(sle.DisabledValidators))
	}
	dv := sle.DisabledValidators[0]
	if !bytes.Equal(dv.PublicKey, validator) {
		t.Errorf("DisabledValidators[0].PublicKey = %x, want %x", dv.PublicKey, validator)
	}
	if dv.FirstLedgerSequence != 256 {
		t.Errorf("FirstLedgerSequence = %d, want 256", dv.FirstLedgerSequence)
	}
	if len(sle.ValidatorToDisable) != 0 {
		t.Errorf("ValidatorToDisable not cleared on flag ledger: %x", sle.ValidatorToDisable)
	}
}
