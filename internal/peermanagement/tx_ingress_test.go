package peermanagement

import (
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// newLaneTestOverlay builds a bare Overlay with the three inbound lanes
// wired, sized for ingress-routing tests. The zero-value cfg leaves
// reduce-relay metrics off so onMessageReceived takes the plain forward
// path.
func newLaneTestOverlay(consensusCap, txCap, ledgerDataCap int) *Overlay {
	return &Overlay{
		messages:   make(chan *InboundMessage, consensusCap),
		txMessages: make(chan *InboundMessage, txCap),
		ledgerData: make(chan *InboundMessage, ledgerDataCap),
	}
}

// TestOverlay_TxLane_BoundedByCapacity pins the tx-lane ceiling: inbound
// TMTransaction frames land on txMessages until it is full, then excess
// frames are shed and counted in droppedTransactions (the jq_trans_overflow
// signal). A full tx lane is exactly the MaxTransactions ceiling.
func TestOverlay_TxLane_BoundedByCapacity(t *testing.T) {
	const txCap = 4
	const flooded = 64

	o := newLaneTestOverlay(32, txCap, 8)

	for range flooded {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: uint16(message.TypeTransaction),
			Payload:     []byte{0xde, 0xad, 0xbe, 0xef},
		})
	}

	assert.Equal(t, txCap, len(o.txMessages),
		"tx lane must hold exactly its capacity of accepted frames")
	assert.Equal(t, uint64(flooded-txCap), o.DroppedTransactions(),
		"DroppedTransactions must count frames shed past the tx-lane ceiling")

	// The consensus lane is a different buffer and must be untouched by a
	// pure transaction flood — that is the whole point of issue #1103.
	assert.Equal(t, 0, len(o.messages),
		"transaction flood must not consume the consensus lane")
	assert.Equal(t, uint64(0), o.DroppedMessages(),
		"DroppedMessages must not move for transaction-lane shedding")
}

// TestOverlay_TxFlood_DoesNotStarveConsensusLane is the core #1103
// regression: a transaction flood that saturates the tx lane must not
// cause consensus frames (mtPROPOSE/mtVALIDATION) or acquisition replies
// (mtLEDGER_DATA) to be dropped. Each rides its own lane, so a saturated
// tx lane leaves both untouched.
func TestOverlay_TxFlood_DoesNotStarveConsensusLane(t *testing.T) {
	// Tiny tx lane, easily saturated; the other lanes have their own room.
	o := newLaneTestOverlay(8, 2, 8)

	// Saturate the tx lane well past capacity.
	for range 1000 {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: uint16(message.TypeTransaction),
			Payload:     []byte{0x01},
		})
	}
	require.Equal(t, 2, len(o.txMessages), "tx lane must be saturated")
	require.Greater(t, o.DroppedTransactions(), uint64(0),
		"flood must have shed transactions")

	// Proposals and validations still reach the consensus lane.
	for _, mt := range []message.MessageType{
		message.TypeProposeLedger,
		message.TypeValidation,
	} {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: uint16(mt),
			Payload:     []byte{0x00},
		})
	}
	// Acquisition replies still reach their dedicated lane.
	o.onMessageReceived(Event{
		Type:        EventMessageReceived,
		PeerID:      PeerID(1),
		MessageType: uint16(message.TypeLedgerData),
		Payload:     []byte{0x00},
	})

	assert.Equal(t, 2, len(o.messages),
		"proposals/validations must reach the consensus lane despite the tx flood")
	assert.Equal(t, 1, len(o.ledgerData),
		"acquisition replies must reach the dedicated lane despite the tx flood")
	assert.Equal(t, uint64(0), o.DroppedMessages(),
		"no consensus frame may be dropped while only the tx lane is saturated")
	assert.Equal(t, uint64(0), o.DroppedLedgerData(),
		"no acquisition reply may be dropped while only the tx lane is saturated")
}

// TestOverlay_NonTxUsesConsensusLane confirms the counters stay
// class-specific: a consensus frame (mtPROPOSE) that overflows the
// consensus lane bumps droppedMessages and never touches
// droppedTransactions or droppedLedgerData, so the jq_trans_overflow signal
// isn't polluted by unrelated traffic. The tx and acquisition lanes stay
// empty.
func TestOverlay_NonTxUsesConsensusLane(t *testing.T) {
	o := newLaneTestOverlay(1, 8, 8)

	for range 4 {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: uint16(message.TypeProposeLedger),
			Payload:     []byte{0x00},
		})
	}

	assert.Greater(t, o.DroppedMessages(), uint64(0),
		"DroppedMessages must record consensus-lane overflow")
	assert.Equal(t, uint64(0), o.DroppedTransactions(),
		"DroppedTransactions must not move when only consensus frames overflow")
	assert.Equal(t, uint64(0), o.DroppedLedgerData(),
		"DroppedLedgerData must not move when only consensus frames overflow")
	assert.Equal(t, 0, len(o.txMessages),
		"consensus traffic must never reach the tx lane")
	assert.Equal(t, 0, len(o.ledgerData),
		"consensus traffic must never reach the acquisition lane")
}

// TestOverlay_AcquisitionRepliesUseDedicatedLane pins the fix: mtLEDGER_DATA
// (a reply this node explicitly requested) rides its own lane, never the
// shared consensus lane, and overflow there bumps only droppedLedgerData. A
// serve/propose flood on the consensus lane therefore can't shed a requested
// acquisition reply and wedge catch-up.
func TestOverlay_AcquisitionRepliesUseDedicatedLane(t *testing.T) {
	o := newLaneTestOverlay(8, 8, 1)

	for range 4 {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: uint16(message.TypeLedgerData),
			Payload:     []byte{0x00},
		})
	}

	assert.Equal(t, 1, len(o.ledgerData),
		"acquisition replies must ride the dedicated lane up to its capacity")
	assert.Greater(t, o.DroppedLedgerData(), uint64(0),
		"DroppedLedgerData must record dedicated-lane overflow")
	assert.Equal(t, uint64(0), o.DroppedMessages(),
		"acquisition-lane overflow must not touch the consensus-lane counter")
	assert.Equal(t, uint64(0), o.DroppedTransactions(),
		"acquisition-lane overflow must not touch the tx-lane counter")
	assert.Equal(t, 0, len(o.messages),
		"acquisition replies must never reach the consensus lane")
	assert.Equal(t, 0, len(o.txMessages),
		"acquisition replies must never reach the tx lane")
}

// TestOverlay_TxLane_BoundedGoroutines is the bounded-backpressure soak:
// flood thousands of TMTransaction frames at a tiny tx lane and confirm no
// goroutine fans out per-message. The single-writer ingest path is the
// structural bound on memory growth — a future per-frame fan-out would
// scale the goroutine count with the flood size and fail this test.
func TestOverlay_TxLane_BoundedGoroutines(t *testing.T) {
	const txCap = 8
	const flooded = 10_000
	const writers = 16

	o := newLaneTestOverlay(8, txCap, 8)

	runtime.GC()
	baseline := runtime.NumGoroutine()

	var wg sync.WaitGroup
	wg.Add(writers)
	for range writers {
		go func() {
			defer wg.Done()
			for range flooded / writers {
				o.onMessageReceived(Event{
					Type:        EventMessageReceived,
					PeerID:      PeerID(1),
					MessageType: uint16(message.TypeTransaction),
					Payload:     []byte{0x01},
				})
			}
		}()
	}
	wg.Wait()
	runtime.GC()

	delta := runtime.NumGoroutine() - baseline
	assert.LessOrEqual(t, delta, writers+64,
		"per-message goroutine fan-out detected: delta=%d, baseline=%d", delta, baseline)

	require.Greater(t, o.DroppedTransactions(), uint64(0),
		"flood must have triggered at least one transaction shed")
}

// TestMessageBufferSize_NonPositiveFallback pins the helper's
// non-positive → DefaultMessageBufferSize contract. Without this,
// a non-positive cfg.MessageBufferSize would create an unbuffered
// channel and turn the non-blocking send into a drop-every-message
// path under any load.
func TestMessageBufferSize_NonPositiveFallback(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, DefaultMessageBufferSize},
		{-1, DefaultMessageBufferSize},
		{1, 1},
		{1024, 1024},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, messageBufferSize(tc.in),
			"messageBufferSize(%d)", tc.in)
	}
}

// TestTxLaneBufferSize_NonPositiveFallback pins the tx-lane helper's
// non-positive → DefaultMaxTransactions contract, mirroring the
// consensus-lane helper. A non-positive cfg.MaxTransactions must still
// yield a buffered lane so the non-blocking send doesn't degrade into a
// drop-every-transaction path.
func TestTxLaneBufferSize_NonPositiveFallback(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, DefaultMaxTransactions},
		{-1, DefaultMaxTransactions},
		{100, 100},
		{1000, 1000},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, txLaneBufferSize(tc.in),
			"txLaneBufferSize(%d)", tc.in)
	}
}
