package persistence_test

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/storage/kvstore/memorydb"
	"github.com/LeJamon/goXRPLd/storage/nodestore"
)

// newMemoryNodeStore creates an in-memory nodestore for testing.
func newMemoryNodeStore() nodestore.Database {
	store := memorydb.New()
	return nodestore.NewKVDatabase(store, "test-memory", 2000, time.Hour)
}

// newSharedMemoryStore returns the raw memorydb and a nodestore wrapping it.
// Multiple nodestore.Database instances can be created from the same memorydb
// to simulate service restarts.
func newSharedMemoryStore() (*memorydb.MemDatabase, nodestore.Database) {
	store := memorydb.New()
	db := nodestore.NewKVDatabase(store, "test-memory", 2000, time.Hour)
	return store, db
}

// newNodeStoreFromMemDB wraps an existing MemDatabase with a fresh nodestore.
func newNodeStoreFromMemDB(store *memorydb.MemDatabase) nodestore.Database {
	return nodestore.NewKVDatabase(store, "test-memory", 2000, time.Hour)
}

// TestStartupNormal_FreshDB verifies that StartupNormal with no existing
// state creates a genesis ledger at sequence 1.
func TestStartupNormal_FreshDB(t *testing.T) {
	db := newMemoryNodeStore()
	cfg := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupNormal,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
	}

	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := svc.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	if seq := svc.GetValidatedLedgerIndex(); seq != 1 {
		t.Errorf("expected validated ledger seq 1, got %d", seq)
	}

	if seq := svc.GetCurrentLedgerIndex(); seq != 2 {
		t.Errorf("expected open ledger seq 2, got %d", seq)
	}
}

// TestStartupNormal_ExistingState persists several ledgers, then creates a
// new service with StartupNormal and verifies it resumes from the latest.
func TestStartupNormal_ExistingState(t *testing.T) {
	memStore, db := newSharedMemoryStore()

	// Phase 1: create and advance ledgers
	cfg := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupFresh,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
	}
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Accept 3 ledgers (closes ledgers 2, 3, 4)
	for i := 0; i < 3; i++ {
		if _, err := svc.AcceptLedger(); err != nil {
			t.Fatalf("AcceptLedger() %d failed: %v", i, err)
		}
	}

	lastValidatedSeq := svc.GetValidatedLedgerIndex()
	lastValidatedHash := svc.GetValidatedLedger().Hash()
	if lastValidatedSeq != 4 {
		t.Fatalf("expected validated seq 4, got %d", lastValidatedSeq)
	}

	// Phase 2: create a new service from the same store
	db2 := newNodeStoreFromMemDB(memStore)
	cfg2 := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupNormal,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db2,
	}
	svc2, err := service.New(cfg2)
	if err != nil {
		t.Fatalf("New() for recovery failed: %v", err)
	}
	if err := svc2.Start(); err != nil {
		t.Fatalf("Start() for recovery failed: %v", err)
	}

	// Verify recovery
	if seq := svc2.GetValidatedLedgerIndex(); seq != lastValidatedSeq {
		t.Errorf("expected recovered validated seq %d, got %d", lastValidatedSeq, seq)
	}
	if hash := svc2.GetValidatedLedger().Hash(); hash != lastValidatedHash {
		t.Errorf("expected recovered validated hash %x, got %x", lastValidatedHash[:8], hash[:8])
	}
	// Open ledger should be lastValidatedSeq+1
	if seq := svc2.GetCurrentLedgerIndex(); seq != lastValidatedSeq+1 {
		t.Errorf("expected open ledger seq %d, got %d", lastValidatedSeq+1, seq)
	}
}

// TestStartupFresh_ExistingState persists ledgers, then creates a new service
// with StartupFresh and verifies it resets to genesis (seq 1).
func TestStartupFresh_ExistingState(t *testing.T) {
	memStore, db := newSharedMemoryStore()

	// Phase 1: create and advance ledgers
	cfg := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupFresh,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
	}
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := svc.AcceptLedger(); err != nil {
			t.Fatalf("AcceptLedger() %d failed: %v", i, err)
		}
	}

	// Phase 2: create a new service with StartupFresh — should ignore stored state
	db2 := newNodeStoreFromMemDB(memStore)
	cfg2 := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupFresh,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db2,
	}
	svc2, err := service.New(cfg2)
	if err != nil {
		t.Fatalf("New() for fresh start failed: %v", err)
	}
	if err := svc2.Start(); err != nil {
		t.Fatalf("Start() for fresh start failed: %v", err)
	}

	if seq := svc2.GetValidatedLedgerIndex(); seq != 1 {
		t.Errorf("expected fresh genesis seq 1, got %d", seq)
	}
	if seq := svc2.GetCurrentLedgerIndex(); seq != 2 {
		t.Errorf("expected fresh open seq 2, got %d", seq)
	}
}

// TestStartupLoad_NoState verifies that StartupLoad with an empty DB returns
// an error.
func TestStartupLoad_NoState(t *testing.T) {
	db := newMemoryNodeStore()
	cfg := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupLoad,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
	}

	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	err = svc.Start()
	if err == nil {
		t.Fatal("expected error from StartupLoad with no persisted state")
	}
	t.Logf("StartupLoad error (expected): %v", err)
}

// TestStartupLoad_ExistingState persists ledgers, then creates a new service
// with StartupLoad and verifies it loads correctly.
func TestStartupLoad_ExistingState(t *testing.T) {
	memStore, db := newSharedMemoryStore()

	// Phase 1: create and advance ledgers
	cfg := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupFresh,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
	}
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := svc.AcceptLedger(); err != nil {
			t.Fatalf("AcceptLedger() %d failed: %v", i, err)
		}
	}

	lastValidatedSeq := svc.GetValidatedLedgerIndex()

	// Phase 2: create a new service with StartupLoad
	db2 := newNodeStoreFromMemDB(memStore)
	cfg2 := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupLoad,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db2,
	}
	svc2, err := service.New(cfg2)
	if err != nil {
		t.Fatalf("New() for load failed: %v", err)
	}
	if err := svc2.Start(); err != nil {
		t.Fatalf("Start() for load failed: %v", err)
	}

	if seq := svc2.GetValidatedLedgerIndex(); seq != lastValidatedSeq {
		t.Errorf("expected loaded validated seq %d, got %d", lastValidatedSeq, seq)
	}
}

// TestFeeRecovery verifies that fees are correctly restored from the loaded
// ledger's FeeSettings SLE.
func TestFeeRecovery(t *testing.T) {
	memStore, db := newSharedMemoryStore()

	// Phase 1: create service and advance
	cfg := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupFresh,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
	}
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Read fees from original service
	origBaseFee, origReserveBase, origReserveInc := svc.GetCurrentFees()
	if origBaseFee == 0 || origReserveBase == 0 || origReserveInc == 0 {
		t.Fatalf("original fees should be non-zero: baseFee=%d reserveBase=%d reserveInc=%d",
			origBaseFee, origReserveBase, origReserveInc)
	}

	// Accept a ledger to persist
	if _, err := svc.AcceptLedger(); err != nil {
		t.Fatalf("AcceptLedger() failed: %v", err)
	}

	// Phase 2: recover and check fees
	db2 := newNodeStoreFromMemDB(memStore)
	cfg2 := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupNormal,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db2,
	}
	svc2, err := service.New(cfg2)
	if err != nil {
		t.Fatalf("New() for recovery failed: %v", err)
	}
	if err := svc2.Start(); err != nil {
		t.Fatalf("Start() for recovery failed: %v", err)
	}

	// Fees should match (read dynamically from the FeeSettings SLE)
	baseFee, reserveBase, reserveInc := svc2.GetCurrentFees()
	if baseFee != origBaseFee {
		t.Errorf("baseFee: expected %d, got %d", origBaseFee, baseFee)
	}
	if reserveBase != origReserveBase {
		t.Errorf("reserveBase: expected %d, got %d", origReserveBase, reserveBase)
	}
	if reserveInc != origReserveInc {
		t.Errorf("reserveInc: expected %d, got %d", origReserveInc, reserveInc)
	}
}

// TestContinueAfterRecovery verifies that a recovered service can continue
// accepting new ledgers.
func TestContinueAfterRecovery(t *testing.T) {
	memStore, db := newSharedMemoryStore()

	// Phase 1: create and advance to ledger 4
	cfg := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupFresh,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db,
	}
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := svc.AcceptLedger(); err != nil {
			t.Fatalf("AcceptLedger() %d failed: %v", i, err)
		}
	}

	// Phase 2: recover and continue
	db2 := newNodeStoreFromMemDB(memStore)
	cfg2 := service.Config{
		Standalone:    true,
		StartupMode:   service.StartupNormal,
		GenesisConfig: genesis.DefaultConfig(),
		NodeStore:     db2,
	}
	svc2, err := service.New(cfg2)
	if err != nil {
		t.Fatalf("New() for recovery failed: %v", err)
	}
	if err := svc2.Start(); err != nil {
		t.Fatalf("Start() for recovery failed: %v", err)
	}

	// Accept 2 more ledgers (should produce ledger 5, 6)
	for i := 0; i < 2; i++ {
		closedSeq, err := svc2.AcceptLedger()
		if err != nil {
			t.Fatalf("AcceptLedger() after recovery %d failed: %v", i, err)
		}
		expectedSeq := uint32(5 + i)
		if closedSeq != expectedSeq {
			t.Errorf("expected closed seq %d, got %d", expectedSeq, closedSeq)
		}
	}

	if seq := svc2.GetValidatedLedgerIndex(); seq != 6 {
		t.Errorf("expected validated seq 6, got %d", seq)
	}
	if seq := svc2.GetCurrentLedgerIndex(); seq != 7 {
		t.Errorf("expected open seq 7, got %d", seq)
	}
}
