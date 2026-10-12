package service

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/stretchr/testify/require"
)

func TestGetCachedLedgerByHashIsMemoryOnlyAndNonblocking(t *testing.T) {
	var absent *Service
	l, ok := absent.GetCachedLedgerByHash([32]byte{1})
	require.Nil(t, l)
	require.False(t, ok)
	s := &Service{}
	// No storage dependencies are configured; only the memory indexes exist.
	held, queried := &ledger.Ledger{}, &ledger.Ledger{}
	s.ledgerHistory = map[uint32]*ledger.Ledger{10: held}
	s.ledgerByHash = map[[32]byte]uint32{{1}: 10}
	s.persistedLedgers = map[[32]byte]*ledger.Ledger{{2}: queried}
	l, ok = s.GetCachedLedgerByHash([32]byte{1})
	require.True(t, ok)
	require.Same(t, held, l)
	l, ok = s.GetCachedLedgerByHash([32]byte{2})
	require.True(t, ok)
	require.Same(t, queried, l)
	l, ok = s.GetCachedLedgerByHash([32]byte{3})
	require.False(t, ok)
	require.Nil(t, l)
	s.historyComponent.mu.Lock()
	// A concurrent retention writer must not stall a consensus scheduling probe.
	l, ok = s.GetCachedLedgerByHash([32]byte{1})
	s.historyComponent.mu.Unlock()
	require.False(t, ok)
	require.Nil(t, l)
	l, ok = s.GetCachedLedgerByHash([32]byte{1})
	require.True(t, ok)
	require.Same(t, held, l)
}
