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

func TestAdapterOpenLedgerViewReturnsSnapshot(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	view, err := NewLedgerServiceAdapter(svc).GetOpenLedgerView()
	require.NoError(t, err)
	require.NotNil(t, view)
	require.NotNil(t, view.Rules())
	require.NotZero(t, view.LedgerSeq())
}

func TestAdapterContextLedgerReadHasIdempotentClose(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	adapter := NewLedgerServiceAdapter(svc)
	reader, err := adapter.GetLedgerBySequenceContext(context.Background(), closed.Sequence())
	require.NoError(t, err)
	closer, ok := reader.(interface{ Close() error })
	require.True(t, ok)
	require.NoError(t, closer.Close())
	require.NoError(t, closer.Close())

	view, metadata, err := adapter.GetLedgerViewBySeqContext(context.Background(), closed.Sequence())
	require.NoError(t, err)
	require.Same(t, view, metadata)
	viewCloser, ok := view.(interface{ Close() error })
	require.True(t, ok)
	require.NoError(t, viewCloser.Close())
	require.NoError(t, viewCloser.Close())
}
