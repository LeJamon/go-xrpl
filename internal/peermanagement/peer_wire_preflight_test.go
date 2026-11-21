package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

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

func peerRepeatedTransactionField(count int) []byte {
	tx := protowire.AppendTag(nil, 1, protowire.BytesType)
	tx = protowire.AppendBytes(tx, []byte{0})
	tx = protowire.AppendTag(tx, 2, protowire.VarintType)
	tx = protowire.AppendVarint(tx, 1)
	wire := make([]byte, 0, count*len(tx)+count*2)
	for range count {
		wire = protowire.AppendTag(wire, 1, protowire.BytesType)
		wire = protowire.AppendBytes(wire, tx)
	}
	return wire
}

func peerGetObjectTransactionsPayload(count int) []byte {
	wire := protowire.AppendTag(nil, 1, protowire.VarintType)
	wire = protowire.AppendVarint(wire, uint64(message.ObjectTypeTransactions))
	wire = protowire.AppendTag(wire, 2, protowire.VarintType)
	wire = protowire.AppendVarint(wire, 1)
	return append(wire, peerRepeatedMessageField(6, count)...)
}

func peerValidatorListCollectionPayload(count int) []byte {
	wire := protowire.AppendTag(nil, 1, protowire.VarintType)
	wire = protowire.AppendVarint(wire, 1)
	wire = protowire.AppendTag(wire, 2, protowire.BytesType)
	wire = protowire.AppendBytes(wire, []byte{0})
	blob := protowire.AppendTag(nil, 2, protowire.BytesType)
	blob = protowire.AppendBytes(blob, []byte{0})
	blob = protowire.AppendTag(blob, 3, protowire.BytesType)
	blob = protowire.AppendBytes(blob, []byte{0})
	for range count {
		wire = protowire.AppendTag(wire, 3, protowire.BytesType)
		wire = protowire.AppendBytes(wire, blob)
	}
	return wire
}

func peerEndpointEntries(count int) []byte {
	entry := protowire.AppendTag(nil, 1, protowire.BytesType)
	entry = protowire.AppendString(entry, "10.0.0.1:51235")
	entry = protowire.AppendTag(entry, 2, protowire.VarintType)
	entry = protowire.AppendVarint(entry, 1)
	field := protowire.AppendTag(nil, 3, protowire.BytesType)
	return bytes.Repeat(protowire.AppendBytes(field, entry), count)
}

func peerLedgerDataPayload(t *testing.T) []byte {
	t.Helper()
	payload, err := message.Encode(&message.LedgerData{
		LedgerHash: make([]byte, 32),
		LedgerSeq:  1,
		InfoType:   message.LedgerInfoAsNode,
		Nodes: []message.LedgerNode{{
			NodeData: bytes.Repeat([]byte{0x4c}, 4*1024),
		}},
	})
	require.NoError(t, err)
	return payload
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
			name:     "transactions",
			msgType:  message.TypeTransactions,
			payload:  peerRepeatedTransactionField(10_001),
			reason:   "transactions-batch-too-large",
			charge:   resource.FeeMalformedRequest(),
			compress: true,
		},
		{
			name:    "get object transactions",
			msgType: message.TypeGetObjects,
			payload: peerGetObjectTransactionsPayload(10_001),
			reason:  "get-objects-transactions-oversize",
			charge:  resource.FeeMalformedRequest(),
		},
		{
			name:    "validator list collection",
			msgType: message.TypeValidatorListCollection,
			payload: peerValidatorListCollectionPayload(6),
			reason:  "vl-coll-heavy-too-many-blobs",
			charge:  resource.FeeHeavyBurdenPeer(),
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preflightErr := message.Preflight(test.msgType, test.payload)
			require.Equal(t, test.reason, wirePreflightChargeReason(preflightErr))

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

func TestPeerEndpointsOversizeAdmissionCarriesVersionAndLimit(t *testing.T) {
	entries := peerEndpointEntries(1_024)
	versionTwo := []byte{0x08, 2}
	tests := []struct {
		name           string
		payload        []byte
		wantVersion    uint32
		wantOversized  bool
		wantFrameError bool
	}{
		{
			name:          "initialized oversized",
			payload:       slices.Concat(versionTwo, entries),
			wantVersion:   2,
			wantOversized: true,
		},
		{
			name:           "incomplete entry after size limit",
			payload:        slices.Concat(versionTwo, entries, peerRepeatedMessageField(3, 1)),
			wantFrameError: true,
		},
		{
			name:           "missing version after oversized list",
			payload:        entries,
			wantFrameError: true,
		},
		{
			name:          "version last",
			payload:       slices.Concat(entries, versionTwo),
			wantVersion:   2,
			wantOversized: true,
		},
		{
			name:          "duplicate version uses last value",
			payload:       slices.Concat([]byte{0x08, 1}, entries, versionTwo),
			wantVersion:   2,
			wantOversized: true,
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame, err := message.BuildWireMessage(message.TypeEndpoints, test.payload)
			require.NoError(t, err)

			identity, err := NewIdentity()
			require.NoError(t, err)
			events := make(chan Event, 1)
			peer := NewPeer(PeerID(200+i), Endpoint{Host: "192.0.2.1", Port: 51235}, false, identity, events)
			t.Cleanup(func() { require.NoError(t, peer.Close()) })
			manager := resource.NewManager(nil, nil)
			consumer := manager.NewInboundEndpoint(peer.Endpoint().String())
			peer.attachUsage(consumer, func() {})
			peer.bufReader = bufio.NewReader(bytes.NewReader(frame))

			err = peer.readLoop(context.Background())
			if test.wantFrameError {
				var frameErr *FrameReadError
				require.ErrorAs(t, err, &frameErr)
				require.Equal(t, message.TypeEndpoints, frameErr.MessageType)
				require.Empty(t, events)
				require.Zero(t, peer.BadDataCount())
				require.Zero(t, peer.traffic.TotalStats().MessagesIn)
				require.Zero(t, consumer.Balance())
				return
			}

			require.ErrorIs(t, err, io.EOF)
			require.Len(t, events, 1)
			event := <-events
			require.NotNil(t, event.decodedEndpoints)
			require.Equal(t, test.wantVersion, event.decodedEndpoints.Version)
			require.Equal(t, test.wantOversized, event.endpointsOversized)
			require.Empty(t, event.decodedEndpoints.EndpointsV2)
			event.release()
		})
	}
}

func TestPeerInboundLedgerDataTrafficUsesWirePayloadSize(t *testing.T) {
	payload := peerLedgerDataPayload(t)
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "compressed"}[compressed], func(t *testing.T) {
			frame, err := message.BuildWireMessage(message.TypeLedgerData, payload)
			require.NoError(t, err)
			if compressed {
				var compressedFrame bool
				frame, compressedFrame = message.CompressFrameIfWorthwhile(frame)
				require.True(t, compressedFrame)
			}
			wirePayloadSize := len(frame) - message.HeaderSizeUncompressed
			if compressed {
				wirePayloadSize = len(frame) - message.HeaderSizeCompressed
			}

			identity, err := NewIdentity()
			require.NoError(t, err)
			events := make(chan Event, 1)
			peer := NewPeer(PeerID(210), Endpoint{Host: "192.0.2.1", Port: 51235}, false, identity, events)
			t.Cleanup(func() { require.NoError(t, peer.Close()) })
			manager := resource.NewManager(nil, nil)
			consumer := manager.NewInboundEndpoint(peer.Endpoint().String())
			peer.attachUsage(consumer, func() {})
			peer.handshakeCfg.EnableCompression = compressed
			if compressed {
				peer.capabilities = NewPeerCapabilities()
				peer.capabilities.Features.Enable(FeatureCompression)
			}
			peer.bufReader = bufio.NewReader(bytes.NewReader(frame))

			require.ErrorIs(t, peer.readLoop(context.Background()), io.EOF)
			require.Len(t, events, 1)
			event := <-events
			require.Equal(t, uint64(wirePayloadSize), event.WireSize)
			total := peer.traffic.TotalStats()
			require.Equal(t, uint64(wirePayloadSize), total.BytesIn)
			require.Equal(t, uint64(1), total.MessagesIn)
			category := peer.traffic.Stats(CategoryLedgerDataStateNodeGet)
			require.Equal(t, uint64(wirePayloadSize), category.BytesIn)
			require.Equal(t, uint64(1), category.MessagesIn)
			event.release()
		})
	}
}

func TestPeerMissingRequiredLedgerFieldTerminatesBeforeAccounting(t *testing.T) {
	payload := protowire.AppendTag(nil, 2, protowire.VarintType)
	payload = protowire.AppendVarint(payload, 1)
	payload = protowire.AppendTag(payload, 3, protowire.VarintType)
	payload = protowire.AppendVarint(payload, uint64(message.LedgerInfoAsNode))
	frame, err := message.BuildWireMessage(message.TypeLedgerData, payload)
	require.NoError(t, err)

	identity, err := NewIdentity()
	require.NoError(t, err)
	events := make(chan Event, 1)
	peer := NewPeer(PeerID(220), Endpoint{Host: "192.0.2.1", Port: 51235}, false, identity, events)
	t.Cleanup(func() { require.NoError(t, peer.Close()) })
	manager := resource.NewManager(nil, nil)
	consumer := manager.NewInboundEndpoint(peer.Endpoint().String())
	peer.attachUsage(consumer, func() {})
	budget := newReadBudget(int64(len(frame) + len(payload)))
	peer.SetInboundReadBudget(budget)
	peer.bufReader = bufio.NewReader(bytes.NewReader(frame))

	err = peer.readLoop(context.Background())
	var frameErr *FrameReadError
	require.ErrorAs(t, err, &frameErr)
	require.Equal(t, message.TypeLedgerData, frameErr.MessageType)
	require.Empty(t, events)
	require.Zero(t, peer.BadDataCount())
	require.Zero(t, consumer.Balance())
	require.Zero(t, peer.traffic.TotalStats().MessagesIn)
	require.Zero(t, peer.traffic.TotalStats().BytesIn)
	require.Zero(t, readBudgetUsed(budget))
}

func TestPeerEndpointsDecodeFailureTerminatesBeforeCharge(t *testing.T) {
	tests := []struct {
		name      string
		payload   []byte
		malformed bool
	}{
		{name: "malformed wire", payload: []byte{0xff}, malformed: true},
		{name: "missing version"},
		{name: "missing nested required fields", payload: append([]byte{0x08, 2}, peerRepeatedMessageField(3, 1)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !test.malformed {
				require.NoError(t, message.Preflight(message.TypeEndpoints, test.payload))
			}
			frame, err := message.BuildWireMessage(message.TypeEndpoints, test.payload)
			require.NoError(t, err)

			identity, err := NewIdentity()
			require.NoError(t, err)
			events := make(chan Event, 1)
			peer := NewPeer(PeerID(117), Endpoint{Host: "192.0.2.1", Port: 51235}, false, identity, events)
			manager := resource.NewManager(nil, nil)
			consumer := manager.NewInboundEndpoint(peer.Endpoint().String())
			peer.attachUsage(consumer, func() {})
			peer.bufReader = bufio.NewReader(bytes.NewReader(frame))

			err = peer.readLoop(context.Background())
			var frameErr *FrameReadError
			require.ErrorAs(t, err, &frameErr)
			if test.malformed {
				require.ErrorIs(t, err, message.ErrMalformedWire)
			}
			require.Equal(t, message.TypeEndpoints, frameErr.MessageType)
			require.Empty(t, events)
			require.Zero(t, peer.BadDataCount())
			require.Zero(t, peer.traffic.TotalStats().MessagesIn)
		})
	}
}

func TestPeerRequiredProofFieldsTerminateBeforeCharge(t *testing.T) {
	for _, negotiated := range []bool{false, true} {
		t.Run(map[bool]string{false: "feature-disabled", true: "feature-negotiated"}[negotiated], func(t *testing.T) {
			frame, err := message.BuildWireMessage(message.TypeProofPathResponse, nil)
			require.NoError(t, err)

			identity, err := NewIdentity()
			require.NoError(t, err)
			events := make(chan Event, 1)
			peer := NewPeer(PeerID(118), Endpoint{Host: "192.0.2.1", Port: 51235}, false, identity, events)
			manager := resource.NewManager(nil, nil)
			consumer := manager.NewInboundEndpoint(peer.Endpoint().String())
			peer.attachUsage(consumer, func() {})
			if negotiated {
				peer.capabilities = NewPeerCapabilities()
				peer.capabilities.Features.Enable(FeatureLedgerReplay)
			}
			peer.bufReader = bufio.NewReader(bytes.NewReader(frame))

			err = peer.readLoop(context.Background())
			var frameErr *FrameReadError
			require.ErrorAs(t, err, &frameErr)
			require.Equal(t, message.TypeProofPathResponse, frameErr.MessageType)
			require.Empty(t, events)
			require.Zero(t, peer.BadDataCount())
			require.Zero(t, peer.traffic.TotalStats().MessagesIn)
		})
	}
}

func TestPeerWirePreflightMalformedUsesInvalidDataClass(t *testing.T) {
	reason := wirePreflightChargeReason(errors.Join(message.ErrMalformedWire, errors.New("bad tag")))
	require.Equal(t, "wire-invalid", reason)
	require.Equal(t, resource.FeeInvalidData(), chargeForReason(reason))
}

func TestPeerGetLedgerLimitChargesOnceAndReleasesBudget(t *testing.T) {
	for _, compress := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "compressed"}[compress], func(t *testing.T) {
			payload := protowire.AppendTag(nil, 1, protowire.VarintType)
			payload = protowire.AppendVarint(payload, uint64(message.LedgerInfoAsNode))
			payload = protowire.AppendTag(payload, 3, protowire.BytesType)
			payload = protowire.AppendBytes(payload, make([]byte, 32))
			for range 12_289 {
				payload = protowire.AppendTag(payload, 5, protowire.BytesType)
				payload = protowire.AppendBytes(payload, make([]byte, 33))
			}
			frame, err := message.BuildWireMessage(message.TypeGetLedger, payload)
			require.NoError(t, err)
			if compress {
				var compressed bool
				frame, compressed = message.CompressFrameIfWorthwhile(frame)
				require.True(t, compressed)
			}

			identity, err := NewIdentity()
			require.NoError(t, err)
			events := make(chan Event, 1)
			peer := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, false, identity, events)
			now := time.Now()
			manager := resource.NewManager(func() time.Time { return now }, nil)
			consumer := manager.NewInboundEndpoint(peer.Endpoint().String())
			peer.attachUsage(consumer, func() {})
			t.Cleanup(func() { require.NoError(t, peer.Close()) })
			peer.handshakeCfg.EnableCompression = compress
			if compress {
				peer.capabilities = NewPeerCapabilities()
				peer.capabilities.Features.Enable(FeatureCompression)
			}
			budget := newReadBudget(int64(len(frame) + len(payload)))
			peer.SetInboundReadBudget(budget)
			peer.bufReader = bufio.NewReader(bytes.NewReader(frame))

			require.ErrorIs(t, peer.readLoop(t.Context()), io.EOF)
			require.Empty(t, events)
			require.Equal(t, int64(resource.FeeInvalidData().Cost()/resource.DecayWindowSeconds), consumer.Balance())
			require.Zero(t, readBudgetUsed(budget))
			require.False(t, peer.closed.Load())
		})
	}
}
