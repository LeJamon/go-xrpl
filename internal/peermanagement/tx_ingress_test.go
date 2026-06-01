package peermanagement

import (
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// TestOverlay_TxIngress_RippledGate pins the rippled-shape
// jq_trans_overflow trigger: when the in-flight TMTransaction count
// (proxied by messages-channel depth) already meets the configured
// MaxTransactions ceiling, the next TMTransaction frame is refused
// BEFORE the channel send and droppedTransactions increments.
// Mirrors PeerImp.cpp:1349-1355 where
// `getJobCount(jtTRANSACTION) > config().MAX_TRANSACTIONS` causes
// rippled to bump jqTransOverflow_ and skip dispatching the job.
func TestOverlay_TxIngress_RippledGate(t *testing.T) {
	const maxTx = 4
	const flooded = 64

	// Channel cap is intentionally larger than maxTx so the ceiling
	// gate fires before any hard channel-saturation drop — matching
	// the production case where MessageBufferSize > MaxTransactions.
	o := &Overlay{
		messages:        make(chan *InboundMessage, 32),
		maxTransactions: maxTx,
	}

	for i := 0; i < flooded; i++ {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: uint16(message.TypeTransaction),
			Payload:     []byte{0xde, 0xad, 0xbe, 0xef},
		})
	}

	// First `maxTx` sends land in the channel; the rest are refused
	// at the rippled-faithful ingress gate.
	wantDropped := uint64(flooded - maxTx)
	assert.Equal(t, wantDropped, o.DroppedTransactions(),
		"DroppedTransactions must count refusals at the MaxTransactions ceiling")
	assert.Equal(t, maxTx, len(o.messages),
		"messages channel must hold exactly MaxTransactions accepted frames")

	// The aggregate droppedMessages counter does NOT move for ceiling
	// refusals — only hard channel-saturation drops bump it, matching
	// the field invariant that droppedMessages tracks send-side failures.
	assert.Equal(t, uint64(0), o.DroppedMessages(),
		"DroppedMessages must not move when refusals happen at the per-type gate")
}

// TestOverlay_TxIngress_ChannelSaturationBackstop verifies that when
// the rippled-faithful gate is disabled (maxTransactions <= 0), the
// channel-saturation drop branch still records refused TMTransaction
// frames. This is the defensive backstop: even without an explicit
// ceiling, a slow consumer cannot silently lose tx-ingress signal.
func TestOverlay_TxIngress_ChannelSaturationBackstop(t *testing.T) {
	const capacity = 4
	const flooded = 64

	o := &Overlay{
		messages: make(chan *InboundMessage, capacity),
		// maxTransactions intentionally zero — gate disabled.
	}

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
		"DroppedTransactions must count channel-full drops when ceiling gate is disabled")
	assert.Equal(t, wantDropped, o.DroppedMessages(),
		"DroppedMessages aggregate must equal the type-specific count when only tx frames overflow")
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

// TestOverlay_TxIngress_NonTxBypassesGate confirms the
// rippled-faithful gate is per-type: non-TMTransaction frames are
// never refused at the ceiling, matching PeerImp.cpp where the
// jq_trans_overflow check is inside `onMessage(TMTransaction)` and
// other handlers (proposal, validation, ledger_data) ignore it.
func TestOverlay_TxIngress_NonTxBypassesGate(t *testing.T) {
	o := &Overlay{
		messages:        make(chan *InboundMessage, 32),
		maxTransactions: 2,
	}

	// Saturate the channel above maxTransactions with NON-tx frames.
	for i := 0; i < 16; i++ {
		o.onMessageReceived(Event{
			Type:        EventMessageReceived,
			PeerID:      PeerID(1),
			MessageType: uint16(message.TypeLedgerData),
			Payload:     []byte{0x00},
		})
	}
	require.Equal(t, 16, len(o.messages),
		"non-tx frames must not trip the per-type ceiling")
	assert.Equal(t, uint64(0), o.DroppedTransactions(),
		"non-tx traffic must not bump the TMTransaction counter")
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
		messages:        make(chan *InboundMessage, capacity),
		maxTransactions: capacity, // exercise the rippled-faithful gate
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
	// runtime's bookkeeping goroutines on loaded CI runners; the
	// assertion fails only on per-message fan-out (which would push
	// the delta into the thousands).
	delta := runtime.NumGoroutine() - baseline
	assert.LessOrEqual(t, delta, writers+64,
		"per-message goroutine fan-out detected: delta=%d, baseline=%d", delta, baseline)

	// Sanity: counter advanced. Exact value isn't deterministic under
	// concurrent producers (consumer never runs, but writes are
	// non-blocking), so just assert progress.
	require.Greater(t, o.DroppedTransactions(), uint64(0),
		"flood must have triggered at least one TMTransaction refusal")
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
