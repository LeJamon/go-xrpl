package service

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/stretchr/testify/require"
)

// closeStoredLedgerFixture turns a test-only raw ledger fixture into a real
// closed-ledger frontier before storage assertions inspect it. Raw fixtures
// may already contain serialized transaction metadata, so they must not be
// replayed through the transaction engine.
func closeStoredLedgerFixture(t testing.TB, svc *Service) *ledger.Ledger {
	return closeStoredLedgerFixtureWithValidation(t, svc, true)
}

func closeStoredLedgerFixtureWithValidation(t testing.TB, svc *Service, validate bool) *ledger.Ledger {
	t.Helper()

	// Capture the legacy fixture while ingress and frontier transitions are
	// excluded. SwitchToPreferredLedger below performs the real publication and
	// open-view rebuild after these locks are released.
	svc.openLedgerMu.Lock()
	svc.mu.RLock()
	raw := svc.openLedger
	var closed *ledger.Ledger
	var err error
	if raw != nil {
		closed, err = raw.MutableSnapshotUnflushed()
	}
	svc.mu.RUnlock()
	svc.openLedgerMu.Unlock()

	require.NoError(t, err)
	require.NotNil(t, closed)
	require.NoError(t, closed.Close(time.Now(), 0))
	if validate {
		require.NoError(t, closed.SetValidated())
	}
	require.NoError(t, svc.SwitchToPreferredLedger(closed))
	if validate {
		svc.SetValidatedLedger(closed.Sequence(), closed.Hash())
	}
	return closed
}

// modifyPublishedOpenLedger applies storage-only state mutations to the
// authoritative published candidate after a fixture has been adopted.
func modifyPublishedOpenLedger(t testing.TB, svc *Service, mutate func(*ledger.Ledger) error) {
	t.Helper()
	require.NotNil(t, svc.openLedgerView)
	var err error
	changed := svc.openLedgerView.Modify(func(view *ledger.Ledger) bool {
		err = mutate(view)
		return err == nil
	})
	require.NoError(t, err)
	require.True(t, changed)
}

func discardPersistedLedgerCache(t testing.TB, svc *Service, hash [32]byte) {
	t.Helper()
	svc.mu.Lock()
	delete(svc.persistedLedgers, hash)
	filtered := svc.persistedLedgerFIFO[:0]
	for _, cachedHash := range svc.persistedLedgerFIFO {
		if cachedHash != hash {
			filtered = append(filtered, cachedHash)
		}
	}
	svc.persistedLedgerFIFO = filtered
	svc.mu.Unlock()
}
