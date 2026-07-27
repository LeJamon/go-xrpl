package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServerInfoReportsNeedForNetworkLedger(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standalone = false
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	require.True(t, svc.GetServerInfo().NeedsNetworkLedger)

	header, stateMap, txMap := acquiredLedgerFixture(t, 100, 0xb1)
	initialCandidate, err := svc.BootstrapLedgerWithState(t.Context(), header, stateMap, txMap)
	require.NoError(t, err)
	require.True(t, initialCandidate)
	require.True(t, svc.GetServerInfo().NeedsNetworkLedger)
	stored, err := svc.GetLedgerByHash(header.Hash)
	require.NoError(t, err)
	require.NoError(t, svc.SwitchToPreferredLedger(stored))
	require.False(t, svc.GetServerInfo().NeedsNetworkLedger)
}
