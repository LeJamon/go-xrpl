package adaptor

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	testenv "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildPaymentBlob builds a serialized Payment carrying the given flags, signed
// by the genesis master key so it round-trips through the binary codec.
func buildPaymentBlob(t *testing.T, flags uint32) []byte {
	t.Helper()
	env := testenv.NewTestEnv(t)
	master := testenv.MasterAccount()
	alice := testenv.NewAccount("alice")
	txn := payment.Pay(master, alice, 100_000_000).Sequence(1).Flags(flags).Build()
	env.SignWith(txn, master)
	txMap, err := txn.Flatten()
	require.NoError(t, err)
	hexStr, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	blob, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	return blob
}

func feedTransaction(t *testing.T, r *Router, peerID uint64, blob []byte) {
	t.Helper()
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  peermanagement.PeerID(peerID),
		Type:    uint16(message.TypeTransaction),
		Payload: encodePayload(t, &message.Transaction{RawTransaction: blob, Status: message.TxStatusNew}),
	})
}

func innerBatchCharges(calls []badDataCall) []badDataCall {
	var out []badDataCall
	for _, c := range calls {
		if c.reason == "inner-batch-txn" {
			out = append(out, c)
		}
	}
	return out
}

// TestRouter_HandleTransaction_RejectsInnerBatchTxn verifies a peer-relayed
// transaction carrying tfInnerBatchTxn is dropped before submission and the
// sender is charged for bad data — unconditionally, with the Batch amendment
// disabled (the default test rules). Mirrors rippled
// PeerImp::handleTransaction (PeerImp.cpp:1287-1296).
func TestRouter_HandleTransaction_RejectsInnerBatchTxn(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)

	feedTransaction(t, r, 42, buildPaymentBlob(t, tx.TfInnerBatchTxn))

	charges := innerBatchCharges(rs.getBadDataCalls())
	require.Len(t, charges, 1, "an inner-batch tx relayed by a peer must charge the sender exactly once")
	assert.Equal(t, uint64(42), charges[0].peerID,
		"the charge must be attributed to the peer that relayed the inner-batch tx")
}

// TestRouter_HandleTransaction_AllowsNormalTxn is the control: a transaction
// without the inner-batch flag is never charged as an inner-batch relay.
func TestRouter_HandleTransaction_AllowsNormalTxn(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)

	feedTransaction(t, r, 7, buildPaymentBlob(t, 0))

	assert.Empty(t, innerBatchCharges(rs.getBadDataCalls()),
		"a normal tx must not be charged as an inner-batch relay")
}
