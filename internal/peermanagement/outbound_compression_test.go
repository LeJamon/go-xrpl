package peermanagement

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type peerWriter struct {
	remote net.Conn
	errs   <-chan error
}

func startPeerWriter(t *testing.T, peer *Peer) peerWriter {
	t.Helper()
	local, remote := net.Pipe()
	peer.mu.Lock()
	peer.conn = local
	peer.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		errs <- peer.writeLoop(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		_ = local.Close()
		_ = remote.Close()
	})
	return peerWriter{remote: remote, errs: errs}
}

func readWireFrame(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	prefix := make([]byte, message.HeaderSizeUncompressed)
	_, err := io.ReadFull(conn, prefix)
	require.NoError(t, err)

	headerBytes := prefix
	if prefix[0]&0x80 != 0 {
		headerBytes = make([]byte, message.HeaderSizeCompressed)
		copy(headerBytes, prefix)
		_, err = io.ReadFull(conn, headerBytes[message.HeaderSizeUncompressed:])
		require.NoError(t, err)
	}

	header, err := message.DecodeHeader(headerBytes)
	require.NoError(t, err)
	payload := make([]byte, header.PayloadSize)
	_, err = io.ReadFull(conn, payload)
	require.NoError(t, err)
	return append(headerBytes, payload...)
}

func newOutboundCompressionPeer(id PeerID, negotiated bool) *Peer {
	peer := NewPeer(id, Endpoint{Host: "127.0.0.1", Port: 51235}, false, nil, nil)
	peer.setState(PeerStateConnected)
	peer.handshakeCfg.EnableCompression = true
	peer.capabilities = NewPeerCapabilities()
	if negotiated {
		peer.capabilities.Features.Enable(FeatureCompression)
	}
	return peer
}

func TestBroadcastSelectsOutboundCompressionPerPeer(t *testing.T) {
	compressedPeer := newOutboundCompressionPeer(1, true)
	plainPeer := newOutboundCompressionPeer(2, false)
	compressedWriter := startPeerWriter(t, compressedPeer)
	plainWriter := startPeerWriter(t, plainPeer)
	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{
		compressedPeer.ID(): compressedPeer,
		plainPeer.ID():      plainPeer,
	})

	frame, err := message.EncodeFrame(&message.Manifests{
		List: []message.Manifest{{STObject: bytes.Repeat([]byte("manifest"), 2048)}},
	})
	require.NoError(t, err)
	original := bytes.Clone(frame)
	require.NoError(t, overlay.Broadcast(frame))

	compressedWire := readWireFrame(t, compressedWriter.remote)
	plainWire := readWireFrame(t, plainWriter.remote)
	assert.Equal(t, original, frame, "broadcast input must remain immutable")
	assert.Equal(t, frame, plainWire)

	header, err := message.DecodeHeader(compressedWire)
	require.NoError(t, err)
	require.True(t, header.Compressed)
	assert.Equal(t, message.TypeManifests, header.MessageType)
	assert.Less(t, len(compressedWire), len(frame))

	payload, err := message.DecompressLZ4(
		compressedWire[message.HeaderSizeCompressed:],
		int(header.UncompressedSize),
	)
	require.NoError(t, err)
	assert.Equal(t, frame[message.HeaderSizeUncompressed:], payload)

	require.Eventually(t, func() bool {
		return compressedPeer.metrics.sent.totalBytesSnapshot() == uint64(len(compressedWire)) &&
			plainPeer.metrics.sent.totalBytesSnapshot() == uint64(len(plainWire))
	}, time.Second, time.Millisecond)
	assert.Equal(t, uint64(len(compressedWire)), compressedPeer.traffic.Stats(CategoryManifests).BytesOut)
	assert.Equal(t, uint64(len(plainWire)), plainPeer.traffic.Stats(CategoryManifests).BytesOut)
}

func TestWriteLoopRejectsUnnegotiatedCompressedFrame(t *testing.T) {
	peer := newOutboundCompressionPeer(1, false)
	writer := startPeerWriter(t, peer)
	plain, err := message.BuildWireMessage(
		message.TypeManifests,
		bytes.Repeat([]byte("manifest"), 512),
	)
	require.NoError(t, err)
	compressed, ok := message.CompressFrameIfWorthwhile(plain)
	require.True(t, ok)
	require.NoError(t, peer.Send(compressed))

	select {
	case err := <-writer.errs:
		assert.ErrorIs(t, err, errCompressionUnnegotiated)
	case <-time.After(time.Second):
		t.Fatal("write loop did not reject unnegotiated compressed frame")
	}

	require.NoError(t, writer.remote.SetReadDeadline(time.Now().Add(10*time.Millisecond)))
	buffer := make([]byte, 1)
	_, err = writer.remote.Read(buffer)
	var netErr net.Error
	require.ErrorAs(t, err, &netErr)
	assert.True(t, netErr.Timeout())
}
