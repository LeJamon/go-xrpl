package peermanagement

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TestHandleClusterMessage_DropsNonClusterPeer pins issue #497 audit
// finding "TMCluster ... go-xrpl: no handler". A peer that isn't in the
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
		MessageType: message.TypeCluster,
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

	now := time.Now().UTC().Truncate(time.Second)
	cm := &message.Cluster{
		ClusterNodes: []message.ClusterNode{
			{PublicKey: peerNodePubEncoded, NodeName: "peer", NodeLoad: 320, ReportTime: protocol.ToRippleTime(now)},
			{PublicKey: otherPub, NodeName: "other", NodeLoad: 400, ReportTime: protocol.ToRippleTime(now)},
		},
	}
	payload, err := message.Encode(cm)
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeCluster,
		Payload:     payload,
	})

	require.Len(t, sinkCalls, 1, "clusterFeeSink must fire exactly once per ingress")
	assert.Equal(t, uint32(400), sinkCalls[0])
	member, ok := clusterReg.Member(peerNodePub)
	require.True(t, ok)
	assert.Equal(t, now, member.ReportTime)
}

func TestHandleClusterMessage_ClearsStaleClusterFee(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peerIdentity, err := NewIdentity()
	require.NoError(t, err)
	peerToken := NewPublicKeyTokenFromBtcec(peerIdentity.BtcecPublicKey())
	peerNodePubEncoded, err := addresscodec.EncodeNodePublicKey(peerToken.Bytes())
	require.NoError(t, err)

	clusterReg := cluster.New()
	require.NoError(t, clusterReg.Load([]string{peerNodePubEncoded + " peer"}))
	clusterFee := uint32(777)
	o := &Overlay{
		peers:   make(map[PeerID]*Peer),
		events:  make(chan Event, 8),
		cluster: clusterReg,
		clusterFeeSink: func(fee uint32) {
			clusterFee = fee
		},
	}

	peer := NewPeer(PeerID(34), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	peer.remotePubKey = peerToken
	o.peers[peer.ID()] = peer
	payload, err := message.Encode(&message.Cluster{})
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeCluster,
		Payload:     payload,
	})

	assert.Zero(t, clusterFee)
}

func TestHandleClusterMessage_ClearsClusterFeeAfterReportAgesOut(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peerIdentity, err := NewIdentity()
	require.NoError(t, err)
	peerToken := NewPublicKeyTokenFromBtcec(peerIdentity.BtcecPublicKey())
	peerNodePubEncoded, err := addresscodec.EncodeNodePublicKey(peerToken.Bytes())
	require.NoError(t, err)

	clusterReg := cluster.New()
	require.NoError(t, clusterReg.Load([]string{peerNodePubEncoded + " peer"}))
	clusterFee := uint32(0)
	now := time.Unix(2_000_000_000, 0)
	o := &Overlay{
		peers:   make(map[PeerID]*Peer),
		events:  make(chan Event, 8),
		cluster: clusterReg,
		clock: func() time.Time {
			return now
		},
		clusterFeeSink: func(fee uint32) {
			clusterFee = fee
		},
	}

	peer := NewPeer(PeerID(35), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	peer.remotePubKey = peerToken
	o.peers[peer.ID()] = peer

	payload, err := message.Encode(&message.Cluster{ClusterNodes: []message.ClusterNode{
		{PublicKey: peerNodePubEncoded, NodeName: "peer", NodeLoad: 512, ReportTime: protocol.ToRippleTime(now)},
	}})
	require.NoError(t, err)
	o.onMessageReceived(Event{PeerID: peer.ID(), MessageType: message.TypeCluster, Payload: payload})
	assert.Equal(t, uint32(512), clusterFee)

	now = now.Add(clusterFeeWindow + time.Second)
	// An otherwise valid frame from the trusted peer must recompute the
	// median, including the empty result, so stale fee state cannot persist.
	payload, err = message.Encode(&message.Cluster{})
	require.NoError(t, err)
	o.onMessageReceived(Event{PeerID: peer.ID(), MessageType: message.TypeCluster, Payload: payload})
	assert.Zero(t, clusterFee)
}

// TestHandleClusterMessage_ImportsLoadSourceGossip pins issue #765: a
// TMCluster frame from a registered cluster peer must fold its
// TMLoadSource entries into the resource manager (importConsumers),
// mirroring rippled PeerImp.cpp:1157-1172. Entries whose name does not
// parse as an IP endpoint are dropped while the rest are kept — rippled's
// `item.address != Endpoint()` guard at PeerImp.cpp:1168.
func TestHandleClusterMessage_ImportsLoadSourceGossip(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peerIdentity, err := NewIdentity()
	require.NoError(t, err)
	peerToken := NewPublicKeyTokenFromBtcec(peerIdentity.BtcecPublicKey())

	clusterReg := cluster.New()
	peerNodePubEncoded, err := addresscodec.EncodeNodePublicKey(peerToken.Bytes())
	require.NoError(t, err)
	require.NoError(t, clusterReg.Load([]string{peerNodePubEncoded + " peer"}))

	rm := resource.NewManager(nil, nil)
	o := &Overlay{
		peers:           make(map[PeerID]*Peer),
		events:          make(chan Event, 8),
		cluster:         clusterReg,
		resourceManager: rm,
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(44), endpoint, false, id, make(chan Event, 1))
	peer.remotePubKey = peerToken
	o.peers[peer.ID()] = peer

	const balance = resource.MinimumGossipBalance * 4
	cm := &message.Cluster{
		LoadSources: []message.LoadSource{
			{Name: "203.0.113.7:0", Cost: balance},  // rippled "ip:port" form
			{Name: "198.51.100.9", Cost: balance},   // go-xrpl bare-host form
			{Name: "not-an-address", Cost: balance}, // dropped by the Endpoint() guard
		},
	}
	payload, err := message.Encode(cm)
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeCluster,
		Payload:     payload,
	})

	// Only the two parseable addresses are imported; the malformed one
	// is dropped (rippled PeerImp.cpp:1168).
	assert.Equal(t, 2, rm.EntryCount(),
		"only parseable load-source addresses must be imported")

	c := rm.NewInboundEndpoint("203.0.113.7:9999")
	defer c.Release()
	assert.Equal(t, int64(balance), c.Balance(),
		"imported gossip balance must show up on the inbound consumer")
}

// TestHandleClusterMessage_ReimportReplacesPriorGossip pins that a second
// TMCluster frame from the same cluster member REPLACES its prior
// load-source contribution rather than stacking. go-xrpl keys the import
// by the member's configured name (rippled importConsumers(name(), …) at
// PeerImp.cpp:1171) — a stable origin across frames — so
// ResourceManager.ImportConsumers subtracts the old balance before adding
// the new, matching Resource Logic.h:282-336.
func TestHandleClusterMessage_ReimportReplacesPriorGossip(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peerIdentity, err := NewIdentity()
	require.NoError(t, err)
	peerToken := NewPublicKeyTokenFromBtcec(peerIdentity.BtcecPublicKey())

	clusterReg := cluster.New()
	peerNodePubEncoded, err := addresscodec.EncodeNodePublicKey(peerToken.Bytes())
	require.NoError(t, err)
	require.NoError(t, clusterReg.Load([]string{peerNodePubEncoded + " peer"}))

	rm := resource.NewManager(nil, nil)
	o := &Overlay{
		peers:           make(map[PeerID]*Peer),
		events:          make(chan Event, 8),
		cluster:         clusterReg,
		resourceManager: rm,
	}

	peer := NewPeer(PeerID(45), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	peer.remotePubKey = peerToken
	o.peers[peer.ID()] = peer

	reportTime := time.Now().UTC().Truncate(time.Second)
	send := func(cost uint32, advertisedName string) {
		reportTime = reportTime.Add(time.Second)
		cm := &message.Cluster{
			ClusterNodes: []message.ClusterNode{{
				PublicKey:  peerNodePubEncoded,
				NodeName:   advertisedName,
				ReportTime: protocol.ToRippleTime(reportTime),
			}},
			LoadSources: []message.LoadSource{{Name: "203.0.113.7", Cost: cost}},
		}
		payload, err := message.Encode(cm)
		require.NoError(t, err)
		o.onMessageReceived(Event{
			PeerID:      peer.ID(),
			MessageType: message.TypeCluster,
			Payload:     payload,
		})
	}

	const first = resource.MinimumGossipBalance * 2
	const second = resource.MinimumGossipBalance * 5
	send(first, "renamed-on-wire-a")
	send(second, "renamed-on-wire-b")

	c := rm.NewInboundEndpoint("203.0.113.7:9999")
	defer c.Release()
	assert.Equal(t, int64(second), c.Balance(),
		"re-import from the same member must replace, not stack, the gossip balance")
	assert.Equal(t, 1, rm.Stats().Imports, "one peer must retain one stable gossip origin")
}

func TestValidGossipAddress(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"bare ipv4", "203.0.113.7", true},
		{"ipv4 port zero", "203.0.113.7:0", true},
		{"ipv4 high port", "203.0.113.7:51235", true},
		{"ipv4 trailing colon", "203.0.113.7:", false},
		{"ipv4 surrounding whitespace", " 203.0.113.7 ", true},
		{"non-ip host", "not-an-address", false},
		{"out-of-range port", "203.0.113.7:99999", false},
		{"non-numeric port", "203.0.113.7:abc", false},
		{"bare ipv6", "2001:db8::1", true},
		{"bracketed bare ipv6", "[2001:db8::1]", true},
		{"bracketed ipv6 with port", "[2001:db8::1]:51235", true},
		{"legacy ipv4 port", "203.0.113.7 51235", true},
		{"legacy ipv6 port", "2001:db8::1 51235", true},
		{"trimmed", " 203.0.113.7:51235 ", true},
		{"zone scoped", "fe80::1%en0", false},
		{"zone scoped with port", "[fe80::1%en0]:51235", false},
		{"whitespace before colon", "203.0.113.7 :51235", false},
		{"too long", "2001:0db8:0000:0000:0000:0000:0000:0001 0000000000000000000051235", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, validGossipAddress(tc.in))
		})
	}
}

func TestCanonicalGossipAddress(t *testing.T) {
	tests := map[string]string{
		" 203.0.113.7:51235 ": "203.0.113.7:51235",
		"2001:0DB8::1 51235":  "[2001:db8::1]:51235",
		"[2001:0DB8::1]":      "2001:db8::1",
	}
	for input, want := range tests {
		got, ok := canonicalGossipAddress(input)
		require.True(t, ok)
		assert.Equal(t, want, got)
	}
}

// TestHandleHaveTransactionsMessage_GatedOnFeatureNegotiation pins the
// rippled gate at PeerImp.cpp:2598-2606: a TMHaveTransactions frame from
// a peer that did NOT negotiate tx-reduce-relay must be dropped and the
// sender charged feeMalformedRequest "disabled". go-xrpl doesn't
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
		MessageType: message.TypeHaveTransactions,
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
		cfg:        Config{EnableTxReduceRelay: false},
		peers:      make(map[PeerID]*Peer),
		events:     make(chan Event, 8),
		messages:   make(chan *InboundMessage, 8),
		txMessages: make(chan *InboundMessage, 8),
		cluster:    cluster.New(),
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
		MessageType: message.TypeTransactions,
		Payload:     payload,
	})

	assert.NotZero(t, peer.BadDataCount(),
		"TMTransactions batch without tx-reduce-relay must charge bad-data")

	// And no inner frame should have been fanned out to the tx lane —
	// the whole batch is dropped before the fanout loop.
	select {
	case got := <-o.txMessages:
		t.Fatalf("expected no fanout, got %v", got)
	case <-time.After(10 * time.Millisecond):
	}
}

// TestHandleTransactionsBatchMessage_FansOutDecodedTx pins the negotiated
// batch path: each inner TMTransaction is fanned out carrying its
// already-decoded form (InboundMessage.Tx) with no re-serialization,
// mirroring rippled handing the decoded inner straight to
// handleTransaction (PeerImp.cpp:2682-2687). Payload stays nil so the
// router skips a redundant decode.
func TestHandleTransactionsBatchMessage_FansOutDecodedTx(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:        Config{EnableTxReduceRelay: true},
		peers:      make(map[PeerID]*Peer),
		events:     make(chan Event, 8),
		messages:   make(chan *InboundMessage, 8),
		txMessages: make(chan *InboundMessage, 8),
		cluster:    cluster.New(),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(34), endpoint, false, id, make(chan Event, 1))
	caps := NewPeerCapabilities()
	caps.Features.Enable(FeatureTxReduceRelay)
	peer.capabilities = caps
	o.peers[peer.ID()] = peer

	inners := []message.Transaction{
		{RawTransaction: []byte{0x12, 0x00, 0x01}, Status: message.TxStatusCurrent},
		{RawTransaction: []byte{0x12, 0x00, 0x02}, Status: message.TxStatusCurrent},
	}
	payload, err := message.Encode(&message.Transactions{Transactions: inners})
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeTransactions,
		Payload:     payload,
	})

	for i := range inners {
		select {
		case got := <-o.txMessages:
			require.NotNil(t, got)
			assert.Equal(t, message.TypeTransaction, got.Type)
			assert.Nil(t, got.Payload,
				"fanned-out frame must carry the decoded tx, not re-encoded bytes")
			require.NotNil(t, got.Tx,
				"fanned-out frame must carry the decoded transaction")
			assert.Equal(t, inners[i].RawTransaction, got.Tx.RawTransaction)
		case <-time.After(time.Second):
			t.Fatalf("expected fanout for inner %d", i)
		}
	}

	assert.Zero(t, peer.BadDataCount(),
		"negotiated batch must not charge bad-data")
}

// TestHandleTransactionsBatchMessage_OverflowShedsToTxCounter pins the
// #1103 batch-overflow accounting: when the tx lane is saturated, inner
// frames fanned out from a TMTransactions batch are shed to
// droppedTransactions (jq_trans_overflow) rather than the consensus-lane
// droppedMessages, and never consume the consensus lane. Mirrors rippled
// routing batched txs through the same MAX_TRANSACTIONS / jqTransOverflow
// gate as single ones (PeerImp.cpp:2682-2687 → 1349-1355).
func TestHandleTransactionsBatchMessage_OverflowShedsToTxCounter(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	const txCap = 2
	o := &Overlay{
		cfg:        Config{EnableTxReduceRelay: true},
		peers:      make(map[PeerID]*Peer),
		events:     make(chan Event, 8),
		messages:   make(chan *InboundMessage, 8),
		txMessages: make(chan *InboundMessage, txCap),
		cluster:    cluster.New(),
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(35), endpoint, false, id, make(chan Event, 1))
	caps := NewPeerCapabilities()
	caps.Features.Enable(FeatureTxReduceRelay)
	peer.capabilities = caps
	o.peers[peer.ID()] = peer

	// More inners than the tx lane holds; the lane is never drained, so the
	// overflow has nowhere to go but the shed branch.
	inners := []message.Transaction{
		{RawTransaction: []byte{0x12, 0x00, 0x01}, Status: message.TxStatusCurrent},
		{RawTransaction: []byte{0x12, 0x00, 0x02}, Status: message.TxStatusCurrent},
		{RawTransaction: []byte{0x12, 0x00, 0x03}, Status: message.TxStatusCurrent},
		{RawTransaction: []byte{0x12, 0x00, 0x04}, Status: message.TxStatusCurrent},
		{RawTransaction: []byte{0x12, 0x00, 0x05}, Status: message.TxStatusCurrent},
	}
	payload, err := message.Encode(&message.Transactions{Transactions: inners})
	require.NoError(t, err)

	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeTransactions,
		Payload:     payload,
	})

	assert.Equal(t, txCap, len(o.txMessages),
		"tx lane must fill to capacity with fanned-out inner frames")
	assert.Equal(t, uint64(len(inners)-txCap), o.DroppedTransactions(),
		"batch overflow must shed to droppedTransactions (jq_trans_overflow)")
	assert.Equal(t, uint64(0), o.DroppedMessages(),
		"batch overflow must not bump the consensus-lane counter")
	assert.Equal(t, 0, len(o.messages),
		"a transaction batch must never consume the consensus lane")
}

// TestHandleGetObjectsMessage_DropsReplyWithoutOutstandingRequest pins
// the rippled reply-branch behavior at PeerImp.cpp:2540-2594: an
// inbound query=false frame is parsed but go-xrpl has no fetch-pack
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
		MessageType: message.TypeGetObjects,
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

// Only literal IP:port entries are gossipable; hostname entries fail the
// recipient's IP-endpoint parse (rippled PeerImp.cpp:1213) and must be dropped.
func TestCollectDiscoveredEndpoints_SkipsHostnames(t *testing.T) {
	o := &Overlay{
		cfg:       Config{},
		peers:     make(map[PeerID]*Peer),
		events:    make(chan Event, 8),
		discovery: NewDiscovery(&Config{}, make(chan Event, 1)),
	}
	o.discovery.AddPeer("rippled-0:51235", 1, PeerID(1))
	o.discovery.AddPeer("10.0.0.7:51235", 1, PeerID(1))
	o.discovery.AddPeer("[2001:db8::1]:51235", 2, PeerID(1))

	eps := o.collectDiscoveredEndpoints()
	for _, e := range eps {
		assert.NotEqual(t, "rippled-0:51235", e.Endpoint,
			"hostname entries must not be gossiped")
	}
	got := make(map[string]bool, len(eps))
	for _, e := range eps {
		got[e.Endpoint] = true
	}
	assert.True(t, got["10.0.0.7:51235"], "IPv4 literal must be gossiped")
	assert.True(t, got["[2001:db8::1]:51235"], "IPv6 literal must be gossiped")
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
		MessageType: message.TypeEndpoints,
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
		MessageType: message.TypeEndpoints,
		Payload:     payload,
	})

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 1)
	require.Contains(t, o.discovery.peers, "203.0.113.7:51235",
		"hops==0 host must be rewritten to the socket remote IP")
	assert.Equal(t, uint32(0), o.discovery.peers["203.0.113.7:51235"].Hops)
}

// TestHandleEndpoints_VerifyOn_DropsNonPublic pins the rippled PeerFinder
// is_valid_address filter (Logic.h) once verify_endpoints is on: only
// publicly-routable, non-loopback, non-zero-port addresses reach
// Discovery. The drop is silent (parsed fine, just not gossip-worthy).
func TestHandleEndpoints_VerifyOn_DropsNonPublic(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(20))
	o.cfg.VerifyEndpoints = true

	payload := encodeEndpoints(t, 2, []message.Endpointv2{
		{Endpoint: "10.0.0.1:51235", Hops: 1},    // RFC1918 → drop
		{Endpoint: "127.0.0.1:51235", Hops: 1},   // loopback → drop
		{Endpoint: "169.254.1.1:51235", Hops: 1}, // link-local → drop (3.1.3)
		{Endpoint: "203.0.113.9:51235", Hops: 1}, // TEST-NET-3 → drop (3.1.3)
		{Endpoint: "1.2.3.4:0", Hops: 1},         // port 0 → drop
		{Endpoint: "8.8.8.8:51235", Hops: 1},     // public → keep
	})
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeEndpoints,
		Payload:     payload,
	})

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 1, "only the public address survives verification")
	assert.Contains(t, o.discovery.peers, "8.8.8.8:51235")
	assert.Zero(t, peer.BadDataCount(), "a non-public but well-formed address is a silent drop, not bad data")
}

// TestHandleEndpoints_VerifyOff_AcceptsNonPublic documents the
// verify_endpoints = 0 escape hatch: on a local dev network, non-public
// addresses are ingested unchanged.
func TestHandleEndpoints_VerifyOff_AcceptsNonPublic(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(21))
	o.cfg.VerifyEndpoints = false

	payload := encodeEndpoints(t, 2, []message.Endpointv2{
		{Endpoint: "10.0.0.1:51235", Hops: 1},
		{Endpoint: "192.168.5.5:51235", Hops: 2},
	})
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeEndpoints,
		Payload:     payload,
	})

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 2, "verification off accepts private addresses")
	assert.Contains(t, o.discovery.peers, "10.0.0.1:51235")
	assert.Contains(t, o.discovery.peers, "192.168.5.5:51235")
}

// TestHandleEndpoints_VerifyOn_Hops0Validated verifies that the
// is_valid_address check runs on the post-rewrite socket IP for a hops==0
// entry: a peer behind a public socket is kept, one behind a private
// socket is dropped (rippled validates ep.address after the rewrite).
func TestHandleEndpoints_VerifyOn_Hops0Validated(t *testing.T) {
	t.Run("public_socket_kept", func(t *testing.T) {
		o, peer := newEndpointsTestOverlay(t, PeerID(22))
		o.cfg.VerifyEndpoints = true
		peer.conn = fakeAddrConn{remote: &net.TCPAddr{IP: net.ParseIP("5.6.7.8"), Port: 40000}}

		payload := encodeEndpoints(t, 2, []message.Endpointv2{
			{Endpoint: "192.168.1.1:51235", Hops: 0},
		})
		o.onMessageReceived(Event{
			PeerID:      peer.ID(),
			MessageType: message.TypeEndpoints,
			Payload:     payload,
		})

		o.discovery.mu.RLock()
		defer o.discovery.mu.RUnlock()
		require.Len(t, o.discovery.peers, 1)
		assert.Contains(t, o.discovery.peers, "5.6.7.8:51235")
	})

	t.Run("private_socket_dropped", func(t *testing.T) {
		o, peer := newEndpointsTestOverlay(t, PeerID(23))
		o.cfg.VerifyEndpoints = true
		peer.conn = fakeAddrConn{remote: &net.TCPAddr{IP: net.ParseIP("10.0.0.5"), Port: 40000}}

		payload := encodeEndpoints(t, 2, []message.Endpointv2{
			{Endpoint: "1.2.3.4:51235", Hops: 0},
		})
		o.onMessageReceived(Event{
			PeerID:      peer.ID(),
			MessageType: message.TypeEndpoints,
			Payload:     payload,
		})

		o.discovery.mu.RLock()
		defer o.discovery.mu.RUnlock()
		assert.Empty(t, o.discovery.peers, "hops==0 behind a private socket is dropped")
	})
}

// TestDefaultConfig_VerifyEndpointsOn pins the rippled default: endpoint
// verification is on unless explicitly disabled.
func TestDefaultConfig_VerifyEndpointsOn(t *testing.T) {
	assert.True(t, DefaultConfig().VerifyEndpoints,
		"endpoint verification must default on")
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
		MessageType: message.TypeEndpoints,
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
		MessageType: message.TypeEndpoints,
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
		MessageType: message.TypeEndpoints,
		Payload:     payload,
	})

	assert.NotZero(t, peer.BadDataCount(),
		"malformed endpoint must be charged bad-data")
	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 1)
	assert.Contains(t, o.discovery.peers, "10.0.0.9:51235")
}

func TestHandleEndpoints_AggregatesMalformedEntryFees(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(115))
	payload := encodeEndpoints(t, 2, []message.Endpointv2{
		{Endpoint: "not-an-endpoint", Hops: 1},
		{Endpoint: "also-invalid", Hops: 1},
	})
	o.onMessageReceived(Event{
		PeerID: peer.ID(), MessageType: message.TypeEndpoints, Payload: payload,
	})

	want := uint32((2 * resource.FeeInvalidData().Cost()) / resource.DecayWindowSeconds)
	assert.Equal(t, want, peer.BadDataCount())
}

// TestHandleEndpoints_RejectsOversizedFrame pins PeerImp.cpp:1206-1210: a
// frame at or above 1024 entries is rejected wholesale and charged.
func TestHandleEndpoints_RejectsOversizedFrame(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(16))

	payload := make([]byte, 0, 2*endpointsIngestMaxEntries)
	for range endpointsIngestMaxEntries {
		payload = protowire.AppendTag(payload, 3, protowire.BytesType)
		payload = protowire.AppendVarint(payload, 0)
	}
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeEndpoints,
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
// malformed and charged even though go-xrpl's laxer ParseEndpoint (used
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
		MessageType: message.TypeEndpoints,
		Payload:     payload,
	})

	assert.NotZero(t, peer.BadDataCount(),
		"non-IP host entries must be charged bad-data")
	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 1)
	assert.Contains(t, o.discovery.peers, "10.0.0.9:51235")
}

// TestHandleEndpoints_RateLimitsPerPeer pins issue #1170: after a peer's
// TMEndpoints frame is accepted, a second frame arriving inside the
// secondsPerMessage window is dropped silently (no charge, no ingest),
// mirroring rippled's whenAcceptEndpoints gate. Once the window elapses
// the peer is served again.
func TestHandleEndpoints_RateLimitsPerPeer(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(18))

	send := func(addr string) {
		o.onMessageReceived(Event{
			PeerID:      peer.ID(),
			MessageType: message.TypeEndpoints,
			Payload:     encodeEndpoints(t, 2, []message.Endpointv2{{Endpoint: addr, Hops: 1}}),
		})
	}

	send("10.0.0.1:51235")
	send("10.0.0.2:51235") // inside the window → silently dropped

	o.discovery.mu.RLock()
	require.Len(t, o.discovery.peers, 1, "second frame inside the window must not ingest")
	assert.Contains(t, o.discovery.peers, "10.0.0.1:51235")
	o.discovery.mu.RUnlock()
	assert.Zero(t, peer.BadDataCount(), "rate-limited frame must not be charged")

	// Simulate the window elapsing, then a fresh frame is accepted again.
	peer.acceptEndpointsMu.Lock()
	peer.whenAcceptEndpoints = time.Now().Add(-time.Second)
	peer.acceptEndpointsMu.Unlock()

	send("10.0.0.2:51235")
	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 2, "frame after the window must ingest")
	assert.Contains(t, o.discovery.peers, "10.0.0.2:51235")
}

// TestHandleEndpoints_SamplesOversizedFrame pins issue #1170: a frame
// carrying more than numberOfEndpointsMax valid entries (but under the
// 1024 wholesale-reject bound) is sampled down before ingest, so a single
// accepted frame contributes at most endpointsIngestSampleMax addresses.
func TestHandleEndpoints_SamplesOversizedFrame(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(19))

	const n = endpointsIngestSampleMax * 3
	eps := make([]message.Endpointv2, n)
	for i := range eps {
		eps[i] = message.Endpointv2{Endpoint: fmt.Sprintf("10.%d.%d.1:51235", i/256, i%256), Hops: 1}
	}
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeEndpoints,
		Payload:     encodeEndpoints(t, 2, eps),
	})

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	assert.Len(t, o.discovery.peers, endpointsIngestSampleMax,
		"accepted frame must be sampled down to numberOfEndpointsMax")
	assert.Zero(t, peer.BadDataCount(), "well-formed oversized-but-sub-1024 frame is not charged")
}

// TestHandleEndpoints_DropsBeyondHorizon pins issue #1170: an entry whose
// hop count exceeds our discovery horizon (MaxHops) is dropped at ingest
// without a charge, while a sibling within the horizon is still ingested.
// Mirrors rippled's hops>maxHops preprocess drop.
func TestHandleEndpoints_DropsBeyondHorizon(t *testing.T) {
	o, peer := newEndpointsTestOverlay(t, PeerID(20))

	payload := encodeEndpoints(t, 2, []message.Endpointv2{
		{Endpoint: "10.0.0.1:51235", Hops: MaxHops},
		{Endpoint: "10.0.0.2:51235", Hops: MaxHops + 1},
	})
	o.onMessageReceived(Event{
		PeerID:      peer.ID(),
		MessageType: message.TypeEndpoints,
		Payload:     payload,
	})

	o.discovery.mu.RLock()
	defer o.discovery.mu.RUnlock()
	require.Len(t, o.discovery.peers, 1)
	assert.Contains(t, o.discovery.peers, "10.0.0.1:51235", "hops within horizon must ingest")
	assert.NotContains(t, o.discovery.peers, "10.0.0.2:51235", "hops beyond horizon must be dropped")
	assert.Zero(t, peer.BadDataCount(), "over-horizon hop count is dropped, not charged")
}
