package peermanagement

import (
	"bytes"
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bootstrapConnection struct {
	overlay *Overlay
	peerID  PeerID
}

func startBootstrapTestOverlay(t *testing.T, opts ...Option) *Overlay {
	t.Helper()
	base := []Option{
		WithListenAddr("127.0.0.1:0"),
		WithDataDir(t.TempDir()),
		WithMaxPeers(4),
		WithMaxInbound(2),
		WithMaxOutbound(0),
		WithPrivateMode(true),
		WithCompression(false),
	}
	overlay, err := New(append(base, opts...)...)
	require.NoError(t, err)
	runDone := make(chan error, 1)
	go func() {
		runDone <- overlay.Run(context.Background())
	}()
	select {
	case <-overlay.ListenerReady():
	case err := <-runDone:
		t.Fatalf("overlay stopped before its listener became ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("overlay listener did not become ready")
	}
	t.Cleanup(func() {
		require.NoError(t, overlay.Stop())
		select {
		case <-runDone:
		case <-time.After(10 * time.Second):
			t.Error("overlay did not stop")
		}
	})
	return overlay
}

func TestOverlayPrepareAutoconnectPreservesColdTarget(t *testing.T) {
	overlay := &Overlay{}

	count, cold, lease, ok := overlay.prepareAutoconnect(5)
	require.True(t, ok)
	assert.True(t, cold)
	assert.Equal(t, 5, count)
	require.NotNil(t, lease)
	assert.Equal(t, 1, overlay.bootstrap.activeCount())

	count, cold, secondLease, ok := overlay.prepareAutoconnect(5)
	require.True(t, ok)
	assert.True(t, cold)
	assert.Equal(t, 5, count)
	assert.Nil(t, secondLease)
	assert.Equal(t, 1, overlay.bootstrap.activeCount())

	lease.markReady()
	count, cold, lease, ok = overlay.prepareAutoconnect(5)
	require.True(t, ok)
	assert.False(t, cold)
	assert.Equal(t, 5, count)
	assert.Nil(t, lease)
	assert.Zero(t, overlay.bootstrap.activeCount())
}

func TestBootstrapGovernorAllowsOneBoundedHedge(t *testing.T) {
	var governor bootstrapGovernor
	first, ok := governor.tryReserve()
	require.True(t, ok)
	require.NotNil(t, first)

	_, ok = governor.tryReserve()
	assert.False(t, ok)

	assert.True(t, first.observeProgress(bootstrapFrameProgress{
		messageType: message.TypeManifests,
		wireSize:    60_431_740,
		bytesRead:   1 << 20,
		elapsed:     19 * time.Second,
	}).hedge)

	second, ok := governor.tryReserve()
	require.True(t, ok)
	require.NotNil(t, second)
	_, ok = governor.tryReserve()
	assert.False(t, ok)
	assert.Equal(t, 2, governor.activeCount())

	second.release()
	replacement, ok := governor.tryReserve()
	require.True(t, ok)
	require.NotNil(t, replacement)
	replacement.release()
	first.markReady()

	assert.True(t, governor.isReady())
	lease, ok := governor.tryReserve()
	assert.True(t, ok)
	assert.NotNil(t, lease)
	lease.release()
}

func TestBootstrapGovernorStagesOneWarmupAfterReady(t *testing.T) {
	var governor bootstrapGovernor
	lease, ok := governor.tryReserve()
	require.True(t, ok)
	lease.markReady()

	next, reserved := governor.tryReserve()
	require.True(t, reserved)
	require.NotNil(t, next)
	_, reserved = governor.tryReserve()
	assert.False(t, reserved)
	assert.Equal(t, 1, governor.activeCount())

	next.markReady()
	assert.Equal(t, 0, governor.activeCount())
	replacement, reserved := governor.tryReserve()
	require.True(t, reserved)
	require.NotNil(t, replacement)
	replacement.release()
}

func TestBootstrapGovernorHedgesOnlySlowManifestTransfers(t *testing.T) {
	tests := []struct {
		name string
		rate uint64
		want bool
	}{
		{name: "issue first source", rate: 444_146, want: true},
		{name: "issue second source", rate: 360_735, want: true},
		{name: "live pathological source", rate: 15_704, want: true},
		{name: "finishes within target", rate: 650_000, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var governor bootstrapGovernor
			lease, ok := governor.tryReserve()
			require.True(t, ok)
			elapsed := bootstrapSampleAge
			bytesRead := tt.rate * uint64(elapsed) / uint64(time.Second)

			observation := lease.observeProgress(bootstrapFrameProgress{
				messageType: message.TypeManifests,
				wireSize:    60_431_740,
				bytesRead:   bytesRead,
				elapsed:     elapsed,
			})
			assert.True(t, observation.sampled)
			assert.Equal(t, tt.want, observation.hedge)
		})
	}
}

func TestBootstrapReleasedLeaseCannotEnableHedge(t *testing.T) {
	var governor bootstrapGovernor
	lease, ok := governor.tryReserve()
	require.True(t, ok)
	lease.release()

	assert.False(t, lease.observeProgress(bootstrapFrameProgress{
		messageType: message.TypeManifests,
		wireSize:    60_431_740,
		bytesRead:   1 << 20,
		elapsed:     30 * time.Second,
	}).hedge)
	assert.Equal(t, 0, governor.activeCount())
}

func TestBootstrapFlowRotatesTwoCutoffSourcesAndCompletesFromThird(t *testing.T) {
	now := time.Unix(5_000, 0)
	discovery := NewDiscovery(&Config{
		MaxOutbound: 3,
		Clock:       func() time.Time { return now },
	}, nil)
	for _, address := range []string{
		"192.0.2.1:51235",
		"192.0.2.2:51235",
		"192.0.2.3:51235",
	} {
		discovery.AddPeer(address, 0, 0)
		discovery.markNegotiatedCompression(address, false)
	}

	var governor bootstrapGovernor
	first, ok := governor.tryReserve()
	require.True(t, ok)
	firstCandidates := discovery.selectPeersToConnect(1, true)
	require.Len(t, firstCandidates, 1)
	firstAddress := firstCandidates[0]
	firstProgress := bootstrapFrameProgress{
		messageType: message.TypeManifests,
		wireSize:    60_431_740,
		bytesRead:   444_146 * 19,
		elapsed:     19 * time.Second,
	}
	firstObservation := first.observeProgress(firstProgress)
	require.True(t, firstObservation.hedge)
	discovery.observeBootstrapSource(firstAddress, firstObservation.projected)
	assert.LessOrEqual(t, governor.activeCount(), 2)

	second, ok := governor.tryReserve()
	require.True(t, ok)
	secondCandidates := discovery.selectPeersToConnect(1, true)
	require.Len(t, secondCandidates, 1)
	secondAddress := secondCandidates[0]
	require.NotEqual(t, firstAddress, secondAddress)
	secondProgress := bootstrapFrameProgress{
		messageType: message.TypeManifests,
		wireSize:    60_431_740,
		bytesRead:   360_735 * 24,
		elapsed:     24 * time.Second,
	}
	secondObservation := second.observeProgress(secondProgress)
	require.True(t, secondObservation.sampled)
	assert.False(t, secondObservation.hedge)
	discovery.observeBootstrapSource(secondAddress, secondObservation.projected)
	assert.Equal(t, 2, governor.activeCount())
	_, ok = governor.tryReserve()
	assert.False(t, ok)

	for _, failed := range []struct {
		address string
		lease   *bootstrapLease
	}{
		{address: firstAddress, lease: first},
		{address: secondAddress, lease: second},
	} {
		failed.lease.release()
		discovery.delayConnectRetry(failed.address, bootstrapPartialRetry)
		discovery.finishConnectAttempt(failed.address, connectAttemptReleased)
		assert.LessOrEqual(t, governor.activeCount(), 2)
	}

	third, ok := governor.tryReserve()
	require.True(t, ok)
	thirdCandidates := discovery.selectPeersToConnect(1, true)
	require.Len(t, thirdCandidates, 1)
	thirdAddress := thirdCandidates[0]
	assert.NotEqual(t, firstAddress, thirdAddress)
	assert.NotEqual(t, secondAddress, thirdAddress)
	thirdObservation := third.observeProgress(bootstrapFrameProgress{
		messageType: message.TypeManifests,
		wireSize:    60_431_740,
		bytesRead:   650_000 * 15,
		elapsed:     15 * time.Second,
	})
	require.True(t, thirdObservation.sampled)
	assert.LessOrEqual(t, thirdObservation.projected, bootstrapTargetDuration)
	discovery.observeBootstrapSource(thirdAddress, thirdObservation.projected)
	third.markReady()

	assert.True(t, governor.isReady())
	assert.Zero(t, governor.activeCount())
	lease, ok := governor.tryReserve()
	assert.True(t, ok)
	assert.NotNil(t, lease)
	lease.release()
}

func TestBootstrapFlowDoesNotDialWhileEverySourceIsCoolingDown(t *testing.T) {
	now := time.Unix(6_000, 0)
	discovery := NewDiscovery(&Config{
		MaxOutbound: 2,
		Clock:       func() time.Time { return now },
	}, nil)
	for _, address := range []string{"192.0.2.1:51235", "192.0.2.2:51235"} {
		discovery.AddPeer(address, 0, 0)
		discovery.observeBootstrapSource(address, bootstrapTargetDuration+time.Second)
	}

	var governor bootstrapGovernor
	for range 100 {
		lease, ok := governor.tryReserve()
		require.True(t, ok)
		assert.Empty(t, discovery.selectPeersToConnect(1, true))
		lease.release()
		assert.Zero(t, governor.activeCount())
	}
	status := discovery.bootstrapSourceSummary()
	assert.True(t, status.allUnviable())
	assert.Equal(t, status.known, status.unviable)

	now = now.Add(bootstrapPartialRetry)
	assert.NotEmpty(t, discovery.selectPeersToConnect(1, true))
}

func TestDiscoveryBootstrapSelectionLimitsFixedPeers(t *testing.T) {
	now := time.Unix(1_000, 0)
	d := NewDiscovery(&Config{
		MaxOutbound: 3,
		FixedPeers: []string{
			"192.0.2.1:51235",
			"192.0.2.2:51235",
		},
		Clock: func() time.Time { return now },
	}, nil)
	for _, address := range []string{
		"192.0.2.1:51235",
		"192.0.2.2:51235",
		"192.0.2.3:51235",
	} {
		d.AddPeer(address, 0, 0)
	}

	candidates := d.selectPeersToConnect(1, true)
	require.Len(t, candidates, 1)
	assert.True(t, d.IsFixed(candidates[0]))
}

func TestDiscoveryBootstrapSelectionPreservesFixedPeersAtZeroOrdinaryCapacity(t *testing.T) {
	d := NewDiscovery(&Config{
		MaxOutbound: 0,
		FixedPeers: []string{
			"192.0.2.1:51235",
			"192.0.2.2:51235",
		},
	}, nil)
	d.AddPeer("192.0.2.1:51235", 0, 0)
	d.AddPeer("192.0.2.2:51235", 0, 0)

	candidates := d.selectPeersToConnect(0, true)
	require.Len(t, candidates, 1)
	assert.True(t, d.IsFixed(candidates[0]))
}

func TestDiscoveryBootstrapSelectionPrefersKnownCompression(t *testing.T) {
	now := time.Unix(2_000, 0)
	d := NewDiscovery(&Config{
		MaxOutbound: 3,
		Clock:       func() time.Time { return now },
	}, nil)
	for _, address := range []string{
		"192.0.2.1:51235",
		"192.0.2.2:51235",
		"192.0.2.3:51235",
	} {
		d.AddPeer(address, 0, 0)
	}
	d.markNegotiatedCompression("192.0.2.1:51235", false)
	d.markNegotiatedCompression("192.0.2.2:51235", true)

	candidates := d.selectPeersToConnect(1, true)
	require.Equal(t, []string{"192.0.2.2:51235"}, candidates)
}

func TestDiscoveryBootstrapSelectionPrefersCompressionOverFixedPeer(t *testing.T) {
	now := time.Unix(2_500, 0)
	fixed := "192.0.2.1:51235"
	compressed := "192.0.2.2:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 2,
		FixedPeers:  []string{fixed},
		Clock:       func() time.Time { return now },
	}, nil)
	d.AddPeer(fixed, 0, 0)
	d.AddPeer(compressed, 0, 0)
	d.markNegotiatedCompression(fixed, false)
	d.markNegotiatedCompression(compressed, true)

	assert.Equal(t, []string{compressed}, d.selectPeersToConnect(1, true))
}

func TestDiscoveryBootstrapRetryDelayStartsAtDisconnect(t *testing.T) {
	now := time.Unix(3_000, 0)
	address := "192.0.2.1:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 1,
		Clock:       func() time.Time { return now },
	}, nil)
	d.AddPeer(address, 0, 0)
	d.MarkConnected(address, 1)
	d.delayConnectRetry(address, recentConnectAttempt)
	d.MarkDisconnected(1)

	assert.Empty(t, d.selectPeersToConnect(1, true))
	now = now.Add(recentConnectAttempt)
	assert.Equal(t, []string{address}, d.selectPeersToConnect(1, true))
}

func TestDiscoveryPartialBootstrapFailureIsQuarantined(t *testing.T) {
	now := time.Unix(4_000, 0)
	address := "192.0.2.1:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 1,
		Clock:       func() time.Time { return now },
	}, nil)
	d.AddPeer(address, 0, 0)
	overlay := &Overlay{discovery: d}
	overlay.delayPeerRetry(address, true, &FrameReadError{
		MessageType: message.TypeManifests,
		WireSize:    1024,
		BytesRead:   1,
		Err:         errors.New("connection reset"),
	}, false)

	now = now.Add(recentConnectAttempt)
	assert.Empty(t, d.selectPeersToConnect(1, true))
	now = now.Add(bootstrapPartialRetry - recentConnectAttempt)
	assert.Equal(t, []string{address}, d.selectPeersToConnect(1, true))
}

func TestOverlayPartialManifestFailurePreservesOrdinarySourceSquelch(t *testing.T) {
	now := time.Unix(4_500, 0)
	address := "192.0.2.1:51235"
	discovery := NewDiscovery(&Config{
		MaxOutbound: 1,
		Clock:       func() time.Time { return now },
	}, nil)
	discovery.AddPeer(address, 0, 0)
	require.Equal(t, []string{address}, discovery.SelectPeersToConnect(1))
	now = now.Add(10 * time.Second)
	discovery.MarkConnected(address, 1)
	overlay := &Overlay{discovery: discovery}

	now = now.Add(20 * time.Second)
	overlay.delayPeerRetry(address, false, &FrameReadError{
		MessageType: message.TypeManifests,
		WireSize:    1024,
		BytesRead:   1,
		Err:         errors.New("connection reset"),
	}, false)
	discovery.MarkDisconnected(1)

	assert.Empty(t, discovery.SelectPeersToConnect(1))
	now = now.Add(recentConnectAttempt/2 - time.Nanosecond)
	assert.Empty(t, discovery.SelectPeersToConnect(1))
	now = now.Add(time.Nanosecond)
	assert.Equal(t, []string{address}, discovery.SelectPeersToConnect(1))
}

func TestOverlayLongLivedOrdinaryManifestFailureDoesNotRestartSquelch(t *testing.T) {
	now := time.Unix(4_600, 0)
	address := "192.0.2.1:51235"
	discovery := NewDiscovery(&Config{
		MaxOutbound: 1,
		Clock:       func() time.Time { return now },
	}, nil)
	discovery.AddPeer(address, 0, 0)
	require.Equal(t, []string{address}, discovery.SelectPeersToConnect(1))
	now = now.Add(10 * time.Second)
	discovery.MarkConnected(address, 1)
	overlay := &Overlay{discovery: discovery}

	now = now.Add(2 * recentConnectAttempt)
	overlay.delayPeerRetry(address, false, &FrameReadError{
		MessageType: message.TypeManifests,
		WireSize:    1024,
		BytesRead:   1,
		Err:         errors.New("connection reset"),
	}, false)
	discovery.MarkDisconnected(1)

	assert.Equal(t, []string{address}, discovery.SelectPeersToConnect(1))
}

func TestOverlayLocalSpoolFailureDoesNotExtendBootstrapQuarantine(t *testing.T) {
	now := time.Unix(4_750, 0)
	address := "192.0.2.1:51235"
	discovery := NewDiscovery(&Config{
		MaxOutbound: 1,
		Clock:       func() time.Time { return now },
	}, nil)
	discovery.AddPeer(address, 0, 0)
	overlay := &Overlay{discovery: discovery}

	overlay.delayPeerRetry(address, true, &FrameReadError{
		MessageType: message.TypeManifests,
		WireSize:    1024,
		BytesRead:   1,
		Err: &manifestSpoolLocalError{
			operation: "spool manifest payload",
			err: &os.PathError{
				Op:   "write",
				Path: "manifest-spool",
				Err:  syscall.ETIMEDOUT,
			},
		},
	}, false)

	now = now.Add(recentConnectAttempt - time.Nanosecond)
	assert.Empty(t, discovery.selectPeersToConnect(1, true))
	now = now.Add(time.Nanosecond)
	assert.Equal(t, []string{address}, discovery.selectPeersToConnect(1, true))
}

func TestOverlayOrdinaryPeerFailuresDoNotTriggerManifestQuarantine(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		stopping bool
	}{
		{
			name: "non-frame failure",
			err:  errors.New("connection reset"),
		},
		{
			name: "partial non-manifest frame",
			err: &FrameReadError{
				MessageType: message.TypePing,
				BytesRead:   1,
				Err:         errors.New("connection reset"),
			},
		},
		{
			name: "zero-byte manifest frame",
			err: &FrameReadError{
				MessageType: message.TypeManifests,
				Err:         errors.New("connection reset"),
			},
		},
		{
			name: "shutdown",
			err: &FrameReadError{
				MessageType: message.TypeManifests,
				BytesRead:   1,
				Err:         context.Canceled,
			},
			stopping: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			address := "192.0.2.1:51235"
			discovery := NewDiscovery(&Config{MaxOutbound: 1}, nil)
			discovery.AddPeer(address, 0, 0)
			overlay := &Overlay{discovery: discovery}

			overlay.delayPeerRetry(address, false, tt.err, tt.stopping)

			assert.Equal(t, []string{address}, discovery.SelectPeersToConnect(1))
		})
	}
}

func TestPeerBootstrapAcknowledgementFiresOnce(t *testing.T) {
	called := 0
	peer := &Peer{onBootstrapReady: func() { called++ }}

	peer.acknowledgeBootstrap()
	peer.acknowledgeBootstrap()
	assert.Equal(t, 1, called)
}

func TestOverlayRejectAfterBootstrapAcknowledgementKeepsPeer(t *testing.T) {
	var governor bootstrapGovernor
	lease, ok := governor.tryReserve()
	require.True(t, ok)
	peer := &Peer{id: 1, onBootstrapReady: lease.markReady, closeCh: make(chan struct{})}
	overlay := &Overlay{peers: map[PeerID]*Peer{1: peer}}
	overlay.trackPeerBootstrap(1, lease)

	overlay.AcknowledgePeerBootstrap(1)
	overlay.RejectPeerBootstrap(1)

	select {
	case <-peer.closeCh:
		t.Fatal("rejection after bootstrap acknowledgement closed the peer")
	default:
	}
}

func TestOverlayMalformedPingDoesNotAcknowledgeBootstrap(t *testing.T) {
	var governor bootstrapGovernor
	lease, ok := governor.tryReserve()
	require.True(t, ok)
	overlay := &Overlay{peers: map[PeerID]*Peer{
		1: {id: 1, onBootstrapReady: lease.markReady},
	}}

	overlay.onMessageReceived(Event{
		PeerID:      1,
		MessageType: message.TypePing,
		Payload:     []byte("not-a-ping"),
	})

	assert.False(t, governor.isReady())
}

func TestOverlayPingDoesNotBypassSeenManifest(t *testing.T) {
	var governor bootstrapGovernor
	lease, ok := governor.tryReserve()
	require.True(t, ok)
	peer := &Peer{id: 1, onBootstrapReady: lease.markReady}
	peer.bootstrapManifest.Store(true)
	overlay := &Overlay{peers: map[PeerID]*Peer{1: peer}}
	payload, err := message.Encode(&message.Ping{PType: message.PingTypePong, Seq: 1})
	require.NoError(t, err)

	overlay.onMessageReceived(Event{
		PeerID:      1,
		MessageType: message.TypePing,
		Payload:     payload,
	})

	assert.False(t, governor.isReady())
}

func TestOverlayDroppedManifestDoesNotAcknowledgeBootstrap(t *testing.T) {
	var governor bootstrapGovernor
	lease, ok := governor.tryReserve()
	require.True(t, ok)
	peer := &Peer{id: 1, onBootstrapReady: lease.markReady, closeCh: make(chan struct{})}
	peer.bootstrapManifest.Store(true)
	messages := make(chan *InboundMessage, 1)
	messages <- &InboundMessage{}
	overlay := &Overlay{
		peers:    map[PeerID]*Peer{1: peer},
		messages: messages,
	}
	overlay.trackPeerBootstrap(1, lease)
	payload, err := message.Encode(&message.Manifests{
		List: []message.Manifest{{STObject: []byte{1}}},
	})
	require.NoError(t, err)

	overlay.onMessageReceived(Event{
		PeerID:      1,
		MessageType: message.TypeManifests,
		Payload:     payload,
	})

	assert.False(t, governor.isReady())
	select {
	case <-peer.closeCh:
	default:
		t.Fatal("dropped bootstrap manifests did not close the peer")
	}
}

func TestOverlayConnectsOutboundTargetBeforeStartupFrameCompletes(t *testing.T) {
	connections := make(chan bootstrapConnection, 2)
	first := startBootstrapTestOverlay(t)
	second := startBootstrapTestOverlay(t, WithListenAddr("[::1]:0"))
	first.SetPeerConnectCallback(func(peerID PeerID) {
		connections <- bootstrapConnection{overlay: first, peerID: peerID}
	})
	second.SetPeerConnectCallback(func(peerID PeerID) {
		connections <- bootstrapConnection{overlay: second, peerID: peerID}
	})

	client := startBootstrapTestOverlay(t,
		WithMaxOutbound(2),
		WithFixedPeers(first.ListenAddr(), second.ListenAddr()),
	)
	acknowledge := func(connected bootstrapConnection) {
		t.Helper()
		payload, err := message.Encode(&message.Manifests{
			List: []message.Manifest{{STObject: []byte{1}}},
		})
		require.NoError(t, err)
		var frame bytes.Buffer
		require.NoError(t, message.WriteMessage(&frame, message.TypeManifests, payload))
		require.NoError(t, connected.overlay.Send(connected.peerID, frame.Bytes()))
		select {
		case inbound := <-client.ManifestMessages():
			_, err := message.Decode(message.TypeManifests, inbound.Payload)
			require.NoError(t, err)
			client.AcknowledgePeerBootstrap(inbound.PeerID)
		case <-time.After(5 * time.Second):
			t.Fatal("startup manifests did not reach the protocol consumer")
		}
	}

	connected := make([]bootstrapConnection, 0, 2)
	for len(connected) < 2 {
		select {
		case peer := <-connections:
			connected = append(connected, peer)
		case <-time.After(10 * time.Second):
			t.Fatal("outbound target did not connect before startup manifests completed")
		}
	}

	assert.False(t, client.bootstrap.isReady())
	acknowledge(connected[0])
	acknowledge(connected[1])
	require.Eventually(t, client.bootstrap.isReady, 5*time.Second, 10*time.Millisecond)
}
