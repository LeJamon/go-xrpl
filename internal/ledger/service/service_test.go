package service

import (
	"context"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
)

func TestNewService(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	if svc == nil {
		t.Fatal("Service should not be nil")
	}

	if !svc.IsStandalone() {
		t.Error("Service should be in standalone mode by default")
	}
}

func TestServiceStart(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	if err := svc.Start(); err != nil {
		t.Fatalf("Failed to start service: %v", err)
	}

	// Match rippled: genesis=1, LCL=2 (immediately closed), open=3
	if svc.GetValidatedLedgerIndex() != genesis.GenesisLedgerSequence+1 {
		t.Errorf("Validated ledger should be seq %d, got %d",
			genesis.GenesisLedgerSequence+1, svc.GetValidatedLedgerIndex())
	}

	// Open ledger should be seq 3
	if svc.GetCurrentLedgerIndex() != genesis.GenesisLedgerSequence+2 {
		t.Errorf("Open ledger should be seq %d, got %d",
			genesis.GenesisLedgerSequence+2, svc.GetCurrentLedgerIndex())
	}

	// Closed ledger should be seq 2
	if svc.GetClosedLedgerIndex() != genesis.GenesisLedgerSequence+1 {
		t.Errorf("Closed ledger should be seq %d, got %d",
			genesis.GenesisLedgerSequence+1, svc.GetClosedLedgerIndex())
	}

	t.Logf("Genesis ledger seq: %d", svc.genesisLedger.Sequence())
	t.Logf("Open ledger seq: %d", svc.GetCurrentLedgerIndex())
}

func TestAcceptLedger(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	if err := svc.Start(); err != nil {
		t.Fatalf("Failed to start service: %v", err)
	}

	initialOpenSeq := svc.GetCurrentLedgerIndex()

	// Accept the ledger (like ledger_accept RPC)
	closedSeq, err := svc.AcceptLedger(context.TODO())
	if err != nil {
		t.Fatalf("Failed to accept ledger: %v", err)
	}

	// The closed sequence should be the previous open sequence
	if closedSeq != initialOpenSeq {
		t.Errorf("Closed sequence should be %d, got %d", initialOpenSeq, closedSeq)
	}

	// New open ledger should be seq + 1
	newOpenSeq := svc.GetCurrentLedgerIndex()
	if newOpenSeq != initialOpenSeq+1 {
		t.Errorf("New open ledger should be seq %d, got %d", initialOpenSeq+1, newOpenSeq)
	}

	// Closed ledger should now be the one we just closed
	if svc.GetClosedLedgerIndex() != closedSeq {
		t.Errorf("Closed ledger index mismatch")
	}

	// Validated ledger should also be updated (standalone mode)
	if svc.GetValidatedLedgerIndex() != closedSeq {
		t.Errorf("Validated ledger index should match closed ledger in standalone mode")
	}

	t.Logf("Accepted ledger %d, new open ledger %d", closedSeq, newOpenSeq)
}

func TestAcceptMultipleLedgers(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	if err := svc.Start(); err != nil {
		t.Fatalf("Failed to start service: %v", err)
	}

	// Accept 5 ledgers
	for i := 0; i < 5; i++ {
		closedSeq, err := svc.AcceptLedger(context.TODO())
		if err != nil {
			t.Fatalf("Failed to accept ledger %d: %v", i, err)
		}

		// Each closed ledger should be sequential
		// Open starts at 3 (genesis=1, LCL=2), so first close is 3
		expectedSeq := uint32(3 + i)
		if closedSeq != expectedSeq {
			t.Errorf("Closed sequence should be %d, got %d", expectedSeq, closedSeq)
		}
	}

	// Final state check: after closing 3,4,5,6,7, open should be 8
	finalOpen := svc.GetCurrentLedgerIndex()
	if finalOpen != 8 {
		t.Errorf("Final open ledger should be 8, got %d", finalOpen)
	}

	// Validated should be 7
	if svc.GetValidatedLedgerIndex() != 7 {
		t.Errorf("Final validated ledger should be 7, got %d", svc.GetValidatedLedgerIndex())
	}

	t.Logf("After 5 accepts: open=%d, validated=%d",
		svc.GetCurrentLedgerIndex(), svc.GetValidatedLedgerIndex())
}

func TestGetLedgerBySequence(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	if err := svc.Start(); err != nil {
		t.Fatalf("Failed to start service: %v", err)
	}

	// Accept a ledger to create more history
	_, err = svc.AcceptLedger(context.TODO())
	if err != nil {
		t.Fatalf("Failed to accept ledger: %v", err)
	}

	// Get genesis ledger
	genesis, err := svc.GetLedgerBySequence(1)
	if err != nil {
		t.Fatalf("Failed to get genesis ledger: %v", err)
	}

	if genesis.Sequence() != 1 {
		t.Errorf("Genesis sequence should be 1, got %d", genesis.Sequence())
	}

	// Get ledger 2
	ledger2, err := svc.GetLedgerBySequence(2)
	if err != nil {
		t.Fatalf("Failed to get ledger 2: %v", err)
	}

	if ledger2.Sequence() != 2 {
		t.Errorf("Ledger 2 sequence should be 2, got %d", ledger2.Sequence())
	}

	// Verify parent hash
	if ledger2.ParentHash() != genesis.Hash() {
		t.Error("Ledger 2's parent hash should match genesis hash")
	}

	// Try to get non-existent ledger
	_, err = svc.GetLedgerBySequence(999)
	if err != ErrLedgerNotFound {
		t.Errorf("Expected ErrLedgerNotFound, got %v", err)
	}
}

func TestGetServerInfo(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	if err := svc.Start(); err != nil {
		t.Fatalf("Failed to start service: %v", err)
	}

	info := svc.GetServerInfo()

	if !info.Standalone {
		t.Error("Server info should show standalone mode")
	}

	if info.OpenLedgerSeq != 3 {
		t.Errorf("Open ledger seq should be 3, got %d", info.OpenLedgerSeq)
	}

	if info.ClosedLedgerSeq != 2 {
		t.Errorf("Closed ledger seq should be 2, got %d", info.ClosedLedgerSeq)
	}

	if info.ValidatedLedgerSeq != 2 {
		t.Errorf("Validated ledger seq should be 2, got %d", info.ValidatedLedgerSeq)
	}

	t.Logf("Server info: %+v", info)
}

func TestNotStandaloneError(t *testing.T) {
	cfg := Config{
		Standalone:    false, // Not standalone
		GenesisConfig: genesis.DefaultConfig(),
	}

	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	if err := svc.Start(); err != nil {
		t.Fatalf("Failed to start service: %v", err)
	}

	// AcceptLedger should fail when not in standalone mode
	_, err = svc.AcceptLedger(context.TODO())
	if err != ErrNotStandalone {
		t.Errorf("Expected ErrNotStandalone, got %v", err)
	}
}

func TestGetGenesisAccount(t *testing.T) {
	cfg := DefaultConfig()
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	address, err := svc.GetGenesisAccount()
	if err != nil {
		t.Fatalf("Failed to get genesis account: %v", err)
	}

	expectedAddress := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	if address != expectedAddress {
		t.Errorf("Genesis address should be %s, got %s", expectedAddress, address)
	}

	t.Logf("Genesis account: %s", address)
}
