package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func takeOutboundFrame(peer *Peer) ([]byte, bool) {
	queue := peer.outbound
	queue.mu.Lock()
	if queue.closed || queue.inFlight != nil {
		queue.mu.Unlock()
		return nil, false
	}
	token, ok := queue.nextLocked()
	if !ok {
		queue.mu.Unlock()
		return nil, false
	}
	queue.inFlight = &token
	queue.mu.Unlock()
	data := token.data
	queue.complete(token)
	return data, true
}

func requireOutboundFrame(t *testing.T, peer *Peer) []byte {
	t.Helper()
	frame, ok := takeOutboundFrame(peer)
	require.True(t, ok, "expected an outbound frame")
	return frame
}
