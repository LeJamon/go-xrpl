package adaptor

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	testenv "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

// TestRouter_TxSetAcquire_LearnsTransaction pins the gotNode-equivalent
// added for issue #724: when a tx-set acquisition pulls in a leaf for a
// transaction the node had never seen, that transaction is submitted into
// the open-ledger pool, mirroring rippled ConsensusTransSetSF::gotNode →
// submitTransaction (ConsensusTransSetSF.cpp:51-78). Without it a
// partially/transiently acquired set leaves its novel txs un-relayed and
// un-sourceable, prolonging the network-wide stall this issue tracks.
func TestRouter_TxSetAcquire_LearnsTransaction(t *testing.T) {
	engine := &mockEngine{}
	a := newTestAdaptor(t)
	inbox := make(chan *peermanagement.InboundMessage, 10)

	router := NewRouter(engine, a, nil, inbox)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go router.Run(ctx)

	// A real signed payment — the tx carried by the acquired set. The
	// open-ledger Submit path rejects un-parseable blobs, so a synthetic
	// blob would silently fail to land and mask a regression.
	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")
	txn := payment.Pay(master, alice, 100_000_000).Sequence(1).Build()
	env.SignWith(txn, master)
	txMap, err := txn.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	txHash, err := tx.ComputeTxHashTransaction(txn)
	require.NoError(t, err)

	require.False(t, a.HasTx(consensus.TxID(txHash)),
		"precondition: the tx must be unknown before acquisition")

	// Build a complete tx-set SHAMap carrying the tx, keyed by its real
	// ID, then serialize it to wire nodes the way a peer reply would.
	sm, err := shamap.New(shamap.TypeTransaction)
	require.NoError(t, err)
	require.NoError(t, sm.PutWithNodeType(txHash, blob, shamap.NodeTypeTransactionNoMeta))
	setID, err := sm.Hash()
	require.NoError(t, err)
	wireNodes, err := sm.WalkWireNodes()
	require.NoError(t, err)
	require.NotEmpty(t, wireNodes, "expected at least root + leaf")

	ldNodes := make([]message.LedgerNode, 0, len(wireNodes))
	for _, n := range wireNodes {
		ldNodes = append(ldNodes, message.LedgerNode{NodeID: n.NodeID, NodeData: n.Data})
	}
	resp := &message.LedgerData{
		LedgerHash: setID[:],
		InfoType:   message.LedgerInfoTsCandidate,
		Nodes:      ldNodes,
	}
	inbox <- &peermanagement.InboundMessage{
		PeerID:  5,
		Type:    uint16(message.TypeLedgerData),
		Payload: encodePayload(t, resp),
	}

	require.Eventually(t, func() bool {
		return a.HasTx(consensus.TxID(txHash))
	}, time.Second, 10*time.Millisecond,
		"tx-set acquisition must learn the carried transaction into the open ledger")
}
