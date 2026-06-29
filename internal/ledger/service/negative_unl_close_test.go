package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
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
	cfg := service.DefaultConfig()
	cfg.Standalone = false
	svc, err := service.New(cfg)
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
	validator := negUNLValidator()
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

	assertFlagLedgerTransitioned(t, svc, validator)
}
