package adaptor

import (
	"bytes"
	"testing"

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
	}{
		{name: "state", infoType: message.LedgerInfoAsNode, reason: "ledger-data-state"},
		{name: "transaction", infoType: message.LedgerInfoTxNode, reason: "ledger-data-tx", transaction: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, sender := makeRouterWithBadDataRecorder(t)
			ledger, valid := newLedgerNodeChargeAcquisition(t, r, tc.transaction)
			hash := ledger.Hash()

			before := ledger.Snapshot()
			malformed := valid[0]
			malformed.NodeID = []byte{1}
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
