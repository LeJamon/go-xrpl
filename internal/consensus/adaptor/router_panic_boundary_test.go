package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// panicEngine is a mockEngine whose OnProposal panics, standing in for any
// latent panic reachable from a crafted peer frame (a manual byte parser,
// SHAMap assembly, etc.). It lets the test drive a real dispatch path into a
// panic rather than crafting a corrupt payload.
type panicEngine struct {
	mockEngine
}

func (p *panicEngine) OnProposal(*consensus.Proposal, uint64) error {
	panic("simulated handler panic from a crafted frame")
}

// TestRouter_HandleMessage_RecoversHandlerPanic proves the dispatch boundary
// swallows a panic reachable from a peer frame instead of taking down the
// process, and charges the sender for bad data. Without the recover, a single
// malformed frame would be a network-wide crash vector.
func TestRouter_HandleMessage_RecoversHandlerPanic(t *testing.T) {
	svc := newTestLedgerService(t)
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	rs := &badDataRecordingSender{
		recordingSender: recordingSender{peerSupportsReplay: true},
	}
	a := New(Config{
		LedgerService: svc,
		Sender:        rs,
		Identity:      identity,
	})
	inbox := make(chan *peermanagement.InboundMessage, 8)
	r := newTestRouter(&panicEngine{}, a, inbox)

	proposeSet := &message.ProposeSet{
		ProposeSeq:     1,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     make([]byte, 33),
		CloseTime:      timeToXrplEpoch(time.Unix(1_700_000_000, 0)),
		Signature:      make([]byte, signatureMinLen),
		PreviousLedger: make([]byte, 32),
	}
	proposeSet.NodePubKey[0] = 0x02 // valid compressed key prefix

	require.NotPanics(t, func() {
		r.handleMessage(&peermanagement.InboundMessage{
			PeerID:  9,
			Type:    message.TypeProposeLedger,
			Payload: encodePayload(t, proposeSet),
		})
	}, "a panic from a frame handler must be recovered at the dispatch boundary")

	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1, "a recovered frame panic must charge the sender exactly once")
	assert.Equal(t, uint64(9), calls[0].peerID)
	assert.Equal(t, "panic-dispatch", calls[0].reason)
}

// TestRouter_RecoverFrame_ChargesStageLabel pins the per-stage reason labels so
// the worker-pool boundaries (transaction / get_ledger), which run their
// handlers on separate goroutines, are attributed distinctly from the Run-loop
// dispatch boundary.
func TestRouter_RecoverFrame_ChargesStageLabel(t *testing.T) {
	for _, stage := range []string{"dispatch", "transaction", "get_ledger"} {
		t.Run(stage, func(t *testing.T) {
			r, rs := makeRouterWithBadDataRecorder(t)
			msg := &peermanagement.InboundMessage{PeerID: 3, Type: message.TypeTransaction}

			require.NotPanics(t, func() {
				defer r.recoverFrame(msg, stage)
				panic("boom")
			})

			calls := rs.getBadDataCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, uint64(3), calls[0].peerID)
			assert.Equal(t, "panic-"+stage, calls[0].reason)
		})
	}
}
