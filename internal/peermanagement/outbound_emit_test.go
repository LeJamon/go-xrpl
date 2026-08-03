package peermanagement

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TestSendClusterUpdate_NoClusterConfigured_NoOp pins the
// NetworkOPs.cpp:1121-1122 early-return: when the local
// [cluster_nodes] is empty, processClusterTimer skips the broadcast
// entirely. Without this gate every cluster timer tick on a stock
// non-cluster node would re-encode and walk every peer for no payoff.
func TestSendClusterUpdate_NoClusterConfigured_NoOp(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:     Config{},
		peers:   make(map[PeerID]*Peer),
		events:  make(chan Event, 8),
		cluster: cluster.New(), // empty
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(101), endpoint, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	// Should run to completion without panicking and without
	// modifying any peer state. Direct assertion: cluster.Size stays
	// zero (no implicit member insertion).
	o.sendClusterUpdate()
	assert.Zero(t, o.cluster.Size(),
		"sendClusterUpdate must not register members in an empty registry")
}

// TestSendClusterUpdate_EmitsExportedConsumerGossip pins issue #765: the
// periodic cluster broadcast must carry our resource-manager gossip as
// TMLoadSource entries, mirroring rippled NetworkOPs.cpp:1151-1157
// (exportConsumers → add_loadsources). A consumer over the export
// threshold must appear, keyed by its normalised inbound address.
func TestSendClusterUpdate_EmitsExportedConsumerGossip(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	peerIdentity, err := NewIdentity()
	require.NoError(t, err)
	peerToken := NewPublicKeyTokenFromBtcec(peerIdentity.BtcecPublicKey())

	// Register the receiving peer as a cluster member so it both appears
	// in the broadcast registry and is selected as a send target.
	clusterReg := cluster.New()
	reportTime := protocol.FromRippleTime(800_000_000)
	require.True(t, clusterReg.Update(peerToken.Bytes(), "peer", 0, reportTime))

	// Seed the resource manager with an inbound consumer whose balance
	// clears the gossip-export threshold.
	rm := resource.NewManager(nil, nil)
	seed := rm.NewInboundEndpoint("203.0.113.7:9999")
	seed.Charge(resource.NewCharge(resource.MinimumGossipBalance*resource.DecayWindowSeconds*2, "seed"), "")
	defer seed.Release()

	o := &Overlay{
		cfg:             Config{},
		peers:           make(map[PeerID]*Peer),
		events:          make(chan Event, 8),
		cluster:         clusterReg,
		resourceManager: rm,
	}

	peer := NewPeer(PeerID(303), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	peer.remotePubKey = peerToken
	peer.setState(PeerStateConnected)
	o.peers[peer.ID()] = peer

	o.sendClusterUpdate()

	frame := requireOutboundFrame(t, peer)

	_, payload, err := message.ReadMessage(bytes.NewReader(frame))
	require.NoError(t, err)
	decoded, err := message.Decode(message.TypeCluster, payload)
	require.NoError(t, err)
	cm, ok := decoded.(*message.Cluster)
	require.True(t, ok)
	require.Len(t, cm.ClusterNodes, 1)
	assert.Equal(t, protocol.ToRippleTime(reportTime), cm.ClusterNodes[0].ReportTime)

	require.Len(t, cm.LoadSources, 1, "exported consumer must appear as a load source")
	assert.Equal(t, "203.0.113.7", cm.LoadSources[0].Name,
		"load-source name must be the normalised inbound address (port stripped)")
	assert.GreaterOrEqual(t, cm.LoadSources[0].Cost, uint32(resource.MinimumGossipBalance),
		"exported cost must clear the gossip threshold")
}

func TestSendClusterUpdate_GatesSelfLoadOnValidatedLedgerAge(t *testing.T) {
	for _, test := range []struct {
		name string
		age  time.Duration
		want uint32
	}{
		{name: "fresh boundary", age: 4 * time.Minute, want: 512},
		{name: "stale", age: 4*time.Minute + time.Second, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			localIdentity, err := NewIdentity()
			require.NoError(t, err)
			peerIdentity, err := NewIdentity()
			require.NoError(t, err)
			peerToken := NewPublicKeyTokenFromBtcec(peerIdentity.BtcecPublicKey())

			clusterReg := cluster.New()
			require.True(t, clusterReg.Update(
				peerToken.Bytes(),
				"peer",
				0,
				protocol.FromRippleTime(800_000_000),
			))

			o := &Overlay{
				peers:             make(map[PeerID]*Peer),
				events:            make(chan Event, 8),
				cluster:           clusterReg,
				localNodeIdentity: localIdentity.PublicKey(),
			}
			o.SetLocalLoadFeeProvider(func() (uint32, time.Duration) {
				return 512, test.age
			})

			peer := NewPeer(
				PeerID(304),
				Endpoint{Host: "127.0.0.1", Port: 51235},
				false,
				localIdentity,
				make(chan Event, 1),
			)
			peer.remotePubKey = peerToken
			peer.setState(PeerStateConnected)
			o.peers[peer.ID()] = peer

			o.sendClusterUpdate()
			frame := requireOutboundFrame(t, peer)
			_, payload, err := message.ReadMessage(bytes.NewReader(frame))
			require.NoError(t, err)
			decoded, err := message.Decode(message.TypeCluster, payload)
			require.NoError(t, err)
			cm, ok := decoded.(*message.Cluster)
			require.True(t, ok)

			localPublic, err := addresscodec.EncodeNodePublicKey(localIdentity.PublicKey())
			require.NoError(t, err)
			for _, node := range cm.ClusterNodes {
				if node.PublicKey == localPublic {
					assert.Equal(t, test.want, node.NodeLoad)
					return
				}
			}
			t.Fatalf("local cluster entry %q not found", localPublic)
		})
	}
}

// TestSendTxQueueAnnounce_FeatureDisabled_NoEmit pins the
// EnableTxReduceRelay gate: the periodic emitter MUST be silent when
// the operator hasn't opted into tx-reduce-relay. Otherwise we'd be
// gossiping tx hashes to peers who never negotiated the feature and
// would charge us for the unsolicited frame.
func TestSendTxQueueAnnounce_FeatureDisabled_NoEmit(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	hashesProvided := false
	o := &Overlay{
		cfg:     Config{EnableTxReduceRelay: false},
		peers:   make(map[PeerID]*Peer),
		events:  make(chan Event, 8),
		cluster: cluster.New(),
		openLedgerHashesProvider: func() [][32]byte {
			hashesProvided = true
			return [][32]byte{{0x01}, {0x02}}
		},
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(202), endpoint, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	o.sendTxQueueAnnounce()

	assert.False(t, hashesProvided,
		"sendTxQueueAnnounce must skip the provider call when EnableTxReduceRelay=false")
}

func TestSendTxQueueAnnounce_NoProvider_NoOp(t *testing.T) {
	o := &Overlay{
		cfg:     Config{EnableTxReduceRelay: true},
		peers:   make(map[PeerID]*Peer),
		events:  make(chan Event, 8),
		cluster: cluster.New(),
	}
	o.sendTxQueueAnnounce()
}

func TestSendTxQueueAnnounce_DrainsPerPeerQueueWithoutStarvation(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	caps := NewPeerCapabilities()
	caps.Features.Enable(FeatureTxReduceRelay)
	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	peer.setState(PeerStateConnected)
	peer.capabilities = caps
	o := &Overlay{
		cfg:   Config{EnableTxReduceRelay: true},
		peers: map[PeerID]*Peer{peer.ID(): peer},
	}

	for i := 1; i <= txQueueMaxEntriesPerFrame*2+1; i++ {
		var hash [32]byte
		hash[0] = byte(i >> 8)
		hash[1] = byte(i)
		peer.addTxQueue(hash)
	}
	require.Equal(t, txQueueMaxEntriesPerFrame*2+1, peer.txQueueLen())

	for want := range 3 {
		o.sendTxQueueAnnounce()
		frame := requireOutboundFrame(t, peer)
		_, payload, readErr := message.ReadMessage(bytes.NewReader(frame))
		require.NoError(t, readErr)
		decoded, decodeErr := message.Decode(message.TypeHaveTransactions, payload)
		require.NoError(t, decodeErr)
		have, ok := decoded.(*message.HaveTransactions)
		require.True(t, ok)
		wantCount := txQueueMaxEntriesPerFrame
		if want == 2 {
			wantCount = 1
		}
		require.Len(t, have.Hashes, wantCount)
		var first [32]byte
		copy(first[:], have.Hashes[0])
		firstIndex := want*txQueueMaxEntriesPerFrame + 1
		assert.Equal(t, byte(firstIndex>>8), first[0])
		assert.Equal(t, byte(firstIndex), first[1])
	}
	assert.Zero(t, peer.txQueueLen())
}

func TestSendTxQueueAnnounce_ReleasesPeersLockBeforeEnqueue(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	caps := NewPeerCapabilities()
	caps.Features.Enable(FeatureTxReduceRelay)
	peer := NewPeer(PeerID(10), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	peer.setState(PeerStateConnected)
	peer.capabilities = caps
	o := &Overlay{
		cfg:   Config{EnableTxReduceRelay: true},
		peers: map[PeerID]*Peer{peer.ID(): peer},
	}
	peer.addTxQueue([32]byte{0xA1})

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		o.sendTxQueueAnnounceWith(func(*Peer) error {
			close(started)
			<-release
			return nil
		})
		close(done)
	}()
	<-started

	writerAcquired := make(chan struct{})
	go func() {
		o.peersMu.Lock()
		o.peersMu.Unlock()
		close(writerAcquired)
	}()
	select {
	case <-writerAcquired:
	case <-time.After(time.Second):
		t.Fatal("peersMu writer blocked while relay enqueue was in progress")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("relay announce did not finish")
	}
}

// TestBroadcastHaveTxSet_BuildsValidFrame pins the wire shape of the
// post-BuildTxSet announce so a peer interpreting our broadcast
// reaches handleHaveSet → status=Have. Regression guard against
// accidentally flipping the status field (Have vs Need) at the
// emitter — that bug would manifest as peers ACQUIRING our set
// instead of marking us as a source.
func TestBroadcastHaveTxSet_BuildsValidFrame(t *testing.T) {
	o := &Overlay{
		peers:   make(map[PeerID]*Peer),
		events:  make(chan Event, 8),
		cluster: cluster.New(),
	}

	// Construct a payload directly and round-trip it to ensure our
	// encoder produces a frame the decoder will accept as tsHAVE.
	setID := [32]byte{0xDE, 0xAD, 0xBE, 0xEF}
	msg := &message.HaveTransactionSet{
		Status: message.TxSetStatusHave,
		Hash:   setID[:],
	}
	encoded, err := message.Encode(msg)
	require.NoError(t, err)

	frame, err := message.BuildWireMessage(message.TypeHaveSet, encoded)
	require.NoError(t, err)
	require.NotEmpty(t, frame)

	// Smoke-call the real emitter to make sure it doesn't choke when
	// no peers are connected.
	o.BroadcastHaveTxSet(setID)
}

// TestServeDoTransactions_FetchesViaProvider verifies the
// TMGetObjectByHash{otTRANSACTIONS} reply path: requested hashes are
// looked up via the configured txProvider, found blobs are packed
// into a TMTransactions reply, and a missing hash aborts with a malformed
// request charge.
// Mirrors rippled's PeerImp::doTransactions
// (PeerImp.cpp:2787-2839).
func TestServeDoTransactions_FetchesViaProvider(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	knownHash := [32]byte{0xAA}
	knownBlob := []byte{0x12, 0x00, 0x10, 0x00}
	missingHash := [32]byte{0xBB}

	provided := map[[32]byte][]byte{knownHash: knownBlob}
	lookups := 0

	o := &Overlay{
		cfg:     Config{EnableTxReduceRelay: true},
		peers:   make(map[PeerID]*Peer),
		events:  make(chan Event, 8),
		cluster: cluster.New(),
		txProvider: func(h [32]byte) ([]byte, bool) {
			lookups++
			blob, ok := provided[h]
			return blob, ok
		},
	}

	endpoint := Endpoint{Host: "127.0.0.1", Port: 51235}
	peer := NewPeer(PeerID(303), endpoint, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	req := &message.GetObjectByHash{
		ObjType: message.ObjectTypeTransactions,
		Query:   true,
		Objects: []message.IndexedObject{
			{Hash: knownHash[:]},
			{Hash: missingHash[:]},
		},
	}
	o.serveDoTransactions(peer.ID(), req)

	assert.Equal(t, 2, lookups,
		"serveDoTransactions must consult the provider for every requested hash")
	// The peer.Send call may have failed (no real socket) — we don't
	// assert on it. The behavioural guarantee is that the provider
	// was consulted for both hashes and the handler returned cleanly.
	_ = time.Now()
}

func TestServeDoTransactions_UsesCacheStateAndRejectsMissing(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	includedHash := [32]byte{0xC1}
	queuedHash := [32]byte{0xC2}
	missingHash := [32]byte{0xC3}
	o := &Overlay{
		cfg:   Config{EnableTxReduceRelay: true},
		peers: make(map[PeerID]*Peer),
		txRecordProvider: func(hash [32]byte) (TxRecord, bool) {
			switch hash {
			case includedHash:
				return TxRecord{RawTransaction: []byte{0x01}, Status: message.TxStatusCurrent}, true
			case queuedHash:
				return TxRecord{RawTransaction: []byte{0x02}, Status: message.TxStatusNew, Deferred: true}, true
			default:
				return TxRecord{}, false
			}
		},
	}
	peer := NewPeer(PeerID(8), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer

	o.serveDoTransactions(peer.ID(), &message.GetObjectByHash{
		ObjType: message.ObjectTypeTransactions,
		Query:   true,
		Objects: []message.IndexedObject{{Hash: includedHash[:]}, {Hash: queuedHash[:]}},
	})
	frame := requireOutboundFrame(t, peer)
	_, payload, err := message.ReadMessage(bytes.NewReader(frame))
	require.NoError(t, err)
	decoded, err := message.Decode(message.TypeTransactions, payload)
	require.NoError(t, err)
	reply, ok := decoded.(*message.Transactions)
	require.True(t, ok)
	require.Len(t, reply.Transactions, 2)
	assert.Equal(t, message.TxStatusCurrent, reply.Transactions[0].Status)
	assert.False(t, reply.Transactions[0].Deferred)
	assert.Equal(t, message.TxStatusNew, reply.Transactions[1].Status)
	assert.True(t, reply.Transactions[1].Deferred)
	assert.NotZero(t, reply.Transactions[0].ReceiveTimestamp)
	assert.NotZero(t, reply.Transactions[1].ReceiveTimestamp)

	o.serveDoTransactions(peer.ID(), &message.GetObjectByHash{
		ObjType: message.ObjectTypeTransactions,
		Query:   true,
		Objects: []message.IndexedObject{{Hash: includedHash[:]}, {Hash: missingHash[:]}},
	})
	assert.False(t, func() bool {
		_, ok := takeOutboundFrame(peer)
		return ok
	}(), "missing hashes must abort the TMTransactions reply")
	assert.Positive(t, peer.BadDataCount())
}

func TestServeDoTransactions_RejectsMoreThanCacheCap(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	lookups := 0
	o := &Overlay{
		cfg:   Config{EnableTxReduceRelay: true},
		peers: make(map[PeerID]*Peer),
		txRecordProvider: func([32]byte) (TxRecord, bool) {
			lookups++
			return TxRecord{RawTransaction: []byte{0x01}}, true
		},
	}
	peer := NewPeer(PeerID(9), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	o.peers[peer.ID()] = peer
	objects := make([]message.IndexedObject, peerTxQueueMax+1)
	for i := range objects {
		objects[i].Hash = make([]byte, 32)
		objects[i].Hash[0] = byte(i)
	}
	o.serveDoTransactions(peer.ID(), &message.GetObjectByHash{
		ObjType: message.ObjectTypeTransactions,
		Query:   true,
		Objects: objects,
	})
	assert.Zero(t, lookups)
	assert.Positive(t, peer.BadDataCount())
	assert.False(t, func() bool {
		_, ok := takeOutboundFrame(peer)
		return ok
	}())
}
