package service

import (
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/stretchr/testify/require"
)

func TestPreparedPreferredLedgerFinishCallbackCanStopService(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PersistenceMemory = &relayTestMemory{limit: 256 << 20}
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	parent := svc.GetClosedLedger()
	preferred, err := ledger.NewOpen(parent, time.Now())
	require.NoError(t, err)
	require.NoError(t, preferred.Close(time.Now(), 0))
	prepared, err := svc.PreparePreferredLedger(context.Background(), preferred)
	require.NoError(t, err)
	require.NoError(t, prepared.Wait(context.Background()))
	prepared.relay = []preferredRelay{{hash: [32]byte{1}, blob: []byte{1}}}
	callbackDone := make(chan struct{})
	prepared.relayFn = func([]byte) {
		svc.Stop()
		close(callbackDone)
	}
	published, err := prepared.TryPublish()
	require.NoError(t, err)
	require.True(t, published)
	finishDone := make(chan struct{})
	go func() {
		prepared.Finish()
		close(finishDone)
	}()
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("preferred-ledger callback deadlocked while stopping the service")
	}
	select {
	case <-finishDone:
	case <-time.After(time.Second):
		t.Fatal("preferred-ledger finish did not drain")
	}
	prepared.Release()
}
