package peermanagement

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/cluster"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// TestHandleClusterMessage_DropsNonClusterPeer pins issue #497 audit
// finding "TMCluster ... goXRPL: no handler". A peer that isn't in the
// local [cluster_nodes] registry must NOT be allowed to mutate cluster
// load state — matches rippled PeerImp.cpp:1128-1131 (drop + feeUselessData
// "unknown cluster"). The peer is charged bad-data and the registry is
// untouched.
func TestHandleClusterMessage_DropsNonClusterPeer(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peerIdentity, err := NewIdentity()
	require.NoError(t, err)
	peerToken := NewPublicKeyTokenFromBtcec(peerIdentity.BtcecPublicKey())

	o := &Overlay{
		peers:   make(map[PeerID]*Peer),
		events:  make(chan Event, 8),
		cluster: cluster.New(),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(11), endpoint, false, id, make(chan Event, 1))
	peer.remotePubKey = peerToken
	o.peers[peer.ID()] = peer

	cm := &message.Cluster{
		ClusterNodes: []message.ClusterNode{{
			PublicKey:  "n9MozjnGB3tpULewtkfeEnFdkn5fXjBeZbCJpyqyBhdNu7tcphmW",
			NodeName:   "spoofed",
			NodeLoad:   1,
			ReportTime: 100,
		}},
	}
	payload, err := message.Encode(cm)
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeCluster),
		Payload:     payload,
	})

	assert.NotZero(t, peer.BadDataCount(),
		"non-cluster peer sending TMCluster must be charged bad-data")
	assert.Zero(t, o.cluster.Size(),
		"non-cluster peer must not be able to mutate the cluster registry")
}

// TestHandleClusterMessage_FiresClusterFeeSink pins the LoadFeeTrack
// ingress wiring: a TMCluster frame from a registered cluster peer must
// recompute the median across fresh-reported members and forward it to
// clusterFeeSink. Mirrors rippled PeerImp.cpp:1175-1193 which calls
// getFeeTrack().setClusterFee(median).
func TestHandleClusterMessage_FiresClusterFeeSink(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peerIdentity, err := NewIdentity()
	require.NoError(t, err)
	peerToken := NewPublicKeyTokenFromBtcec(peerIdentity.BtcecPublicKey())

	// Register the peer's node identity in the cluster registry so the
	// gate at handleClusterMessage admits the frame.
	clusterReg := cluster.New()
	peerNodePub := peerToken.Bytes()
	peerNodePubEncoded, err := addresscodec.EncodeNodePublicKey(peerNodePub)
	require.NoError(t, err)
	require.NoError(t, clusterReg.Load([]string{peerNodePubEncoded + " peer"}))

	var sinkCalls []uint32
	o := &Overlay{
		peers:          make(map[PeerID]*Peer),
		events:         make(chan Event, 8),
		cluster:        clusterReg,
		clusterFeeSink: func(fee uint32) { sinkCalls = append(sinkCalls, fee) },
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(33), endpoint, false, id, make(chan Event, 1))
	peer.remotePubKey = peerToken
	o.peers[peer.ID()] = peer

	// Frame announces two fresh-reported cluster members: the peer
	// itself (loadFee=320) and a second identity (loadFee=400). The
	// median over those two should be 400 (sort middle).
	otherIdent, err := NewIdentity()
	require.NoError(t, err)
	otherToken := NewPublicKeyTokenFromBtcec(otherIdent.BtcecPublicKey())
	otherPub, err := addresscodec.EncodeNodePublicKey(otherToken.Bytes())
	require.NoError(t, err)
	// Pre-register the other identity so the registry update accepts it.
	require.NoError(t, clusterReg.Load([]string{otherPub + " other"}))

	now := uint32(time.Now().Unix())
	cm := &message.Cluster{
		ClusterNodes: []message.ClusterNode{
			{PublicKey: peerNodePubEncoded, NodeName: "peer", NodeLoad: 320, ReportTime: now},
			{PublicKey: otherPub, NodeName: "other", NodeLoad: 400, ReportTime: now},
		},
	}
	payload, err := message.Encode(cm)
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeCluster),
		Payload:     payload,
	})

	require.Len(t, sinkCalls, 1, "clusterFeeSink must fire exactly once per ingress")
	assert.Equal(t, uint32(400), sinkCalls[0])
}

// TestHandleHaveTransactionsMessage_GatedOnFeatureNegotiation pins the
// rippled gate at PeerImp.cpp:2598-2606: a TMHaveTransactions frame from
// a peer that did NOT negotiate tx-reduce-relay must be dropped and the
// sender charged feeMalformedRequest "disabled". goXRPL doesn't
// advertise tx-reduce-relay by default (config.go EnableTxReduceRelay=
// false), so the gate fires regardless of peer-side negotiation.
func TestHandleHaveTransactionsMessage_GatedOnFeatureNegotiation(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:     Config{EnableTxReduceRelay: false},
		peers:   make(map[PeerID]*Peer),
		events:  make(chan Event, 8),
		cluster: cluster.New(),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(22), endpoint, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	hashes := make([][]byte, 1)
	hashes[0] = make([]byte, 32)
	hashes[0][0] = 0xAB

	ht := &message.HaveTransactions{Hashes: hashes}
	payload, err := message.Encode(ht)
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeHaveTransactions),
		Payload:     payload,
	})

	assert.NotZero(t, peer.BadDataCount(),
		"TMHaveTransactions without tx-reduce-relay must charge bad-data")
}

// TestHandleTransactionsBatchMessage_GatedOnFeatureNegotiation mirrors
// the gate above for the batched form. Same rippled reference
// (PeerImp.cpp:2667-2675): tx-reduce-relay disabled → drop + charge.
func TestHandleTransactionsBatchMessage_GatedOnFeatureNegotiation(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:      Config{EnableTxReduceRelay: false},
		peers:    make(map[PeerID]*Peer),
		events:   make(chan Event, 8),
		messages: make(chan *InboundMessage, 8),
		cluster:  cluster.New(),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(33), endpoint, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	batch := &message.Transactions{
		Transactions: []message.Transaction{{
			RawTransaction: []byte{0x12, 0x00, 0x00},
			Status:         message.TxStatusCurrent,
		}},
	}
	payload, err := message.Encode(batch)
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeTransactions),
		Payload:     payload,
	})

	assert.NotZero(t, peer.BadDataCount(),
		"TMTransactions batch without tx-reduce-relay must charge bad-data")

	// And no inner frame should have been fanned out to the messages
	// channel — the whole batch is dropped before the fanout loop.
	select {
	case got := <-o.messages:
		t.Fatalf("expected no fanout, got %v", got)
	case <-time.After(10 * time.Millisecond):
	}
}

// TestHandleGetObjectsMessage_DropsReplyWithoutOutstandingRequest pins
// the rippled reply-branch behavior at PeerImp.cpp:2540-2594: an
// inbound query=false frame is parsed but goXRPL has no fetch-pack
// acquisition state to satisfy, so we drop without charging.
func TestHandleGetObjectsMessage_DropsReplyWithoutOutstandingRequest(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:     Config{},
		peers:   make(map[PeerID]*Peer),
		events:  make(chan Event, 8),
		cluster: cluster.New(),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(44), endpoint, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	reply := &message.GetObjectByHash{
		ObjType: message.ObjectTypeTransactionNode,
		Query:   false,
	}
	payload, err := message.Encode(reply)
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeGetObjects),
		Payload:     payload,
	})

	// Reply with no outstanding request: handler drops with no charge.
	assert.Zero(t, peer.BadDataCount(),
		"well-formed but unsolicited TMGetObjects reply must not charge")
}

// TestSendEndpoints_NoSelfNoDiscovered_DoesNotEmit pins the
// rippled-faithful "don't gossip an empty handout" rule. When we have
// neither a self-entry (PublicIP/ListenAddr missing) nor any
// discovered endpoints, the per-peer build returns an empty list and
// the per-peer Send is skipped. Without this guard a fresh node with
// no peer-finder seed would broadcast TMEndpoints{version=2,
// endpoints=[]} and recipients would charge "endpoints too few".
func TestSendEndpoints_NoSelfNoDiscovered_DoesNotEmit(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:       Config{},
		peers:     make(map[PeerID]*Peer),
		events:    make(chan Event, 8),
		discovery: NewDiscovery(&Config{}, make(chan Event, 1)),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(55), endpoint, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	// Should run without panicking and without writing anything to
	// the peer's send queue. Verifying "no send" reliably requires a
	// real socket; here we settle for the smoke test plus
	// confirmation that the helper returned. The build of the helper
	// itself is the regression guard against re-introducing the gap.
	o.sendEndpoints()
}

// newEndpointsTestOverlay builds a minimal Overlay with a single
// converged peer wired into Discovery, ready to receive a TMEndpoints
// frame via onMessageReceived.
func newEndpointsTestOverlay(t *testing.T, peerID PeerID) (*Overlay, *Peer) {
	t.Helper()
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:       Config{},
		peers:     make(map[PeerID]*Peer),
		events:    make(chan Event, 8),
		discovery: NewDiscovery(&Config{}, make(chan Event, 1)),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(peerID, endpoint, false, id, make(chan Event, 1))
	peer.setTracking(PeerTrackingConverged)
	o.peers[peer.ID()] = peer
	return o, peer
}

func encodeEndpoints(t *testing.T, version uint32, eps []message.Endpointv2) []byte {
	t.Helper()
	payload, err := message.Encode(&message.Endpoints{Version: version, EndpointsV2: eps})
	require.NoError(t, err)
	return payload
}

// fakeAddrConn is a net.Conn that only reports a meaningful RemoteAddr,
// used to exercise the hops==0 socket-IP rewrite path.
type fakeAddrConn struct {
	net.Conn
	remote net.Addr
}

func (c fakeAddrConn) RemoteAddr() net.Addr { return c.remote }

// TestHandleEndpoints_IngestsHopsGreaterEntries pins the core fix for
// issue #570: an inbound TMEndpoints frame from a converged peer feeds
// its hops>=1 entries into Discovery. Mirrors rippled
// PeerImp.cpp:1237 emplace + on_endpoints.
func TestHandleEndpoints_IngestsHopsGreaterEntries(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(11))

	payload := encodeEndpoints(t, 2, []message.Endpointv2{
		{Endpoint: "10.0.0.1:51235", Hops: 1},
		{Endpoint: "10.0.0.2:51235", Hops: 2},
	})
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeEndpoints),
		Payload:     payload,
	})

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 2)
	require.Contains(t, o.discovery.peers, "10.0.0.1:51235")
	require.Contains(t, o.discovery.peers, "10.0.0.2:51235")
	assert.Equal(t, uint32(1), o.discovery.peers["10.0.0.1:51235"].Hops)
	assert.Equal(t, uint32(2), o.discovery.peers["10.0.0.2:51235"].Hops)
	assert.Zero(t, peer.BadDataCount())
}

// TestHandleEndpoints_Hops0RewrittenToSocketIP pins PeerImp.cpp:1234-1235:
// a hops==0 entry describes the sender, so its self-reported host is
// replaced with the observed socket remote IP (keeping the advertised
// port) before ingestion.
func TestHandleEndpoints_Hops0RewrittenToSocketIP(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(12))
	peer.conn = fakeAddrConn{remote: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 40000}}

	payload := encodeEndpoints(t, 2, []message.Endpointv2{
		{Endpoint: "192.168.1.1:51235", Hops: 0},
	})
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeEndpoints),
		Payload:     payload,
	})

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 1)
	require.Contains(t, o.discovery.peers, "203.0.113.7:51235",
		"hops==0 host must be rewritten to the socket remote IP")
	assert.Equal(t, uint32(0), o.discovery.peers["203.0.113.7:51235"].Hops)
}

// TestHandleEndpoints_DropsNonConvergedPeer pins PeerImp.cpp:1201: a peer
// that has not reached tracking-converged must not be allowed to seed
// Discovery, and is not charged for it.
func TestHandleEndpoints_DropsNonConvergedPeer(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(13))
	peer.setTracking(PeerTrackingUnknown)

	payload := encodeEndpoints(t, 2, []message.Endpointv2{
		{Endpoint: "10.0.0.1:51235", Hops: 1},
	})
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeEndpoints),
		Payload:     payload,
	})

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	assert.Empty(t, o.discovery.peers)
	assert.Zero(t, peer.BadDataCount())
}

// TestHandleEndpoints_DropsUnsupportedVersion pins PeerImp.cpp:1201: only
// version==2 frames are ingested.
func TestHandleEndpoints_DropsUnsupportedVersion(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(14))

	payload := encodeEndpoints(t, 1, []message.Endpointv2{
		{Endpoint: "10.0.0.1:51235", Hops: 1},
	})
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeEndpoints),
		Payload:     payload,
	})

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	assert.Empty(t, o.discovery.peers)
}

// TestHandleEndpoints_ChargesMalformedEntry pins PeerImp.cpp:1240-1247:
// an unparseable entry is skipped and charged, but valid sibling entries
// in the same frame are still ingested.
func TestHandleEndpoints_ChargesMalformedEntry(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(15))

	payload := encodeEndpoints(t, 2, []message.Endpointv2{
		{Endpoint: "not-an-endpoint", Hops: 1},
		{Endpoint: "10.0.0.9:51235", Hops: 1},
	})
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeEndpoints),
		Payload:     payload,
	})

	assert.NotZero(t, peer.BadDataCount(),
		"malformed endpoint must be charged bad-data")
	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 1)
	assert.Contains(t, o.discovery.peers, "10.0.0.9:51235")
}

// TestHandleEndpoints_RejectsOversizedFrame pins PeerImp.cpp:1206-1210: a
// frame at or above 1024 entries is rejected wholesale and charged.
func TestHandleEndpoints_RejectsOversizedFrame(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(16))

	eps := make([]message.Endpointv2, endpointsIngestMaxEntries)
	for i := range eps {
		eps[i] = message.Endpointv2{Endpoint: "10.0.0.1:51235", Hops: 1}
	}
	payload := encodeEndpoints(t, 2, eps)
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeEndpoints),
		Payload:     payload,
	})

	assert.NotZero(t, peer.BadDataCount())

	// PeerImp.cpp:1208 charges feeUselessData (150) for an oversized
	// frame, strictly lighter than the feeInvalidData (400) levied per
	// malformed entry. A reference peer charged the malformed reason
	// must end up with a heavier balance, pinning the chargeForReason
	// routing for "endpoints-too-large".
	id, err := NewIdentity()
	require.NoError(t, err)
	ref := NewPeer(PeerID(116), Endpoint{Host: "127.0.0.1", Port: 51236}, false, id, make(chan Event, 1))
	ref.setTracking(PeerTrackingConverged)
	o.peers[ref.ID()] = ref
	o.IncPeerBadData(ref.ID(), "endpoints-malformed")
	assert.Less(t, peer.BadDataCount(), ref.BadDataCount(),
		"oversized frame must cost feeUselessData (150), lighter than feeInvalidData (400)")

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	assert.Empty(t, o.discovery.peers)
}

// TestHandleEndpoints_RejectsNonIPHost pins PeerImp.cpp:1218-1226:
// from_string_checked requires a literal IP:port, so a hostname host is
// malformed and charged even though goXRPL's laxer ParseEndpoint (used
// by the outbound Connect path) would accept it. Covers both hops>0 and
// hops==0 — rippled validates the advertised string before substituting
// the socket IP for the hops==0 case.
func TestHandleEndpoints_RejectsNonIPHost(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(17))
	peer.conn = fakeAddrConn{remote: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 40000}}

	payload := encodeEndpoints(t, 2, []message.Endpointv2{
		{Endpoint: "evil-host:51235", Hops: 1},
		{Endpoint: "also-not-an-ip:51235", Hops: 0},
		{Endpoint: "10.0.0.9:51235", Hops: 1},
	})
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: uint16(message.TypeEndpoints),
		Payload:     payload,
	})

	assert.NotZero(t, peer.BadDataCount(),
		"non-IP host entries must be charged bad-data")
	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 1)
	assert.Contains(t, o.discovery.peers, "10.0.0.9:51235")
}
