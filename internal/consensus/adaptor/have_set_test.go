package adaptor

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHaveSetFromMessageRejectsMalformedHash(t *testing.T) {
	for _, size := range []int{0, 31, 33} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			msg := &message.HaveTransactionSet{
				Status: message.TxSetStatusHave,
				Hash:   make([]byte, size),
			}

			id, status, err := HaveSetFromMessage(msg)

			require.Error(t, err)
			assert.Zero(t, id)
			assert.Equal(t, message.TxSetStatusHave, status)
		})
	}
}

func TestRouterHaveSetMalformedHashReturnsBeforeStateMutation(t *testing.T) {
	tests := []struct {
		name   string
		status message.TxSetStatus
	}{
		{name: "have", status: message.TxSetStatusHave},
		{name: "need", status: message.TxSetStatusNeed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := test.status
			r, sender := makeRouterWithRelayRecorder(t)
			engine := &mockEngine{}
			r.engine = engine

			empty, err := NewTxSet(nil)
			require.NoError(t, err)
			require.Zero(t, empty.ID())
			r.adaptor.txSetCache.Put(empty)

			have := &message.HaveTransactionSet{
				Status: status,
				Hash:   bytes.Repeat([]byte{0x9A}, 31),
			}
			r.handleMessage(&peermanagement.InboundMessage{
				PeerID:  88,
				Type:    uint16(message.TypeHaveSet),
				Payload: encodePayload(t, have),
			})

			assert.Equal(t,
				[]badDataCall{{peerID: 88, reason: "have-set-hashsize"}},
				sender.badDataCalls(),
			)
			assert.Empty(t, sender.notedTxSets())
			assert.Empty(t, recordedHaveSetTxSets(engine))
		})
	}
}

func TestRouterHaveSetNeedDoesNotMutatePeerOrEngineState(t *testing.T) {
	r, sender := makeRouterWithRelayRecorder(t)
	engine := &mockEngine{}
	r.engine = engine

	txSet, err := NewTxSet([][]byte{bytes.Repeat([]byte{0x01}, 12)})
	require.NoError(t, err)
	require.NotZero(t, txSet.ID())
	r.adaptor.txSetCache.Put(txSet)

	have := HaveSetToMessage(txSet.ID(), message.TxSetStatusNeed)
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  88,
		Type:    uint16(message.TypeHaveSet),
		Payload: encodePayload(t, have),
	})

	assert.Empty(t, sender.badDataCalls())
	assert.Empty(t, sender.notedTxSets())
	assert.Empty(t, recordedHaveSetTxSets(engine))
}

func TestRouterHaveSetDuplicateChargesUselessData(t *testing.T) {
	r, sender := makeRouterWithRelayRecorder(t)
	have := &message.HaveTransactionSet{
		Status: message.TxSetStatusHave,
		Hash:   bytes.Repeat([]byte{0x9A}, 32),
	}
	inbound := &peermanagement.InboundMessage{
		PeerID:  88,
		Type:    uint16(message.TypeHaveSet),
		Payload: encodePayload(t, have),
	}

	r.handleMessage(inbound)
	assert.Empty(t, sender.badDataCalls())

	r.handleMessage(inbound)

	require.Len(t, sender.notedTxSets(), 1)
	assert.Equal(t,
		[]badDataCall{{peerID: 88, reason: "have-set-duplicate"}},
		sender.badDataCalls(),
	)
}

func recordedHaveSetTxSets(engine *mockEngine) []consensus.TxSetID {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return append([]consensus.TxSetID(nil), engine.txSets...)
}
