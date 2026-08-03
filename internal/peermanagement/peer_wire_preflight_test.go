package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func peerRepeatedMessageField(field protowire.Number, count int) []byte {
	wire := make([]byte, 0, count*2)
	for range count {
		wire = protowire.AppendTag(wire, field, protowire.BytesType)
		wire = protowire.AppendBytes(wire, nil)
	}
	return wire
}

func peerGetObjectTransactionsPayload(count int) []byte {
	wire := protowire.AppendTag(nil, 1, protowire.VarintType)
	wire = protowire.AppendVarint(wire, uint64(message.ObjectTypeTransactions))
	return append(wire, peerRepeatedMessageField(6, count)...)
}

func TestPeerWirePreflightChargesAndDropsBeforeDispatch(t *testing.T) {
	tests := []struct {
		name     string
		msgType  message.MessageType
		payload  []byte
		reason   string
		charge   resource.Charge
		compress bool
	}{
		{
			name:     "endpoints",
			msgType:  message.TypeEndpoints,
			payload:  peerRepeatedMessageField(3, 1_024),
			reason:   "endpoints-too-large",
			charge:   resource.FeeUselessData,
			compress: true,
		},
		{
			name:    "manifests",
			msgType: message.TypeManifests,
			payload: peerRepeatedMessageField(1, 101),
			reason:  "manifests-oversize",
			charge:  resource.FeeModerateBurdenPeer,
		},
		{
			name:    "transactions",
			msgType: message.TypeTransactions,
			payload: peerRepeatedMessageField(1, 10_001),
			reason:  "wire-invalid",
			charge:  resource.FeeInvalidData,
		},
		{
			name:    "get object transactions",
			msgType: message.TypeGetObjects,
			payload: peerGetObjectTransactionsPayload(10_001),
			reason:  "get-objects-transactions-oversize",
			charge:  resource.FeeMalformedRequest,
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame, err := message.BuildWireMessage(test.msgType, test.payload)
			require.NoError(t, err)
			if test.compress {
				var compressed bool
				frame, compressed = message.CompressFrameIfWorthwhile(frame)
				require.True(t, compressed)
			}

			identity, err := NewIdentity()
			require.NoError(t, err)
			events := make(chan Event, 1)
			peer := NewPeer(PeerID(i+1), Endpoint{Host: "192.0.2.1", Port: 51235}, false, identity, events)
			manager := resource.NewManager(nil, nil)
			consumer := manager.NewInboundEndpoint(peer.Endpoint().String())
			peer.attachUsage(consumer, func() {})
			peer.handshakeCfg.EnableCompression = test.compress
			if test.compress {
				peer.capabilities = NewPeerCapabilities()
				peer.capabilities.Features.Enable(FeatureCompression)
			}
			peer.bufReader = bufio.NewReader(bytes.NewReader(frame))

			err = peer.readLoop(context.Background())
			require.ErrorIs(t, err, io.EOF)
			require.Empty(t, events)
			require.Positive(t, peer.BadDataCount())
			require.Equal(t, test.charge, chargeForReason(test.reason))
			require.Zero(t, peer.traffic.TotalStats().MessagesIn)
		})
	}
}

func TestPeerWirePreflightMalformedUsesInvalidDataClass(t *testing.T) {
	reason := wirePreflightChargeReason(errors.Join(message.ErrMalformedWire, errors.New("bad tag")))
	require.Equal(t, "wire-invalid", reason)
	require.Equal(t, resource.FeeInvalidData, chargeForReason(reason))
}
