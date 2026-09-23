package service

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestValidationProgressesDuringTransactionPersistence(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)
	parent := svc.GetClosedLedger()
	blob, _ := startupPaymentBlob(t, "validation-persistence", 1)
	_, err = svc.AcceptConsensusResult(t.Context(), parent, [][]byte{blob}, nil, parent.CloseTime().Add(time.Second), true)
	require.NoError(t, err)
	svc.FlushPersists()
	closed := svc.GetClosedLedger()
	seq, hash := closed.Sequence(), closed.Hash()

	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	persisted := make(chan error, 1)
	go func() {
		persisted <- closed.StoreTransactionDirty(func([]shamap.FlushEntry) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("transaction persistence did not reach storage")
	}

	validated := make(chan struct{})
	go func() {
		svc.SetValidatedLedger(seq, hash)
		close(validated)
	}()
	select {
	case <-validated:
	case <-time.After(time.Second):
		t.Fatal("validation blocked behind transaction persistence")
	}
	require.Equal(t, hash, svc.GetValidatedLedger().Hash())
	require.True(t, closed.IsValidated())
	require.Equal(t, hash, svc.GetClosedLedger().Hash())
	require.False(t, svc.openLedgerMu.Snapshot().Held)

	admitted := make(chan error, 1)
	go func() {
		lockErr := svc.lockOpenLedgerIfRunning(openLedgerConsensus)
		if lockErr == nil {
			svc.openLedgerMu.Unlock()
		}
		admitted <- lockErr
	}()
	select {
	case err := <-admitted:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("consensus admission blocked behind transaction persistence")
	}
	unblock()
	select {
	case err := <-persisted:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("transaction persistence did not finish")
	}
}
