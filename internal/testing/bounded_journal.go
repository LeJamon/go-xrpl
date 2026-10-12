package jtx

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

const boundedJournalTestEnv = "GOXRPL_TEST_BOUNDED_JOURNAL"

const (
	boundedTestPoolBytes      = 1 << 30
	boundedTestWorkspaceBytes = 128 << 20
	boundedTestJournalBytes   = 256 << 20
	boundedTestDiskBytes      = 512 << 20
	boundedTestMaxMemory      = 16 << 20
)

func boundedJournalEnabled() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(boundedJournalTestEnv)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func enableBoundedJournal(t testing.TB, genesis *ledger.Ledger, family shamap.Family) *ledger.ExecutionStore {
	t.Helper()
	if !boundedJournalEnabled() {
		return nil
	}
	if family == nil {
		family = backend.NewMemory()
	}
	pool := membudget.New(boundedTestPoolBytes)
	workspace, err := pool.NewAccount("test-execution-workspace", boundedTestWorkspaceBytes, boundedTestWorkspaceBytes)
	if err != nil {
		t.Fatalf("create bounded execution workspace budget: %v", err)
	}
	journalMemory, err := pool.NewAccount("test-execution-journal", 0, boundedTestJournalBytes)
	if err != nil {
		t.Fatalf("create bounded execution journal budget: %v", err)
	}
	journalDisk, err := pool.NewAccount("test-execution-spill", 0, boundedTestDiskBytes)
	if err != nil {
		t.Fatalf("create bounded execution spill budget: %v", err)
	}
	store, err := ledger.NewExecutionStore(ledger.ExecutionConfig{
		Family:    family,
		Workspace: workspace,
		Overlay: overlay.Config{
			MemoryBudget:   journalMemory,
			DiskBudget:     journalDisk,
			MaxMemoryBytes: boundedTestMaxMemory,
			MaxDiskBytes:   boundedTestDiskBytes,
			SpillDir:       t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("create bounded execution store: %v", err)
	}
	if err := genesis.EnableBoundedExecution(context.Background(), store); err != nil {
		_ = store.Close()
		t.Fatalf("enable bounded execution: %v", err)
	}
	return store
}
