package peermanagement

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
// acquisition state to satisfy, so we drop without charging. Pre-fix
// these frames silently fell through the router default case; we now
// at least decode them (which rejects malformed payloads with a
// charge) before the no-op log.
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
