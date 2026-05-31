package adaptor

import (
	"context"
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/inbound"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	testenv "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// closedLedgerWithPayment builds a standalone service, applies one signed
// Payment, and closes a ledger so GetClosedLedger() returns a ledger with a
// non-empty transaction tree.
func closedLedgerWithPayment(t *testing.T) *service.Service {
	t.Helper()
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: genesis.DefaultConfig()})
	require.NoError(t, err)
	require.NoError(t, svc.Start())

	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")

	txn := payment.Pay(master, alice, 100_000_000).Sequence(1).Build()
	env.SignWith(txn, master)
	flat, err := txn.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(flat)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)

	// SubmitTransaction feeds the standalone pending pool that AcceptLedger
	// commits (SubmitOpenLedgerTx only touches the open view, not pendingTxs).
	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	res, err := svc.SubmitTransaction(parsed, blob, false)
	require.NoError(t, err)
	require.True(t, res.Applied, "payment must apply so it lands in the closed ledger")

	_, err = svc.AcceptLedger(context.Background())
	require.NoError(t, err)
	return svc
}

// TestServeLedger_TxBearing_FullRoundTrip is the end-to-end proof for the
// transaction-tree work: a goXRPL node serves a ledger that actually has
// transactions (liBASE carries node[2]=tx root, plus liTX_NODE replies) and a
// fresh acquisition reconstructs both the state and transaction trees from
// those replies alone. Mirrors rippled PeerImp::sendLedgerBase +
// processLedgerRequest (PeerImp.cpp:3119-3411) feeding InboundLedger.
//
// The acquired tx map hash equals header.TxHash — exactly the value
// completeInboundLedger hands to SubmitHeldAdoption, whose persist path is
// already covered for a non-empty tx map by the replay-delta adoption tests.
func TestServeLedger_TxBearing_FullRoundTrip(t *testing.T) {
	t.Parallel()
	svc := closedLedgerWithPayment(t)

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	adaptor := New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
	})
	router := NewRouter(&mockEngine{}, adaptor, nil, nil)

	l := svc.GetClosedLedger()
	require.NotNil(t, l)
	hash := l.Hash()
	seq := l.Sequence()

	wantTxHash, err := l.TxMapHash()
	require.NoError(t, err)
	require.NotEqual(t, [32]byte{}, wantTxHash, "fixture must have a non-empty tx tree")
	wantStateHash, err := l.StateMapHash()
	require.NoError(t, err)

	// liBASE must now carry the tx root (node[2]) alongside header + state root.
	baseNodes := router.buildLedgerBaseNodes(l)
	require.Len(t, baseNodes, 3, "tx-bearing base must carry header + state root + tx root")
	require.NotEmpty(t, baseNodes[2].NodeData, "node[2] (tx root) must be non-empty")

	// Acquire from the served replies alone.
	il := inbound.New(hash, seq, 7, serveTestLogger())
	require.NoError(t, il.GotBase(baseNodes))
	require.False(t, il.IsComplete(), "must still need state + tx nodes after base")

	asReq := func(ids [][]byte) *message.GetLedger {
		return &message.GetLedger{LedgerHash: hash[:], LedgerSeq: seq, NodeIDs: ids}
	}
	for i := 0; i < 500 && !il.IsComplete(); i++ {
		progressed := false
		if ids := il.NeedsMissingNodeIDs(); len(ids) > 0 {
			nodes := router.serveLedgerMapNodes(l.StateMapSnapshot, asReq(ids), 7, "state")
			require.NotEmpty(t, nodes, "serve must answer requested state nodes")
			require.NoError(t, il.GotStateNodes(nodes))
			progressed = true
		}
		if ids := il.NeedsMissingTxNodeIDs(); len(ids) > 0 {
			nodes := router.serveLedgerMapNodes(l.TxMapSnapshot, asReq(ids), 7, "tx")
			require.NotEmpty(t, nodes, "serve must answer requested tx nodes")
			require.NoError(t, il.GotTransactionNodes(nodes))
			progressed = true
		}
		require.True(t, progressed, "acquisition stalled with neither tree complete")
	}

	require.True(t, il.IsComplete(), "tx-bearing ledger must fully acquire goXRPL→goXRPL")

	gotHdr, gotState, gotTx, err := il.Result()
	require.NoError(t, err)
	require.Equal(t, hash, gotHdr.Hash)
	require.NotNil(t, gotTx, "tx-bearing ledger must yield a non-nil tx map")

	gotTxHash, err := gotTx.Hash()
	require.NoError(t, err)
	require.Equal(t, wantTxHash, gotTxHash,
		"acquired tx map must match header.TxHash — the value handed to adoption")
	require.Equal(t, wantTxHash, gotHdr.TxHash, "acquired header must carry the tx root hash")

	gotStateHash, err := gotState.Hash()
	require.NoError(t, err)
	require.Equal(t, wantStateHash, gotStateHash, "acquired state map must match the served ledger")
}
