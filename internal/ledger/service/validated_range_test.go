package service

import (
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/stretchr/testify/require"
)

func TestTrimPendingValidatedRangeUsesLargestInteriorSide(t *testing.T) {
	for _, test := range []struct {
		name             string
		pending          []uint32
		wantMin, wantMax uint32
	}{
		{name: "lower interior gap", pending: []uint32{75}, wantMin: 76, wantMax: 100},
		{name: "upper interior gap", pending: []uint32{90}, wantMin: 60, wantMax: 89},
		{name: "multiple interior gaps", pending: []uint32{90, 75}, wantMin: 76, wantMax: 89},
		{name: "pending endpoints", pending: []uint32{60, 61, 100}, wantMin: 62, wantMax: 99},
	} {
		t.Run(test.name, func(t *testing.T) {
			min, max := trimPendingValidatedRange(60, 100, test.pending)
			require.Equal(t, test.wantMin, min)
			require.Equal(t, test.wantMax, max)
		})
	}
}

func TestValidatedLedgerRangeExcludesPendingTipUntilPersistenceCompletes(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)

	svc.mu.Lock()
	svc.publishedLedgerSeq = 100
	svc.havePublished = true
	svc.mu.Unlock()
	svc.completeMu.Lock()
	svc.completedLedgers.addRange(60, 100)
	svc.completeMu.Unlock()

	token := svc.beginValidatedPersistence(100, [32]byte{1})
	min, max, ok := svc.AvailableLedgerRange()
	require.True(t, ok)
	require.Equal(t, uint32(60), min)
	require.Equal(t, uint32(100), max)

	min, max, ok = svc.validatedLedgerRange()
	require.True(t, ok)
	require.Equal(t, uint32(60), min)
	require.Equal(t, uint32(99), max)
	info := svc.GetServerInfo()
	require.True(t, info.HaveValidatedRange)
	require.Equal(t, uint32(60), info.ValidatedRangeMin)
	require.Equal(t, uint32(99), info.ValidatedRangeMax)

	svc.recordValidatedPersistence(100, token, true)
	min, max, ok = svc.AvailableLedgerRange()
	require.True(t, ok)
	require.Equal(t, uint32(60), min)
	require.Equal(t, uint32(100), max)
	min, max, ok = svc.validatedLedgerRange()
	require.True(t, ok)
	require.Equal(t, uint32(60), min)
	require.Equal(t, uint32(100), max)
}

func TestValidatedLedgerRangeTracksSetValidatedLedgerPersistence(t *testing.T) {
	base := newTestNodeStore(t, 100)
	t.Cleanup(func() { require.NoError(t, base.Close()) })
	db := &gatedTipDatabase{
		Database: base,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
		blockSeq: 2,
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(db.release) }) }
	defer unblock()

	cfg := DefaultConfig()
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	parent := svc.GetClosedLedger()
	require.Equal(t, uint32(1), parent.Sequence())
	blob, txID := startupPaymentBlob(t, "ctid-pending-save", 1)
	_, err = svc.AcceptConsensusResult(
		t.Context(),
		parent,
		[][]byte{blob},
		nil,
		parent.CloseTime().Add(time.Second),
		true,
	)
	require.NoError(t, err)
	closed := svc.GetClosedLedger()
	require.NotNil(t, closed)
	require.Equal(t, db.blockSeq, closed.Sequence())

	svc.SetValidatedLedger(closed.Sequence(), closed.Hash())
	select {
	case <-db.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("validated persistence did not reach the tip store")
	}

	got, err := svc.GetLedgerBySequence(closed.Sequence())
	require.NoError(t, err)
	require.Equal(t, closed.Hash(), got.Hash())
	require.True(t, got.IsValidated())
	data, found, err := got.GetTransaction(txID)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEmpty(t, data)
	require.Eventually(t, func() bool {
		info := svc.GetServerInfo()
		return info.HavePublished && info.PublishedLedgerSeq == closed.Sequence()
	}, 5*time.Second, time.Millisecond)
	info := svc.GetServerInfo()
	require.True(t, info.HaveValidatedRange)
	_, availableMax, ok := svc.AvailableLedgerRange()
	require.True(t, ok)
	require.Equal(t, closed.Sequence(), availableMax)
	require.Equal(t, closed.Sequence()-1, info.ValidatedRangeMax)

	unblock()
	require.Eventually(t, func() bool {
		return svc.GetServerInfo().ValidatedRangeMax == closed.Sequence()
	}, 5*time.Second, time.Millisecond)
}
