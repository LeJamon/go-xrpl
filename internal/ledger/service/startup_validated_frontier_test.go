package service

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestFreshNonStandaloneStartsWithoutValidatedLedger(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standalone = false

	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	require.Equal(t, uint32(genesis.GenesisLedgerSequence+1), svc.GetClosedLedgerIndex())
	require.Equal(t, uint32(genesis.GenesisLedgerSequence+2), svc.GetCurrentLedgerIndex())
	require.Nil(t, svc.GetValidatedLedger())
	require.Zero(t, svc.GetValidatedLedgerIndex())
	require.True(t, svc.NeedsInitialSync())
	require.False(t, svc.GetServerInfo().HaveValidated)
	require.Zero(t, protocol.ToRippleTime(svc.GetClosedLedger().ParentCloseTime()))
}
