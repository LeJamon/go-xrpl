package adaptor

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func newLedgerNodeChargeAcquisition(t *testing.T, r *Router, transaction bool) (*inbound.Ledger, []message.LedgerNode) {
	t.Helper()

	seq := r.adaptor.LedgerService().GetClosedLedger().Sequence()
	h := header.LedgerHeader{LedgerIndex: seq}
	var source *shamap.SHAMap
	var opts []inbound.Option
	if transaction {
		source, h.TxHash, _ = buildTxSetForTest(t, 2)
		opts = append(opts, inbound.WithTransactionOnly())
	} else {
		source = shamap.New(shamap.TypeState)
		for i, first := range []byte{1, 2} {
			var key [32]byte
			key[0] = first
			key[31] = 0xA5
			require.NoError(t, source.Put(key, bytes.Repeat([]byte{byte(i + 1)}, 12)))
		}
		var err error
		h.AccountHash, err = source.Hash()
		require.NoError(t, err)
	}

	rootData, err := source.SerializeRoot()
	require.NoError(t, err)
	headerData := header.AddRaw(h, false)
	ledgerHash := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), headerData)
	ledger := inbound.New(ledgerHash, seq, 7, serveTestLogger(), opts...)
	base := []message.LedgerNode{{NodeData: headerData}}
	if transaction {
		base = append(base, message.LedgerNode{}, message.LedgerNode{NodeData: rootData})
	} else {
		base = append(base, message.LedgerNode{NodeData: rootData})
	}
	require.NoError(t, ledger.GotBase(base))

	wire, err := source.WalkWireNodes()
	require.NoError(t, err)
	valid := make([]message.LedgerNode, 0, len(wire)-1)
	for _, node := range wire {
		id, err := shamap.ParseNodeID(node.NodeID)
		require.NoError(t, err)
		if id.IsRoot() {
			continue
		}
		valid = append(valid, message.LedgerNode{NodeID: node.NodeID, NodeData: node.Data})
	}
	require.NotEmpty(t, valid)
	r.fetchTracker.Track(ledger)
	return ledger, valid
}

func TestRouter_HandleLedgerData_InvalidNodeReference_ChargesOnceAndRecovers(t *testing.T) {
	for _, tc := range []struct {
		name        string
		infoType    message.LedgerInfoType
		reason      string
		transaction bool
		badHash     bool
	}{
		{name: "state", infoType: message.LedgerInfoAsNode, reason: "ledger-data-state"},
		{name: "transaction", infoType: message.LedgerInfoTxNode, reason: "ledger-data-tx", transaction: true},
		{name: "state hash mismatch", infoType: message.LedgerInfoAsNode, reason: "ledger-data-state", badHash: true},
		{name: "transaction hash mismatch", infoType: message.LedgerInfoTxNode, reason: "ledger-data-tx", transaction: true, badHash: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, sender := makeRouterWithBadDataRecorder(t)
			ledger, valid := newLedgerNodeChargeAcquisition(t, r, tc.transaction)
			hash := ledger.Hash()

			before := ledger.Snapshot()
			malformed := valid[0]
			if tc.badHash {
				malformed.NodeData = bytes.Clone(malformed.NodeData)
				original := malformed.NodeData[0]
				for delta := 1; delta < 256; delta++ {
					malformed.NodeData[0] = original ^ byte(delta)
					if _, err := malformed.SHAMapNodeID(); err == nil {
						break
					}
				}
				_, err := malformed.SHAMapNodeID()
				require.NoError(t, err, "the mismatched hash must pass wire/reference validation")
			} else {
				malformed.NodeID = []byte{1}
			}
			r.handleMessage(&peermanagement.InboundMessage{
				PeerID: 7,
				Type:   message.TypeLedgerData,
				Payload: encodePayload(t, &message.LedgerData{
					LedgerHash: hash[:],
					LedgerSeq:  ledger.Seq(),
					InfoType:   tc.infoType,
					Nodes:      []message.LedgerNode{malformed},
				}),
			})

			require.Equal(t, []badDataCall{{peerID: 7, reason: tc.reason}}, sender.getBadDataCalls())
			afterMalformed := ledger.Snapshot()
			require.False(t, afterMalformed.Failed)
			require.Equal(t, before.StateUseful, afterMalformed.StateUseful)
			require.Equal(t, before.TxUseful, afterMalformed.TxUseful)
			require.Same(t, ledger, r.fetchTracker.Find(ledger.Hash()))

			r.handleMessage(&peermanagement.InboundMessage{
				PeerID: 7,
				Type:   message.TypeLedgerData,
				Payload: encodePayload(t, &message.LedgerData{
					LedgerHash: hash[:],
					LedgerSeq:  ledger.Seq(),
					InfoType:   tc.infoType,
					Nodes:      valid[:1],
				}),
			})

			require.Equal(t, []badDataCall{{peerID: 7, reason: tc.reason}}, sender.getBadDataCalls())
			afterValid := ledger.Snapshot()
			require.False(t, afterValid.Failed)
			require.Equal(t, inbound.StateWantState, ledger.State())
			if tc.transaction {
				require.Equal(t, before.TxUseful+1, afterValid.TxUseful)
			} else {
				require.Equal(t, before.StateUseful+1, afterValid.StateUseful)
			}
		})
	}
}

func TestLedgerDataControlStatesDoNotCharge(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Router) *inbound.Ledger
	}{
		{
			name: "missing header",
			setup: func(t *testing.T, _ *Router) *inbound.Ledger {
				return inbound.New([32]byte{0xA1}, 42, 7, serveTestLogger())
			},
		},
		{
			name: "failed",
			setup: func(t *testing.T, _ *Router) *inbound.Ledger {
				ledger := inbound.New([32]byte{0xA2}, 42, 7, serveTestLogger())
				now := time.Now().Add(time.Hour)
				for range 8 {
					ledger.OnTimer(now)
					now = now.Add(time.Hour)
				}
				require.Equal(t, inbound.StateFailed, ledger.State())
				return ledger
			},
		},
		{
			name: "completed",
			setup: func(t *testing.T, r *Router) *inbound.Ledger {
				ledger, valid := newLedgerNodeChargeAcquisition(t, r, false)
				_, err := ledger.GotStateNodesUseful(valid)
				require.NoError(t, err)
				_, _, complete, err := ledger.CollectMissingRequestContext(context.Background(), false)
				require.NoError(t, err)
				require.True(t, complete)
				return ledger
			},
		},
	} {
		for _, info := range []struct {
			name     string
			infoType message.LedgerInfoType
		}{
			{name: "state", infoType: message.LedgerInfoAsNode},
			{name: "transaction", infoType: message.LedgerInfoTxNode},
		} {
			t.Run(tc.name+"/"+info.name, func(t *testing.T) {
				r, sender := makeRouterWithBadDataRecorder(t)
				ledger := tc.setup(t, r)
				data := &message.LedgerData{
					InfoType: info.infoType,
					Nodes:    []message.LedgerNode{{NodeData: []byte{1}}},
				}

				require.True(t, r.handleInboundLedgerData(ledger, data, 7))
				require.Empty(t, sender.getBadDataCalls(), "synchronous control states must not charge")

				result := processAcquisitionWork(t.Context(), ledger, []acquisitionWorkEvent{{
					kind:   acquisitionWorkData,
					peerID: 7,
					data:   data,
				}})
				require.NoError(t, result.err)
				require.Empty(t, result.badData, "worker control states must not charge")
			})
		}
	}
}
