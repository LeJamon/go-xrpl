package service

import (
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestCompleteLedgers_EvictedSequenceLoadsFromNodeStore(t *testing.T) {
	db := newTestNodeStore(t, 10_000)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	cfg := DefaultConfig()
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	want := svc.GetValidatedLedger()
	require.NotNil(t, want)
	require.NoError(t, svc.persistValidatedLedger(t.Context(), want, false))

	svc.mu.Lock()
	svc.evictOldHistoryLocked(want.Sequence() + historyWindow)
	_, retained := svc.ledgerHistory[want.Sequence()]
	svc.mu.Unlock()
	require.False(t, retained)
	require.Equal(t, strconv.FormatUint(uint64(want.Sequence()), 10), svc.completeLedgersString())

	got, err := svc.GetLedgerBySequence(want.Sequence())
	require.NoError(t, err)
	require.Equal(t, want.Hash(), got.Hash())
	require.True(t, got.IsValidated())

	selected, validated, err := svc.getLedgerForQuery(strconv.FormatUint(uint64(want.Sequence()), 10))
	require.NoError(t, err)
	require.True(t, validated)
	require.Equal(t, want.Hash(), selected.Hash())
}

func TestCompleteLedgers_EvictionRemovesUndurableSequence(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)

	const seq uint32 = 20
	l := makeStubLedger(t, seq, [32]byte{0x20}, [32]byte{0x19})
	svc.mu.Lock()
	svc.putHistoryLocked(l)
	svc.mu.Unlock()
	token := svc.beginValidatedPersistence(seq, l.Hash())
	svc.recordValidatedPersistence(seq, token, true)
	require.Equal(t, strconv.FormatUint(uint64(seq), 10), svc.completeLedgersString())

	svc.mu.Lock()
	svc.evictOldHistoryLocked(seq + historyWindow)
	svc.mu.Unlock()

	require.Equal(t, "empty", svc.completeLedgersString())
	_, err = svc.GetLedgerBySequence(seq)
	require.ErrorIs(t, err, svcerr.ErrLedgerNotFound)
}

func TestCompleteLedgers_EvictionCancelsPendingPersistence(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	svc.persistMu.Lock()
	svc.persistStarted = true
	svc.persistMu.Unlock()

	const seq uint32 = 21
	l := makeStubLedger(t, seq, [32]byte{0x21}, [32]byte{0x20})
	svc.mu.Lock()
	svc.putHistoryLocked(l)
	svc.mu.Unlock()
	svc.enqueueValidatedHistoryPersist(l)

	svc.persistMu.Lock()
	require.Len(t, svc.persistQueue, 1)
	job := svc.persistQueue[0]
	svc.persistQueue = nil
	svc.persistMu.Unlock()

	svc.mu.Lock()
	svc.evictOldHistoryLocked(seq + historyWindow)
	svc.mu.Unlock()

	require.True(t, job.canceled.Load())
	require.Equal(t, "empty", svc.completeLedgersString())
	svc.runPersistJob(job)
	require.Equal(t, "empty", svc.completeLedgersString())
}

func TestSwitchToPreferredLedger_PreservesValidatedFrontier(t *testing.T) {
	tests := []struct {
		name      string
		candidate func(t *testing.T, validatedSeq uint32, validatedHash [32]byte) *ledger.Ledger
	}{
		{
			name: "below validated sequence",
			candidate: func(t *testing.T, validatedSeq uint32, _ [32]byte) *ledger.Ledger {
				return makeStubLedger(t, validatedSeq-1, [32]byte{0xA1}, [32]byte{})
			},
		},
		{
			name: "same sequence sibling",
			candidate: func(t *testing.T, validatedSeq uint32, validatedHash [32]byte) *ledger.Ledger {
				hash := validatedHash
				hash[0] ^= 0xFF
				return makeStubLedger(t, validatedSeq, hash, [32]byte{})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, err := New(DefaultConfig())
			require.NoError(t, err)
			require.NoError(t, svc.Start())
			t.Cleanup(svc.Stop)

			closedBefore := svc.GetClosedLedger()
			openBefore := svc.GetOpenLedger()
			validatedBefore := svc.GetValidatedLedger()
			require.NotNil(t, validatedBefore)
			require.NotZero(t, validatedBefore.Sequence())
			candidate := test.candidate(t, validatedBefore.Sequence(), validatedBefore.Hash())

			err = svc.SwitchToPreferredLedger(candidate)
			require.ErrorIs(t, err, ErrPreferredChainSwitch)
			require.Same(t, closedBefore, svc.GetClosedLedger())
			require.Same(t, openBefore, svc.GetOpenLedger())
			require.Same(t, validatedBefore, svc.GetValidatedLedger())
		})
	}
}
