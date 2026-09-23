//go:build cgo && docker

package peermanagement

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	"github.com/stretchr/testify/require"
)

func TestLedgerNodeDepth_Interop_RippledDocker(t *testing.T) {
	if os.Getenv("PEERTLS_DOCKER_INTEROP") == "" || os.Getenv("PEERTLS_LEDGER_NODE_DEPTH_INTEROP") == "" {
		t.Skip("PEERTLS_DOCKER_INTEROP and PEERTLS_LEDGER_NODE_DEPTH_INTEROP not set")
	}
	node := startRippledInterop(t)
	for _, version := range []protocolVersion{{2, 2}, {2, 3}} {
		t.Run(version.String(), func(t *testing.T) {
			previous := supportedProtocols
			supportedProtocols = []protocolVersion{version}
			t.Cleanup(func() { supportedProtocols = previous })
			identity, err := NewIdentity()
			require.NoError(t, err)
			cert, key, err := identity.TLSCertificatePEM()
			require.NoError(t, err)
			endpoint, err := ParseEndpoint(node.addr)
			require.NoError(t, err)
			events := make(chan Event, 128)
			peer := NewPeer(1, endpoint, false, identity, events)
			peer.handshakeCfg = DefaultHandshakeConfig()
			peer.handshakeCfg.NetworkID = 1
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			require.NoError(t, peer.Connect(ctx, PeerConfig{PeerTLSConfig: &peertls.Config{CertPEM: cert, KeyPEM: key}}))
			require.Equal(t, version.String(), peer.ProtocolVersion())
			done := make(chan error, 1)
			go func() { done <- peer.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				_ = peer.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("peer did not stop")
				}
			})
			frame, err := message.EncodeFrame(&message.GetLedger{
				InfoType:   message.LedgerInfoAsNode,
				LType:      message.LedgerTypeClosed,
				NodeIDs:    [][]byte{make([]byte, 33)},
				QueryDepth: 3,
			})
			require.NoError(t, err)
			require.NoError(t, peer.Send(frame))
			for {
				select {
				case event := <-events:
					event.release()
					if event.MessageType != message.TypeLedgerData {
						continue
					}
					decoded, err := message.Decode(event.MessageType, event.Payload)
					require.NoError(t, err)
					data := decoded.(*message.LedgerData)
					require.Equal(t, message.LedgerInfoAsNode, data.InfoType)
					require.False(t, data.HasError())
					require.NotEmpty(t, data.Nodes)
					leaves := 0
					for _, node := range data.Nodes {
						_, err := node.SHAMapNodeID()
						require.NoError(t, err)
						if version.minor == 3 {
							require.Nil(t, node.NodeID)
							require.True(t, node.ID != nil || node.Depth != nil)
							if node.Depth != nil {
								leaves++
							}
						} else {
							require.Len(t, node.NodeID, 33)
							require.Nil(t, node.ID)
							require.Nil(t, node.Depth)
						}
					}
					if version.minor == 3 {
						require.Positive(t, leaves, "rc1 must send a depth-referenced leaf")
					}
					return
				case <-ctx.Done():
					t.Fatal("timed out waiting for ledger nodes")
				}
			}
		})
	}
}
