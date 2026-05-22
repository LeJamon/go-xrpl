package peermanagement

import (
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// TestOverlay_TxIngress_OverflowCounter pins the rippled-shape
// jq_trans_overflow signal: when the overlay→router messages channel
// is saturated and the dropped frame is a TMTransaction, the per-type
// DroppedTransactions counter increments (in addition to the
// aggregate DroppedMessages). This is the operator signal surfaced as
// server_info.jq_trans_overflow per issue #494.
func TestOverlay_TxIngress_OverflowCounter(t *testing.T) {
	const capacity = 4
	const flooded = 64

	o := &Overlay{
		messages: make(chan *InboundMessage, capacity),
	}

	// First `capacity` sends fit in the buffer; the rest fall through
	// to the default branch and bump droppedTransactions.
	for i := 0; i < flooded; i++ {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: uint16(message.TypeTransaction),
			Payload:     []byte{0xde, 0xad, 0xbe, 0xef},
		})
	}

	wantDropped := uint64(flooded - capacity)
	assert.Equal(t, wantDropped, o.DroppedTransactions(),
		"DroppedTransactions must count overflowed TMTransaction frames")
	assert.Equal(t, wantDropped, o.DroppedMessages(),
		"DroppedMessages aggregate must equal the type-specific count when only tx frames overflow")

	// The accepted frames must have actually landed in the channel.
	assert.Equal(t, capacity, len(o.messages),
		"messages channel must be full after a flood that exceeds capacity")
}

// TestOverlay_TxIngress_OnlyTxCounterMovesForTx confirms the
// droppedTransactions counter is TMTransaction-specific: dropping a
// non-tx frame bumps droppedMessages but leaves droppedTransactions
// untouched. Without this discrimination the jq_trans_overflow signal
// would conflate transaction backpressure with unrelated traffic
// (ledger_data, validations, etc.) and confuse operators.
func TestOverlay_TxIngress_OnlyTxCounterMovesForTx(t *testing.T) {
	o := &Overlay{
		messages: make(chan *InboundMessage, 1),
	}

	nonTxType := uint16(message.TypeLedgerData)
	// Fill the channel with a non-tx frame, then overflow with another.
	for i := 0; i < 4; i++ {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: nonTxType,
			Payload:     []byte{0x00},
		})
	}

	assert.Equal(t, uint64(0), o.DroppedTransactions(),
		"DroppedTransactions must not move when only non-tx frames overflow")
	assert.Greater(t, o.DroppedMessages(), uint64(0),
		"DroppedMessages aggregate must still record the non-tx drops")
}

// TestOverlay_TxIngress_BoundedGoroutines is the bounded-backpressure
// soak: flood thousands of TMTransaction frames at a tiny channel and
// confirm no goroutine fans out per-message. The single-writer ingest
// path is the structural bound on memory growth — if a future change
// fans out per-frame, the goroutine count would scale with the flood
// size and this test would fail.
func TestOverlay_TxIngress_BoundedGoroutines(t *testing.T) {
	const capacity = 8
	const flooded = 10_000
	const writers = 16

	o := &Overlay{
		messages: make(chan *InboundMessage, capacity),
	}

	// Settle the runtime before sampling baseline goroutines.
	runtime.GC()
	baseline := runtime.NumGoroutine()

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < flooded/writers; i++ {
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

	// After the flood, in-flight goroutines must not have ballooned in
	// proportion to message count. Allow generous slack to absorb the
	// runtime's bookkeeping goroutines; the assertion fails only on
	// per-message fan-out (which would push the delta into the thousands).
	delta := runtime.NumGoroutine() - baseline
	assert.LessOrEqual(t, delta, writers+8,
		"per-message goroutine fan-out detected: delta=%d, baseline=%d", delta, baseline)

	// Sanity: counter advanced. Exact value isn't deterministic under
	// concurrent producers (consumer never runs, but writes are
	// non-blocking), so just assert progress.
	require.Greater(t, o.DroppedTransactions(), uint64(0),
		"flood must have triggered at least one TMTransaction drop")
}
