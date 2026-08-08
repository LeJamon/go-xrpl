package peermanagement

import (
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/require"
)

func TestMessageChargeSelectsOneFeeAndFinishesOnce(t *testing.T) {
	identity, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, false, identity, nil)
	manager := resource.NewManager(nil, nil)
	consumer := manager.NewInboundEndpoint(peer.Endpoint().String())
	peer.attachUsage(consumer, nil)
	t.Cleanup(peer.releaseUsage)

	for range resource.DecayWindowSeconds {
		charge := newMessageCharge(peer, "mtPING")
		charge.update(resource.FeeInvalidData(), "malformed")
		charge.finish()
		charge.finish()
	}
	require.Equal(t, resource.FeeInvalidData().Cost(), consumer.Balance())
}

func TestMessageChargePreservesBaseAndSelectedContexts(t *testing.T) {
	charge := newMessageCharge(nil, "mtPING")
	charge.update(resource.FeeModerateBurdenPeer(), "request")
	charge.update(resource.FeeInvalidData(), "malformed")
	latest := resource.NewCharge(resource.FeeInvalidData().Cost(), "latest")
	charge.update(latest, "duplicate evidence")
	charge.update(resource.FeeUselessData(), "ignored lower tier")

	charge.mu.Lock()
	defer charge.mu.Unlock()
	require.Equal(t, latest, charge.fee)
	require.Equal(t, "mtPING request malformed duplicate evidence", charge.context)
}

func TestInboundPingChargesModerateAndPongChargesTrivial(t *testing.T) {
	identity, err := NewIdentity()
	require.NoError(t, err)
	manager := resource.NewManager(nil, nil)
	overlay := &Overlay{peers: make(map[PeerID]*Peer), cfg: DefaultConfig()}

	requestPeer := NewPeer(1, Endpoint{Host: "192.0.2.2", Port: 51235}, false, identity, nil)
	requestConsumer := manager.NewInboundEndpoint(requestPeer.Endpoint().String())
	requestPeer.attachUsage(requestConsumer, nil)
	overlay.peers[requestPeer.ID()] = requestPeer
	t.Cleanup(requestPeer.releaseUsage)

	request, err := message.Encode(&message.Ping{PType: message.PingTypePing})
	require.NoError(t, err)
	for range resource.DecayWindowSeconds {
		overlay.onMessageReceived(Event{PeerID: requestPeer.ID(), MessageType: message.TypePing, Payload: request})
	}
	require.Equal(t, resource.FeeModerateBurdenPeer().Cost(), requestConsumer.Balance())

	pongPeer := NewPeer(2, Endpoint{Host: "192.0.2.3", Port: 51235}, false, identity, nil)
	pongConsumer := manager.NewInboundEndpoint(pongPeer.Endpoint().String())
	pongPeer.attachUsage(pongConsumer, nil)
	overlay.peers[pongPeer.ID()] = pongPeer
	t.Cleanup(pongPeer.releaseUsage)

	pong, err := message.Encode(&message.Ping{PType: message.PingTypePong})
	require.NoError(t, err)
	for range resource.DecayWindowSeconds {
		overlay.onMessageReceived(Event{PeerID: pongPeer.ID(), MessageType: message.TypePing, Payload: pong})
	}
	require.Equal(t, resource.FeeTrivialPeer().Cost(), pongConsumer.Balance())
}

func TestReleaseUsagePreventsLateFallbackConsumer(t *testing.T) {
	identity, err := NewIdentity()
	require.NoError(t, err)
	peer := NewPeer(1, Endpoint{Host: "192.0.2.4", Port: 51235}, false, identity, nil)
	manager := resource.NewManager(nil, nil)
	consumer := manager.NewInboundEndpoint(peer.Endpoint().String())
	var drops atomic.Int32
	peer.attachUsage(consumer, func() { drops.Add(1) })
	peer.releaseUsage()

	require.Equal(t, resource.Ok, peer.Charge(resource.FeeDrop(), "late"))
	require.Nil(t, peer.usageHandle())
	require.Zero(t, drops.Load())
	stats := manager.Stats()
	require.Equal(t, 0, stats.Active)
	require.Equal(t, 1, stats.Retained)
}
