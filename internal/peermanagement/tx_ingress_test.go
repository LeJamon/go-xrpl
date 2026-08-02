package peermanagement

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

func newLaneTestOverlay(consensusCap, txCap, ledgerDataCap int) *Overlay {
	return &Overlay{
		consensusMessages:        make(chan *InboundMessage, consensusCap),
		consensusControlMessages: make(chan *InboundMessage, consensusCap),
		messages:                 make(chan *InboundMessage, consensusCap),
		txMessages:               make(chan *InboundMessage, txCap),
		ledgerData:               make(chan *InboundMessage, ledgerDataCap),
		stopCh:                   make(chan struct{}),
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
			MessageType: message.TypeTransaction,
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
		"transaction flood must not consume the ordinary lane")
	assert.Equal(t, 0, len(o.consensusMessages),
		"transaction flood must not consume the consensus lane")
	assert.Equal(t, 0, len(o.consensusControlMessages),
		"transaction flood must not consume the consensus control lane")
	assert.Equal(t, uint64(0), o.DroppedMessages(),
		"DroppedMessages must not move for transaction-lane shedding")
}

// TestOverlay_TxFlood_DoesNotStarveConsensusLane is the core #1103
// regression: a transaction flood that saturates the tx lane must not
// cause consensus frames (mtPROPOSE/mtVALIDATION) or acquisition replies
// (mtLEDGER_DATA) to be dropped. Each rides its own lane, so a saturated
// tx lane leaves both untouched.
func TestOverlay_TxFlood_DoesNotStarveConsensusLane(t *testing.T) {
	o := newLaneTestOverlay(8, 2, 8)

	for range 1000 {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: message.TypeTransaction,
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
			MessageType: mt,
			Payload:     []byte{0x00},
		})
	}
	// Acquisition replies still reach their dedicated lane.
	o.onMessageReceived(Event{
		Type:        EventMessageReceived,
		PeerID:      PeerID(1),
		MessageType: message.TypeLedgerData,
		Payload:     []byte{0x00},
	})

	assert.Equal(t, 2, len(o.consensusMessages),
		"proposals/validations must reach the consensus lane despite the tx flood")
	assert.Equal(t, 1, len(o.ledgerData),
		"acquisition replies must reach the dedicated lane despite the tx flood")
	assert.Equal(t, uint64(0), o.DroppedMessages(),
		"no consensus frame may be dropped while only the tx lane is saturated")
}

func TestOverlay_OrdinaryTrafficUsesBestEffortLane(t *testing.T) {
	o := newLaneTestOverlay(1, 8, 8)

	for range 4 {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: message.TypeGetLedger,
			Payload:     []byte{0x00},
		})
	}

	assert.Greater(t, o.DroppedMessages(), uint64(0),
		"DroppedMessages must record best-effort lane overflow")
	assert.Equal(t, uint64(0), o.DroppedTransactions(),
		"DroppedTransactions must not move when only consensus frames overflow")
	assert.Equal(t, 0, len(o.txMessages),
		"ordinary traffic must never reach the tx lane")
	assert.Equal(t, 0, len(o.ledgerData),
		"ordinary traffic must never reach the acquisition lane")
	assert.Equal(t, 0, len(o.consensusMessages),
		"ordinary traffic must never reach the consensus lane")
	assert.Equal(t, 0, len(o.consensusControlMessages),
		"ordinary traffic must never reach the consensus control lane")
}

func TestOverlay_ConsensusTrafficBackpressures(t *testing.T) {
	for _, msgType := range []message.MessageType{
		message.TypeProposeLedger,
		message.TypeValidation,
		message.TypeValidatorList,
		message.TypeValidatorListCollection,
	} {
		t.Run(msgType.String(), func(t *testing.T) {
			o := newLaneTestOverlay(1, 8, 8)
			evt := Event{
				Type:        EventMessageReceived,
				PeerID:      PeerID(1),
				MessageType: msgType,
				Payload:     []byte{0x00},
			}
			o.onMessageReceived(evt)

			done := make(chan struct{})
			go func() {
				o.onMessageReceived(evt)
				close(done)
			}()
			select {
			case <-done:
				t.Fatal("full consensus message lane did not apply backpressure")
			case <-time.After(20 * time.Millisecond):
			}

			first := <-o.consensusMessages
			assert.Equal(t, msgType, first.Type)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("consensus message did not resume after capacity became available")
			}
			second := <-o.consensusMessages
			assert.Equal(t, msgType, second.Type)
			assert.Equal(t, uint64(0), o.DroppedMessages())
			assert.Empty(t, o.messages)
		})
	}
}

func TestOverlay_ConsensusControlTrafficBackpressures(t *testing.T) {
	for _, msgType := range []message.MessageType{
		message.TypeStatusChange,
		message.TypeHaveSet,
	} {
		t.Run(msgType.String(), func(t *testing.T) {
			o := newLaneTestOverlay(1, 8, 8)
			evt := Event{
				Type:        EventMessageReceived,
				PeerID:      PeerID(1),
				MessageType: msgType,
				Payload:     []byte{0x00},
			}
			o.onMessageReceived(evt)

			done := make(chan struct{})
			go func() {
				o.onMessageReceived(evt)
				close(done)
			}()
			select {
			case <-done:
				t.Fatal("full consensus control lane did not apply backpressure")
			case <-time.After(20 * time.Millisecond):
			}

			first := <-o.consensusControlMessages
			assert.Equal(t, msgType, first.Type)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("consensus control message did not resume after capacity became available")
			}
			second := <-o.consensusControlMessages
			assert.Equal(t, msgType, second.Type)
			assert.Equal(t, uint64(0), o.DroppedMessages())
			assert.Empty(t, o.messages)
			assert.Empty(t, o.consensusMessages)
		})
	}
}

func TestOverlay_ConsensusBackpressureReleasesOnShutdown(t *testing.T) {
	tests := []struct {
		name    string
		msgType message.MessageType
		lane    func(*Overlay) chan *InboundMessage
	}{
		{"priority", message.TypeValidation, func(o *Overlay) chan *InboundMessage { return o.consensusMessages }},
		{"control", message.TypeStatusChange, func(o *Overlay) chan *InboundMessage { return o.consensusControlMessages }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := newLaneTestOverlay(1, 8, 8)
			evt := Event{
				Type:        EventMessageReceived,
				PeerID:      PeerID(1),
				MessageType: tc.msgType,
				Payload:     []byte{0x00},
			}
			o.onMessageReceived(evt)

			done := make(chan struct{})
			go func() {
				o.onMessageReceived(evt)
				close(done)
			}()
			select {
			case <-done:
				t.Fatal("full consensus lane did not apply backpressure")
			case <-time.After(20 * time.Millisecond):
			}
			close(o.stopCh)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("consensus backpressure did not release during shutdown")
			}
			assert.Len(t, tc.lane(o), 1)
			assert.Equal(t, uint64(0), o.DroppedMessages())
		})
	}
}

func TestOverlay_ServiceSaturationDoesNotBlockConsensusLanes(t *testing.T) {
	o := newLaneTestOverlay(8, 8, 8)
	serviceEvent := Event{
		Type:        EventMessageReceived,
		PeerID:      PeerID(1),
		MessageType: message.TypeGetLedger,
		Payload:     []byte{0x00},
	}
	for range cap(o.messages) + 3 {
		o.onMessageReceived(serviceEvent)
	}
	require.Len(t, o.messages, cap(o.messages))
	require.Equal(t, uint64(3), o.DroppedMessages())

	priorityTypes := []message.MessageType{
		message.TypeProposeLedger,
		message.TypeValidation,
		message.TypeValidatorList,
		message.TypeValidatorListCollection,
	}
	for _, msgType := range priorityTypes {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: msgType,
			Payload:     []byte{0x00},
		})
	}
	controlTypes := []message.MessageType{
		message.TypeStatusChange,
		message.TypeHaveSet,
	}
	for _, msgType := range controlTypes {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: msgType,
			Payload:     []byte{0x00},
		})
	}

	assert.Len(t, o.consensusMessages, 4)
	assert.Len(t, o.consensusControlMessages, 2)
	for _, want := range priorityTypes {
		assert.Equal(t, want, (<-o.consensusMessages).Type)
	}
	for _, want := range controlTypes {
		assert.Equal(t, want, (<-o.consensusControlMessages).Type)
	}
	assert.Equal(t, uint64(3), o.DroppedMessages(),
		"consensus traffic must not increment service shedding")
}

// TestOverlay_AcquisitionRepliesUseDedicatedLane pins bounded backpressure:
// a requested reply waits for capacity instead of being discarded.
func TestOverlay_AcquisitionRepliesUseDedicatedLane(t *testing.T) {
	o := newLaneTestOverlay(8, 8, 1)
	o.onMessageReceived(Event{
		Type:        EventMessageReceived,
		PeerID:      PeerID(1),
		MessageType: message.TypeLedgerData,
		Payload:     []byte{0x00},
	})

	done := make(chan struct{})
	go func() {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: message.TypeLedgerData,
			Payload:     []byte{0x00},
		})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("acquisition reply was not backpressured")
	case <-time.After(20 * time.Millisecond):
	}
	<-o.ledgerData
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("acquisition reply did not resume after capacity became available")
	}

	assert.Equal(t, 1, len(o.ledgerData),
		"the resumed acquisition reply must occupy the released slot")
	assert.Equal(t, uint64(0), o.DroppedMessages(),
		"acquisition-lane overflow must not touch the consensus-lane counter")
	assert.Equal(t, uint64(0), o.DroppedTransactions(),
		"acquisition-lane overflow must not touch the tx-lane counter")
	assert.Equal(t, 0, len(o.messages),
		"acquisition replies must never reach the ordinary lane")
	assert.Equal(t, 0, len(o.txMessages),
		"acquisition replies must never reach the tx lane")
	assert.Equal(t, 0, len(o.consensusMessages),
		"acquisition replies must never reach the consensus lane")
	assert.Equal(t, 0, len(o.consensusControlMessages),
		"acquisition replies must never reach the consensus control lane")
}

func TestOverlay_AcquisitionBackpressureReleasesOnRunShutdown(t *testing.T) {
	o := newLaneTestOverlay(8, 8, 1)
	runDone := make(chan struct{})
	o.runDone = runDone
	o.ledgerData <- &InboundMessage{}

	done := make(chan struct{})
	go func() {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: message.TypeLedgerData,
			Payload:     []byte{0x00},
		})
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("acquisition reply was not backpressured")
	case <-time.After(20 * time.Millisecond):
	}
	close(runDone)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("acquisition backpressure did not release when the run stopped")
	}
	assert.Equal(t, 1, len(o.ledgerData),
		"shutdown must not enqueue the blocked acquisition reply")
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
					MessageType: message.TypeTransaction,
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
