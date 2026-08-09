package peermanagement

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func manifestPayloadAtSize(size int) []byte {
	payload := make([]byte, 0, size)
	for len(payload) < size {
		payload = append(payload, 0x48, 0x00)
	}
	return payload[:size]
}

func newManifestLimitTestPeer(t *testing.T, compressed bool) (*Peer, chan *InboundMessage, chan Event) {
	t.Helper()
	identity, err := NewIdentity()
	require.NoError(t, err)
	manifestMessages := make(chan *InboundMessage, 2)
	events := make(chan Event, 2)
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 51235}, false, identity, events)
	peer.SetManifestMessages(manifestMessages)
	if compressed {
		peer.handshakeCfg.EnableCompression = true
		peer.capabilities = NewPeerCapabilities()
		peer.capabilities.Features.Enable(FeatureCompression)
	}
	return peer, manifestMessages, events
}

func appendUncompressedFrame(t *testing.T, wire *bytes.Buffer, msgType message.MessageType, payload []byte) {
	t.Helper()
	frame := make([]byte, message.HeaderSizeUncompressed+len(payload))
	require.NoError(t, message.EncodeHeader(frame, uint32(len(payload)), msgType, message.AlgorithmNone, 0))
	copy(frame[message.HeaderSizeUncompressed:], payload)
	_, err := wire.Write(frame)
	require.NoError(t, err)
}

func TestManifestWireLimitUncompressedBoundariesAndDrop(t *testing.T) {
	limit := DefaultMaxManifestPayload
	exact := manifestPayloadAtSize(limit)
	over := bytes.Repeat([]byte{0x7f}, limit+1)
	var wire bytes.Buffer
	appendUncompressedFrame(t, &wire, message.TypeManifests, over)
	appendUncompressedFrame(t, &wire, message.TypeManifests, exact)
	appendUncompressedFrame(t, &wire, message.TypePing, []byte{0x08, 0x00})

	spoolDir, err := prepareManifestSpoolDir(t.TempDir())
	require.NoError(t, err)
	peer, manifestMessages, events := newManifestLimitTestPeer(t, false)
	peer.SetInboundReadBudget(newReadBudget(int64(limit + 2)))
	peer.SetManifestSpoolDir(spoolDir)
	peer.bufReader = bufio.NewReader(bytes.NewReader(wire.Bytes()))

	err = peer.readLoop(context.Background())
	require.ErrorIs(t, err, io.EOF)
	accepted := <-manifestMessages
	require.Equal(t, exact, accepted.Payload)
	require.NoError(t, accepted.Close())
	ping := <-events
	require.Equal(t, message.TypePing, ping.MessageType)
	ping.release()
	require.Zero(t, peer.BadDataCount())
	require.Equal(t, uint64(1), peer.traffic.Stats(CategoryManifests).MessagesIn)
	require.Equal(t,
		uint64(message.HeaderSizeUncompressed*2+len(over)+len(exact)+message.HeaderSizeUncompressed+2),
		peer.metrics.recv.totalBytesSnapshot())
	peer.inboundReadBudget.mu.Lock()
	require.Zero(t, peer.inboundReadBudget.used)
	peer.inboundReadBudget.mu.Unlock()
	entries, err := os.ReadDir(spoolDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func compressedManifestFrame(t *testing.T, payload []byte, uncompressedSize uint32) []byte {
	t.Helper()
	frame := make([]byte, message.HeaderSizeCompressed+len(payload))
	require.NoError(t, message.EncodeHeader(
		frame,
		uint32(len(payload)),
		message.TypeManifests,
		message.AlgorithmLZ4,
		uncompressedSize,
	))
	copy(frame[message.HeaderSizeCompressed:], payload)
	return frame
}

type headerOnlyReader struct {
	data  []byte
	calls atomic.Int64
}

func (r *headerOnlyReader) Read(dst []byte) (int, error) {
	r.calls.Add(1)
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(dst, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestPeerRejectsUnnegotiatedCompressionAtHeader(t *testing.T) {
	header := make([]byte, message.HeaderSizeCompressed)
	require.NoError(t, message.EncodeHeader(
		header,
		uint32(message.MaxMessageSize-1),
		message.TypeManifests,
		message.AlgorithmLZ4,
		DefaultMaxManifestPayload,
	))
	underlying := &headerOnlyReader{data: header}
	spoolDir, err := prepareManifestSpoolDir(t.TempDir())
	require.NoError(t, err)
	peer, _, _ := newManifestLimitTestPeer(t, false)
	peer.SetInboundReadBudget(newReadBudget(int64(DefaultMaxManifestPayload)))
	peer.SetManifestSpoolDir(spoolDir)
	peer.bufReader = bufio.NewReader(underlying)

	err = peer.readLoop(context.Background())
	require.EqualError(t, err, "compressed frame without negotiated compression")
	require.Equal(t, int64(1), underlying.calls.Load(), "header-only input must not trigger a body read")
	require.NotZero(t, peer.BadDataCount())
	peer.inboundReadBudget.mu.Lock()
	require.Zero(t, peer.inboundReadBudget.used)
	peer.inboundReadBudget.mu.Unlock()
	entries, err := os.ReadDir(spoolDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestManifestWireLimitCompressedClaimsDropWithoutDecode(t *testing.T) {
	limit := uint32(DefaultMaxManifestPayload)
	tests := []struct {
		name                 string
		wirePayload          []byte
		declaredUncompressed uint32
	}{
		{
			name:                 "wire-payload",
			wirePayload:          bytes.Repeat([]byte{0x4d}, int(limit)+1),
			declaredUncompressed: limit,
		},
		{
			name:                 "declared-uncompressed",
			wirePayload:          []byte{0xff},
			declaredUncompressed: limit + 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wire bytes.Buffer
			wire.Write(compressedManifestFrame(t, tt.wirePayload, tt.declaredUncompressed))
			appendUncompressedFrame(t, &wire, message.TypePing, []byte{0x08, 0x00})

			peer, manifestMessages, events := newManifestLimitTestPeer(t, true)
			peer.SetInboundReadBudget(newReadBudget(int64(limit)))
			peer.bufReader = bufio.NewReader(bytes.NewReader(wire.Bytes()))

			err := peer.readLoop(context.Background())
			require.ErrorIs(t, err, io.EOF)
			select {
			case got := <-manifestMessages:
				t.Fatalf("oversized compressed manifest was dispatched: %#v", got)
			default:
			}
			ping := <-events
			require.Equal(t, message.TypePing, ping.MessageType)
			ping.release()
			require.Zero(t, peer.BadDataCount())
			peer.inboundReadBudget.mu.Lock()
			require.Zero(t, peer.inboundReadBudget.used)
			peer.inboundReadBudget.mu.Unlock()
		})
	}
}

func TestManifestWireLimitCompressedExactLimitDispatches(t *testing.T) {
	limit := DefaultMaxManifestPayload
	uncompressed := manifestPayloadAtSize(limit)
	compressed, err := message.CompressLZ4(uncompressed)
	require.NoError(t, err)
	require.NotEmpty(t, compressed)
	require.Less(t, len(compressed), len(uncompressed))

	var wire bytes.Buffer
	wire.Write(compressedManifestFrame(t, compressed, uint32(limit)))
	appendUncompressedFrame(t, &wire, message.TypePing, []byte{0x08, 0x00})

	spoolDir, err := prepareManifestSpoolDir(t.TempDir())
	require.NoError(t, err)
	peer, manifestMessages, events := newManifestLimitTestPeer(t, true)
	peer.SetInboundReadBudget(newReadBudget(int64(limit + len(compressed))))
	peer.SetManifestSpoolDir(spoolDir)
	peer.bufReader = bufio.NewReader(bytes.NewReader(wire.Bytes()))

	err = peer.readLoop(context.Background())
	require.ErrorIs(t, err, io.EOF)
	accepted := <-manifestMessages
	require.Equal(t, uncompressed, accepted.Payload)
	require.Nil(t, accepted.ManifestFrame)
	require.NoError(t, accepted.Close())
	ping := <-events
	require.Equal(t, message.TypePing, ping.MessageType)
	ping.release()
	require.Zero(t, peer.BadDataCount())
	require.Equal(t, uint64(1), peer.traffic.Stats(CategoryManifests).MessagesIn)
	peer.inboundReadBudget.mu.Lock()
	require.Zero(t, peer.inboundReadBudget.used)
	peer.inboundReadBudget.mu.Unlock()
	entries, err := os.ReadDir(spoolDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestManifestWireLimitPreservesCompressionNegotiationFailure(t *testing.T) {
	limit := uint32(DefaultMaxManifestPayload)
	wire := compressedManifestFrame(t, bytes.Repeat([]byte{0x4d}, int(limit)+1), limit)
	peer, _, _ := newManifestLimitTestPeer(t, false)
	peer.bufReader = bufio.NewReader(bytes.NewReader(wire))

	err := peer.readLoop(context.Background())
	require.Error(t, err)
	require.NotErrorIs(t, err, io.EOF)
	require.NotZero(t, peer.BadDataCount(), "compression negotiation must remain charged")
}
