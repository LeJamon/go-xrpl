package peermanagement

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerRunRecoversWorkerPanics(t *testing.T) {
	tests := []struct {
		name string
		loop peerRunLoop
	}{
		{name: "read", loop: peerReadLoop},
		{name: "write", loop: peerWriteLoop},
		{name: "ping", loop: peerPingLoop},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer := NewPeer(7, Endpoint{Host: "192.0.2.7", Port: 51235}, false, nil, nil)
			peer.setState(PeerStateConnected)

			waitForClose := func(context.Context) error {
				<-peer.closeCh
				return nil
			}
			panicWorker := func(context.Context) error {
				panic("injected worker panic")
			}
			read, write, ping := waitForClose, waitForClose, waitForClose
			switch tt.loop {
			case peerReadLoop:
				read = panicWorker
			case peerWriteLoop:
				write = panicWorker
			case peerPingLoop:
				ping = panicWorker
			}

			done := make(chan error, 1)
			go func() {
				done <- peer.run(context.Background(), read, write, ping)
			}()

			select {
			case err := <-done:
				require.ErrorContains(t, err, fmt.Sprintf("peer 7 %s worker panic", tt.name))
				require.ErrorContains(t, err, "injected worker panic")
			case <-time.After(time.Second):
				t.Fatal("Peer.Run did not recover the worker panic")
			}
			assert.True(t, peer.closed.Load())
			assert.Equal(t, PeerStateDisconnected, peer.State())
		})
	}
}
