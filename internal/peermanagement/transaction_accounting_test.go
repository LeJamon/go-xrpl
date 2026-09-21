package peermanagement

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func newTransactionAccountingOverlay(t *testing.T, txCapacity int, enabled bool) (*Overlay, *Peer, *resource.Consumer) {
	t.Helper()
	identity, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(PeerID(901), Endpoint{Host: "127.0.0.1", Port: 51235}, false, identity, nil)
	if enabled {
		caps := NewPeerCapabilities()
		caps.Features.Enable(FeatureTxReduceRelay)
		peer.capabilities = caps
	}
	manager := resource.NewManager(nil, nil)
	consumer := manager.NewInboundEndpoint(peer.Endpoint().String())
	peer.attachUsage(consumer, nil)
	t.Cleanup(peer.releaseUsage)

	o := &Overlay{
		cfg:        Config{EnableTxReduceRelay: enabled},
		peers:      map[PeerID]*Peer{peer.ID(): peer},
		txMessages: make(chan *InboundMessage, txCapacity),
	}
	return o, peer, consumer
}

func transactionBatchPayload(t *testing.T, count int) []byte {
	t.Helper()
	transactions := make([]message.Transaction, count)
	for i := range transactions {
		transactions[i] = message.Transaction{
			RawTransaction: []byte{0x01, byte(i), 0x03},
			Status:         message.TxStatusCurrent,
		}
	}
	payload, err := message.Encode(&message.Transactions{Transactions: transactions})
	require.NoError(t, err)
	return payload
}

func oversizedTransactionBatchPayload(t *testing.T, count int) []byte {
	t.Helper()
	one := transactionBatchPayload(t, 1)
	field, wireType, tagSize := protowire.ConsumeTag(one)
	require.Equal(t, protowire.BytesType, wireType)
	inner, valueSize := protowire.ConsumeBytes(one[tagSize:])
	require.Positive(t, valueSize)

	payload := make([]byte, 0, len(one)*count)
	for range count {
		payload = protowire.AppendTag(payload, field, wireType)
		payload = protowire.AppendBytes(payload, inner)
	}
	return payload
}

func TestTransactionsBatchAdmissionBoundaryPrecedesProcessingAndFanout(t *testing.T) {
	below, peer, belowConsumer := newTransactionAccountingOverlay(t, MaxTxQueueSize-1, true)
	belowPayload := transactionBatchPayload(t, MaxTxQueueSize-1)
	below.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeTransactions,
		Payload:     belowPayload,
		WireSize:    uint64(len(belowPayload)),
	})
	require.Len(t, below.txMessages, MaxTxQueueSize-1)
	require.Equal(t, uint64(1), below.txm.transactions.count.accum)
	require.Equal(t, uint64(MaxTxQueueSize-1), below.txm.missingTx.accum)
	require.Zero(t, belowConsumer.Balance())
	for range MaxTxQueueSize - 1 {
		require.NoError(t, (<-below.txMessages).Close())
	}

	accepted, peer, acceptedConsumer := newTransactionAccountingOverlay(t, MaxTxQueueSize, true)
	acceptedPayload := transactionBatchPayload(t, MaxTxQueueSize)
	accepted.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeTransactions,
		Payload:     acceptedPayload,
		WireSize:    uint64(len(acceptedPayload)),
	})

	require.Len(t, accepted.txMessages, MaxTxQueueSize)
	require.Equal(t, uint64(1), accepted.txm.transactions.count.accum)
	require.Equal(t, uint64(MaxTxQueueSize), accepted.txm.missingTx.accum)
	require.Zero(t, acceptedConsumer.Balance())
	for range MaxTxQueueSize {
		require.NoError(t, (<-accepted.txMessages).Close())
	}

	rejected, peer, rejectedConsumer := newTransactionAccountingOverlay(t, MaxTxQueueSize, true)
	rejectedPayload := oversizedTransactionBatchPayload(t, MaxTxQueueSize+1)
	rejected.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeTransactions,
		Payload:     rejectedPayload,
		WireSize:    uint64(len(rejectedPayload)),
	})

	require.Empty(t, rejected.txMessages)
	require.Equal(t, uint64(1), rejected.txm.transactions.count.accum)
	require.Zero(t, rejected.txm.missingTx.accum)
	for range resource.DecayWindowSeconds {
		rejected.onMessageReceived(Event{
			PeerID:      peer.ID(),
			MessageType: message.TypeTransactions,
			Payload:     rejectedPayload,
			WireSize:    uint64(len(rejectedPayload)),
		})
	}
	require.GreaterOrEqual(t, rejectedConsumer.Balance(), int64(resource.FeeMalformedRequest().Cost()))
	require.Less(t, rejectedConsumer.Balance(), int64(2*resource.FeeMalformedRequest().Cost()))
}

func TestDirectTransactionIsUngatedByReduceRelay(t *testing.T) {
	o, peer, _ := newTransactionAccountingOverlay(t, 1, false)
	payload, err := message.Encode(&message.Transaction{
		RawTransaction: []byte{0x01, 0x02, 0x03},
		Status:         message.TxStatusCurrent,
	})
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeTransaction,
		Payload:     payload,
	})

	msg := <-o.txMessages
	require.NoError(t, msg.Close())
}

func TestTransactionBatchElementChargePreservesStrongerCharge(t *testing.T) {
	o, peer, consumer := newTransactionAccountingOverlay(t, 1, true)
	payload := transactionBatchPayload(t, 1)
	for range resource.DecayWindowSeconds {
		o.onMessageReceived(Event{
			PeerID:      peer.ID(),
			MessageType: message.TypeTransactions,
			Payload:     payload,
		})

		msg := <-o.txMessages
		require.True(t, msg.SelectPeerCharge(resource.FeeInvalidSignature(), "transaction-invalid-signature"))
		require.True(t, msg.SelectPeerCharge(resource.FeeInvalidData(), "transaction-decode"))
		require.NoError(t, msg.Close())
	}
	require.Equal(t, int64(resource.FeeInvalidSignature().Cost()), consumer.Balance())
}

func TestMessageChargeEqualSelectionKeepsOneContext(t *testing.T) {
	o, peer, _ := newTransactionAccountingOverlay(t, 1, true)
	payload := transactionBatchPayload(t, 1)
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeTransactions,
		Payload:     payload,
	})

	msg := <-o.txMessages
	for range MaxTxQueueSize {
		require.True(t, msg.SelectPeerCharge(resource.FeeInvalidData(), "transaction-invalid-data"))
	}
	charge := msg.charge
	charge.mu.Lock()
	require.Equal(t, message.TypeTransactions.String()+" transaction-invalid-data", charge.context)
	charge.mu.Unlock()
	require.NoError(t, msg.Close())
}

func TestRetainedBatchChargeCompletesOncePerChild(t *testing.T) {
	o, peer, _ := newTransactionAccountingOverlay(t, 2, true)
	payload := transactionBatchPayload(t, 2)
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeTransactions,
		Payload:     payload,
	})

	first := <-o.txMessages
	second := <-o.txMessages
	charge := first.charge
	require.Same(t, charge, second.charge)
	require.True(t, first.SelectPeerCharge(resource.FeeInvalidData(), "first"))
	first.CompletePeerCharge()
	require.NoError(t, first.Close())

	charge.mu.Lock()
	require.False(t, charge.finished)
	require.Equal(t, resource.FeeInvalidData(), charge.fee)
	charge.mu.Unlock()

	require.True(t, second.SelectPeerCharge(resource.FeeInvalidSignature(), "second"))
	require.NoError(t, second.Close())

	charge.mu.Lock()
	require.True(t, charge.finished)
	require.Equal(t, resource.FeeInvalidSignature(), charge.fee)
	charge.mu.Unlock()
}

func TestBatchEnvelopeAndElementChargesRemainSeparate(t *testing.T) {
	o, peer, consumer := newTransactionAccountingOverlay(t, 2, true)
	payload := transactionBatchPayload(t, 2)
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeTransactions,
		Payload:     payload,
	})

	malformed := <-o.txMessages
	invalidSignature := <-o.txMessages
	require.True(t, malformed.SelectPeerCharge(resource.FeeInvalidData(), "transaction-invalid-data"))
	require.True(t, invalidSignature.ChargePeer(resource.FeeInvalidSignature(), "transaction-invalid-signature"))
	require.NoError(t, malformed.Close())
	require.NoError(t, invalidSignature.Close())

	// The malformed transaction is charged once at the shared envelope tier;
	// the invalid-signature transaction is an additional per-job charge.
	require.GreaterOrEqual(t, consumer.Balance(), int64(resource.FeeInvalidSignature().Cost()/resource.DecayWindowSeconds+1))
}
