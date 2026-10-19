package service

import (
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestServingLookupsRejectCanceledCachedReads(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	cached := makeStubLedger(t, 20, [32]byte{0x20}, [32]byte{0x19})
	svc.historyComponent.mu.Lock()
	svc.ledgerHistory[cached.Sequence()] = cached
	svc.ledgerByHash[cached.Hash()] = cached.Sequence()
	svc.persistedLedgers[cached.Hash()] = cached
	svc.historyComponent.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, lookup := range map[string]func(context.Context) error{
		"sequence": func(ctx context.Context) error {
			_, err := svc.GetLedgerBySequenceContext(ctx, cached.Sequence())
			return err
		},
		"hash": func(ctx context.Context) error {
			_, err := svc.GetLedgerByHashContext(ctx, cached.Hash())
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, lookup(ctx), context.Canceled)
		})
	}
}

func TestGetLedgerBySequenceContextPropagatesStorageCancellation(t *testing.T) {
	backendDB := newTestNodeStore(t, 10)
	t.Cleanup(func() { require.NoError(t, backendDB.Close()) })
	db := &fetchErrorDatabase{Database: backendDB, err: context.Canceled}
	svc, err := New(Config{
		Standalone:   true,
		NodeStore:    db,
		SHAMapFamily: backend.New(db),
	})
	require.NoError(t, err)

	const seq uint32 = 20
	hash := [32]byte{0x20}
	svc.completeMu.Lock()
	svc.completedLedgers.add(seq)
	svc.completeLedgerHashes[seq] = hash
	svc.completeMu.Unlock()

	_, err = svc.GetLedgerBySequenceContext(context.Background(), seq)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, errors.Is(err, svcerr.ErrLedgerNotFound))
}
