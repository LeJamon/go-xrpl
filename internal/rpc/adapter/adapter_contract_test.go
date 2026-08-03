package adapter

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/stretchr/testify/require"
)

func TestAdapterLedgerSelectionPropagatesStorageErrors(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	adapter := NewLedgerServiceAdapter(svc)
	_, err = adapter.GetLedgerBySequence(999)
	require.Error(t, err)
	_, err = adapter.GetLedgerByHash([32]byte{1})
	require.Error(t, err)
	_, err = adapter.GetLedgerByHashContext(context.Background(), [32]byte{2})
	require.Error(t, err)
}
