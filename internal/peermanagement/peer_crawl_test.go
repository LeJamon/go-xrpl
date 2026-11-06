package peermanagement

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func TestCrawlInboundListeningPort(t *testing.T) {
	for _, tc := range []struct {
		name      string
		listening bool
		inbound   bool
		converged bool
	}{
		{"reachable", true, true, true},
		{"unreachable", false, true, true},
		{"outbound reachable", true, false, true},
		{"outbound unreachable", false, false, true},
		{"unconverged", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { _ = listener.Close() })
			endpoint, err := ParseEndpoint(listener.Addr().String())
			require.NoError(t, err)
			if !tc.listening {
				require.NoError(t, listener.Close())
			}
			o, peer := newEndpointsTestOverlay(t, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			o.ctx = ctx
			o.cfg.ConnectTimeout = time.Second
			peer.inbound = tc.inbound
			peer.crawlPublic = true
			peer.conn = fakeAddrConn{remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}}
			if !tc.converged {
				peer.setTracking(PeerTrackingUnknown)
			}
			o.handleEndpointsMessage(Event{
				PeerID: peer.id,
				Payload: encodeEndpoints(t, 2, []message.Endpointv2{
					{Endpoint: Endpoint{Host: "192.0.2.1", Port: endpoint.Port}.String(), Hops: 0},
				}),
			})
			o.peerStartWG.Wait()
			o.discovery.mu.RLock()
			if !tc.inbound && tc.converged {
				require.Len(t, o.discovery.peers, 1)
				require.Contains(t, o.discovery.peers, endpoint.String())
			} else {
				require.Empty(t, o.discovery.peers)
			}
			o.discovery.mu.RUnlock()
			if !tc.inbound {
				peer.mu.RLock()
				require.False(t, peer.portCheckStarted)
				require.Zero(t, peer.listeningPort)
				peer.mu.RUnlock()
			}
			entry := o.crawlOverlay()["active"].([]map[string]any)[0]
			require.Equal(t, "127.0.0.1", entry["ip"])
			if !tc.inbound {
				require.EqualValues(t, 12345, entry["port"])
			} else if tc.listening && tc.converged {
				require.Equal(t, endpoint.Port, entry["port"])
			} else {
				require.NotContains(t, entry, "port")
			}
			if tc.inbound && tc.converged {
				peer.mu.RLock()
				require.True(t, peer.portCheckStarted)
				port := peer.listeningPort
				peer.mu.RUnlock()
				o.checkPeerListeningPort(peer, "127.0.0.1:1")
				o.peerStartWG.Wait()
				peer.mu.RLock()
				require.Equal(t, port, peer.listeningPort)
				peer.mu.RUnlock()
			}
			peer.acceptEndpointsMu.Lock()
			peer.whenAcceptEndpoints = time.Time{}
			peer.acceptEndpointsMu.Unlock()
			o.handleEndpointsMessage(Event{
				PeerID: peer.id,
				Payload: encodeEndpoints(t, 2, []message.Endpointv2{
					{Endpoint: endpoint.String(), Hops: 0},
					{Endpoint: "127.0.0.1:1", Hops: 0},
				}),
			})
			o.discovery.mu.RLock()
			if (!tc.inbound || tc.listening) && tc.converged {
				require.Len(t, o.discovery.peers, 1)
				require.Contains(t, o.discovery.peers, endpoint.String())
			} else {
				require.Empty(t, o.discovery.peers)
			}
			o.discovery.mu.RUnlock()
		})
	}
}
