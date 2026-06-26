package service

import (
	"context"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
)

// applyConfigForTest exercises applyConfigLocked under the read lock its
// contract requires, failing the test on the only error it can return
// (no closed ledger).
func (s *Service) applyConfigForTest(t *testing.T) openledger.ApplyConfig {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, err := s.applyConfigLocked()
	if err != nil {
		t.Fatalf("applyConfigLocked: %v", err)
	}
	return cfg
}

// TestApplyConfigCache_HitWithinLedger pins the #1094 fix: while the closed
// ledger is unchanged, applyConfigLocked must reuse the same amendment
// Rules instead of re-reading the Amendments SLE and allocating a fresh
// rule set on every call (the per-transaction hot path that starved
// consensus under load).
func TestApplyConfigCache_HitWithinLedger(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Failed to start service: %v", err)
	}

	first := svc.applyConfigForTest(t)
	if first.Rules == nil {
		t.Fatal("expected non-nil Rules")
	}
	second := svc.applyConfigForTest(t)

	if first.Rules != second.Rules {
		t.Error("expected memoised Rules pointer within a ledger; a fresh allocation means the rule set was rebuilt per call")
	}
}

// TestApplyConfigCache_InvalidatedOnClose verifies the cache keys on the
// closed ledger: once it advances, applyConfigLocked rebuilds the config
// (fresh Rules pointer) and reports the new ledger sequence rather than
// serving a stale cached value.
func TestApplyConfigCache_InvalidatedOnClose(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Failed to start service: %v", err)
	}

	before := svc.applyConfigForTest(t)
	if _, err := svc.AcceptLedger(context.TODO()); err != nil {
		t.Fatalf("Failed to accept ledger: %v", err)
	}
	after := svc.applyConfigForTest(t)

	if before.Rules == after.Rules {
		t.Error("expected Rules to be rebuilt after the closed ledger advanced")
	}
	if after.LedgerSequence <= before.LedgerSequence {
		t.Errorf("expected LedgerSequence to advance after close: before=%d after=%d",
			before.LedgerSequence, after.LedgerSequence)
	}
}

// TestApplyConfigCache_ConcurrentReads exercises the cache under concurrent
// readers (the SubmitOpenLedgerTx ingress pattern: many goroutines holding
// only s.mu.RLock) so `go test -race` covers the dedicated cache mutex.
func TestApplyConfigCache_ConcurrentReads(t *testing.T) {
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Failed to start service: %v", err)
	}

	const (
		goroutines = 32
		iterations = 100
	)
	errCh := make(chan error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				svc.mu.RLock()
				_, e := svc.applyConfigLocked()
				svc.mu.RUnlock()
				if e != nil {
					errCh <- e
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	if e := <-errCh; e != nil {
		t.Fatalf("applyConfigLocked under concurrent reads: %v", e)
	}
}
