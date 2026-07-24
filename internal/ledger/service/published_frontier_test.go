package service

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func awaitPublishedLedger(t *testing.T, published <-chan uint32) uint32 {
	t.Helper()
	select {
	case seq := <-published:
		return seq
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published ledger")
		return 0
	}
}

func publishSequence(event *LedgerAcceptedEvent) uint32 {
	if event == nil || event.Ledger == nil {
		return 0
	}
	return event.Ledger.Sequence()
}

func TestPublishedFrontier_AdvancesBeforeCallback(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)

	validated := makeStubLedger(t, 10, [32]byte{0x10}, [32]byte{0x09})
	var observed ServerInfo
	svc.SetEventCallback(func(*LedgerAcceptedEvent) {
		observed = svc.GetServerInfo()
	})

	svc.deliverLedgerEvent(&LedgerAcceptedEvent{Ledger: validated})

	assert.True(t, observed.HavePublished)
	assert.Equal(t, uint32(10), observed.PublishedLedgerSeq)
}

func TestPublishedFrontier_IgnoresUnvalidatedAndNeverRegresses(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)

	unvalidated := mustNewOpenWithHeader(
		t,
		header.LedgerHeader{LedgerIndex: 12, Hash: [32]byte{0x12}},
		shamap.New(shamap.TypeState),
		shamap.New(shamap.TypeTransaction),
	)
	svc.deliverLedgerEvent(&LedgerAcceptedEvent{Ledger: unvalidated})
	assert.False(t, svc.GetServerInfo().HavePublished)

	newer := makeStubLedger(t, 15, [32]byte{0x15}, [32]byte{0x14})
	older := makeStubLedger(t, 14, [32]byte{0x14}, [32]byte{0x13})
	svc.deliverLedgerEvent(&LedgerAcceptedEvent{Ledger: newer})
	svc.deliverLedgerEvent(&LedgerAcceptedEvent{Ledger: older})

	info := svc.GetServerInfo()
	assert.True(t, info.HavePublished)
	assert.Equal(t, uint32(15), info.PublishedLedgerSeq)
}

func TestPublishedFrontier_LagsValidatedQueueUntilOrderedDelivery(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondDelivered := make(chan struct{})
	var calls atomic.Uint32
	svc.SetEventCallback(func(*LedgerAcceptedEvent) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-releaseFirst
		case 2:
			close(secondDelivered)
		}
	})

	first := makeStubLedger(t, 20, [32]byte{0x20}, [32]byte{0x19})
	second := makeStubLedger(t, 21, [32]byte{0x21}, [32]byte{0x20})
	svc.dispatchLedgerEvent(&LedgerAcceptedEvent{Ledger: first})

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first publication was not delivered")
	}
	assert.Equal(t, uint32(20), svc.GetServerInfo().PublishedLedgerSeq)

	svc.dispatchLedgerEvent(&LedgerAcceptedEvent{Ledger: second})
	assert.Equal(t, uint32(20), svc.GetServerInfo().PublishedLedgerSeq,
		"queued publication must not advance the frontier before ordered delivery")

	close(releaseFirst)
	select {
	case <-secondDelivered:
	case <-time.After(time.Second):
		t.Fatal("second publication was not delivered")
	}
	assert.Equal(t, uint32(21), svc.GetServerInfo().PublishedLedgerSeq)

	svc.Stop()
}

func TestPublishedFrontier_LosslessBeyondFormerQueueCapacity(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	const eventCount = 300
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	allDelivered := make(chan struct{})
	var calls atomic.Uint32
	svc.SetEventCallback(func(*LedgerAcceptedEvent) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			<-releaseFirst
		case eventCount:
			close(allDelivered)
		}
	})

	first := makeStubLedger(t, 100, [32]byte{0x00, 0x64}, [32]byte{})
	svc.dispatchLedgerEvent(&LedgerAcceptedEvent{Ledger: first})
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first publication was not delivered")
	}

	for i := uint32(1); i < eventCount; i++ {
		seq := 100 + i
		hash := [32]byte{byte(seq >> 8), byte(seq)}
		parentHash := [32]byte{byte((seq - 1) >> 8), byte(seq - 1)}
		svc.dispatchLedgerEvent(&LedgerAcceptedEvent{
			Ledger: makeStubLedger(t, seq, hash, parentHash),
		})
	}
	close(releaseFirst)

	select {
	case <-allDelivered:
	case <-time.After(5 * time.Second):
		t.Fatal("publication queue did not deliver every event")
	}
	assert.Equal(t, uint32(eventCount), calls.Load())
	assert.Equal(t, uint32(100+eventCount-1), svc.GetServerInfo().PublishedLedgerSeq)
}

func TestPublishedFrontier_HoldsGapUntilBelowTipLedgerArrives(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	const frontier = uint32(50)
	first := makeStubLedger(t, frontier, [32]byte{0x50}, [32]byte{0x49})
	missing := makeStubLedger(t, frontier+1, [32]byte{0x51}, first.Hash())
	tip := makeStubLedger(t, frontier+2, [32]byte{0x52}, missing.Hash())

	published := make(chan uint32, 3)
	svc.SetEventCallback(func(event *LedgerAcceptedEvent) {
		published <- publishSequence(event)
	})
	svc.dispatchLedgerEvent(&LedgerAcceptedEvent{Ledger: first})
	require.Equal(t, frontier, awaitPublishedLedger(t, published))

	svc.mu.Lock()
	svc.putHistoryLocked(first)
	svc.putHistoryLocked(missing)
	svc.putHistoryLocked(tip)
	svc.validatedLedger = first
	svc.localTxs = nil
	svc.mu.Unlock()

	svc.SetValidatedLedger(tip.Sequence(), tip.Hash())
	select {
	case seq := <-published:
		t.Fatalf("published ledger %d across a missing sequence", seq)
	case <-time.After(25 * time.Millisecond):
	}
	assert.Equal(t, frontier, svc.GetServerInfo().PublishedLedgerSeq)

	svc.SetValidatedLedger(missing.Sequence(), missing.Hash())
	assert.Equal(t, frontier+1, awaitPublishedLedger(t, published))
	assert.Equal(t, frontier+2, awaitPublishedLedger(t, published))
	assert.Equal(t, frontier+2, svc.GetServerInfo().PublishedLedgerSeq)
}

func TestPublishedFrontier_JumpsWhenGapExceedsLimit(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	published := make(chan uint32, 3)
	svc.SetEventCallback(func(event *LedgerAcceptedEvent) {
		published <- publishSequence(event)
	})

	first := makeStubLedger(t, 10, [32]byte{0x10}, [32]byte{0x09})
	held := makeStubLedger(t, 12, [32]byte{0x12}, [32]byte{0x11})
	tip := makeStubLedger(t, 111, [32]byte{0x6f}, [32]byte{0x6e})
	svc.dispatchLedgerEvent(&LedgerAcceptedEvent{Ledger: first})
	require.Equal(t, uint32(10), awaitPublishedLedger(t, published))

	svc.dispatchLedgerEvent(&LedgerAcceptedEvent{Ledger: held})
	svc.dispatchLedgerEvent(&LedgerAcceptedEvent{Ledger: tip})
	assert.Equal(t, uint32(111), awaitPublishedLedger(t, published))
	assert.Equal(t, uint32(111), svc.GetServerInfo().PublishedLedgerSeq)

	svc.ledgerEventMu.Lock()
	assert.Empty(t, svc.ledgerEventCandidates)
	svc.ledgerEventMu.Unlock()
}

func TestPublishedFrontier_FirstPublicationStartsAtValidatedTip(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	published := make(chan uint32, 1)
	svc.SetEventCallback(func(event *LedgerAcceptedEvent) {
		published <- publishSequence(event)
	})

	tip := makeStubLedger(t, 75, [32]byte{0x75}, [32]byte{0x74})
	svc.dispatchLedgerEvent(&LedgerAcceptedEvent{Ledger: tip})
	assert.Equal(t, tip.Sequence(), awaitPublishedLedger(t, published))
	assert.Equal(t, tip.Sequence(), svc.GetServerInfo().PublishedLedgerSeq)
}
