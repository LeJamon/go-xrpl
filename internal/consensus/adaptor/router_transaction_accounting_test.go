package adaptor

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func TestRouterTransactionDecodeFailuresSelectInvalidData(t *testing.T) {
	router := newTestRouter(&mockEngine{}, newTestAdaptor(t), nil)

	direct := router.handleTransaction(&peermanagement.InboundMessage{
		PeerID:  1,
		Type:    message.TypeTransaction,
		Payload: []byte{0xff},
	})
	require.Equal(t, resource.FeeInvalidData(), direct.charge)
	require.Equal(t, "transaction-decode", direct.chargeContext)
	require.Error(t, direct.submitError)

	inner := router.handleTransaction(&peermanagement.InboundMessage{
		PeerID: 2,
		Type:   message.TypeTransaction,
		Tx:     &message.Transaction{RawTransaction: []byte{0x01, 0x02, 0x03}},
	})
	require.Equal(t, resource.FeeInvalidData(), inner.charge)
	require.Equal(t, "transaction-invalid-data", inner.chargeContext)
	require.Error(t, inner.submitError)
	require.False(t, inner.relayed)
}

func TestRouterMixedTransactionElementsClassifyIndependently(t *testing.T) {
	router := newTestRouter(&mockEngine{}, newTestAdaptor(t), nil)
	parsed, err := tx.ParseFromBinary(routerSignedPaymentWithMemo(t, ""))
	require.NoError(t, err)
	parsed.GetCommon().TxnSignature = "DEADBEEF"
	txMap, err := parsed.Flatten()
	require.NoError(t, err)
	hexBlob, err := binarycodec.Encode(txMap)
	require.NoError(t, err)
	badBlob, err := hex.DecodeString(hexBlob)
	require.NoError(t, err)

	payload, err := message.Encode(&message.Transactions{Transactions: []message.Transaction{
		{RawTransaction: []byte{0x01, 0x02, 0x03}, Status: message.TxStatusCurrent},
		{RawTransaction: badBlob, Status: message.TxStatusCurrent},
	}})
	require.NoError(t, err)
	decoded, err := message.Decode(message.TypeTransactions, payload)
	require.NoError(t, err)
	batch, ok := decoded.(*message.Transactions)
	require.True(t, ok)

	malformed := router.handleTransaction(&peermanagement.InboundMessage{
		PeerID: 1,
		Type:   message.TypeTransaction,
		Tx:     &batch.Transactions[0],
	})
	require.Equal(t, resource.FeeInvalidData(), malformed.charge)
	require.Error(t, malformed.submitError)

	invalidSignature := router.handleTransaction(&peermanagement.InboundMessage{
		PeerID: 2,
		Type:   message.TypeTransaction,
		Tx:     &batch.Transactions[1],
	})
	require.Equal(t, resource.FeeInvalidSignature(), invalidSignature.charge)
	require.Equal(t, "transaction-invalid-signature", invalidSignature.chargeContext)
	require.Error(t, invalidSignature.submitError)
}

func TestRouterTransactionPanicMarksAdmittedTransactionBad(t *testing.T) {
	router, sender := makeRouterWithBadDataRecorder(t)
	blob := routerSignedPaymentWithMemo(t, "")
	pending, err := openledger.ParsePendingTx(blob)
	require.NoError(t, err)

	// Let the transaction pass suppression admission, then force the submit
	// boundary to panic so the deferred transaction recovery path is exercised.
	router.adaptor = nil
	require.NotPanics(t, func() {
		router.handleTransaction(routerFetchedTransactionMessage(blob, 7))
	})

	shouldProcess, bad := router.txSeen.claim(pending.Hash, 8)
	require.False(t, shouldProcess)
	require.True(t, bad)
	calls := sender.getBadDataCalls()
	require.Len(t, calls, 1)
	require.Equal(t, uint64(7), calls[0].peerID)
	require.Equal(t, "panic-transaction", calls[0].reason)
}
