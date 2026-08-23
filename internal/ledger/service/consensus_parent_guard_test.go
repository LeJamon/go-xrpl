package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
)

func newConsensusParentGuardService(t *testing.T) *Service {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Standalone = false
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(svc.Stop)
	return svc
}

func closeConsensusParentGuardLedger(t *testing.T, svc *Service, closeTime time.Time) {
	t.Helper()
	parent := svc.GetClosedLedger()
	if parent == nil {
		t.Fatal("closed ledger is nil")
	}
	if _, err := svc.AcceptConsensusResult(context.Background(), parent, nil, nil, closeTime, true); err != nil {
		t.Fatalf("AcceptConsensusResult: %v", err)
	}
}

func TestAcceptConsensusResultRejectsStaleParentWithoutMutation(t *testing.T) {
	svc := newConsensusParentGuardService(t)
	staleParent := svc.GetClosedLedger()
	closeTime := time.Unix(1_700_000_000, 0)
	for range 3 {
		closeTime = closeTime.Add(2 * time.Second)
		closeConsensusParentGuardLedger(t, svc, closeTime)
	}

	closedBefore := svc.GetClosedLedger()
	openBefore := svc.GetOpenLedger()
	_, err := svc.AcceptConsensusResult(context.Background(), staleParent, nil, nil, closeTime.Add(2*time.Second), true)
	if !errors.Is(err, ErrConsensusParentMismatch) {
		t.Fatalf("AcceptConsensusResult error = %v, want %v", err, ErrConsensusParentMismatch)
	}
	if got := svc.GetClosedLedger(); got != closedBefore {
		t.Fatalf("closed ledger mutated: got %d/%x, want %d/%x", got.Sequence(), got.Hash(), closedBefore.Sequence(), closedBefore.Hash())
	}
	if got := svc.GetOpenLedger(); got != openBefore {
		t.Fatalf("open ledger mutated: got %p, want %p", got, openBefore)
	}
}

func TestPreferredChainSwitchMovesFrontierBeforeConsensusBuild(t *testing.T) {
	svc := newConsensusParentGuardService(t)
	preferredParent := svc.GetClosedLedger()
	closeTime := time.Unix(1_700_000_000, 0)
	for range 2 {
		closeTime = closeTime.Add(2 * time.Second)
		closeConsensusParentGuardLedger(t, svc, closeTime)
	}
	staleSeq := preferredParent.Sequence() + historyWindow + 10
	staleToken := svc.beginValidatedPersistence(staleSeq, [32]byte{0xEE})
	svc.completeMu.Lock()
	svc.completedLedgers.addRange(
		preferredParent.Sequence()+1,
		staleSeq,
	)
	svc.completeMu.Unlock()

	if err := svc.SwitchToPreferredLedger(preferredParent); err != nil {
		t.Fatalf("SwitchToPreferredLedger: %v", err)
	}
	if got := svc.GetClosedLedger(); got != preferredParent {
		t.Fatalf("closed ledger = %d/%x, want preferred %d/%x", got.Sequence(), got.Hash(), preferredParent.Sequence(), preferredParent.Hash())
	}
	if got, want := svc.GetCurrentLedgerIndex(), preferredParent.Sequence()+1; got != want {
		t.Fatalf("open ledger seq = %d, want %d", got, want)
	}
	if _, err := svc.AdoptedLedgerBySequence(preferredParent.Sequence() + 2); !errors.Is(err, svcerr.ErrLedgerNotFound) {
		t.Fatalf("abandoned history tail remains after switch: %v", err)
	}
	if got := svc.GetServerInfo().CompleteLedgers; got != "empty" {
		t.Fatalf("abandoned complete-ledger tail remains after switch: %q", got)
	}
	svc.recordValidatedPersistence(staleSeq, staleToken, true)
	if got := svc.GetServerInfo().CompleteLedgers; got != "empty" {
		t.Fatalf("stale deep-tail persistence restored complete-ledger state: %q", got)
	}

	seq, err := svc.AcceptConsensusResult(
		context.Background(), preferredParent, nil, nil, closeTime.Add(2*time.Second), true,
	)
	if err != nil {
		t.Fatalf("AcceptConsensusResult: %v", err)
	}
	if want := preferredParent.Sequence() + 1; seq != want {
		t.Fatalf("closed seq = %d, want %d", seq, want)
	}
	closedAfterSwitch := svc.GetClosedLedger()
	if closedAfterSwitch.ParentHash() != preferredParent.Hash() {
		t.Fatalf("closed parent = %x, want %x", closedAfterSwitch.ParentHash(), preferredParent.Hash())
	}
}

func TestPreferredChainSwitchRejectsInvalidParentWithoutMutation(t *testing.T) {
	svc := newConsensusParentGuardService(t)
	closedBefore := svc.GetClosedLedger()
	openBefore := svc.GetOpenLedger()

	err := svc.SwitchToPreferredLedger(nil)
	if !errors.Is(err, ErrPreferredChainSwitch) {
		t.Fatalf("SwitchToPreferredLedger error = %v, want %v", err, ErrPreferredChainSwitch)
	}
	if got := svc.GetClosedLedger(); got != closedBefore {
		t.Fatalf("closed ledger mutated: got %d/%x, want %d/%x", got.Sequence(), got.Hash(), closedBefore.Sequence(), closedBefore.Hash())
	}
	if got := svc.GetOpenLedger(); got != openBefore {
		t.Fatalf("open ledger mutated: got %p, want %p", got, openBefore)
	}
}
