package peermanagement

import (
	"testing"
	"time"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/cluster"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeClusterTestPeer wires a Peer into the overlay's peers map with a
// known remotePubKey, just enough surface for PeersJSON / Info to
// inspect. Mirrors the shape of newTestOverlayWithPeers but lets the
// caller pin both the public key and the cluster registry.
func makeClusterTestPeer(t *testing.T, id *Identity, host string, port uint16) *Peer {
	t.Helper()
	endpoint := Endpoint{Host: host, Port: port}
	tok := NewPublicKeyTokenFromBtcec(id.BtcecPublicKey())
	return &Peer{
		id:           PeerID(1),
		endpoint:     endpoint,
		remotePubKey: tok,
		state:        PeerStateConnected,
		traffic:      NewTrafficCounter(),
		score:        NewPeerScore(),
		squelchMap:   make(map[string]time.Time),
		createdAt:    time.Now(),
		closeCh:      make(chan struct{}),
	}
}

// TestPeersJSON_EmitsClusterAndName verifies the full strict-parity
// path from rippled PeerImp::json (PeerImp.cpp:399-406): a peer whose
// NodePublic key is registered in [cluster_nodes] gets `cluster: true`
// and (when the operator supplied a comment) the `name` field too.
func TestPeersJSON_EmitsClusterAndName(t *testing.T) {
	clusterID, err := NewIdentity()
	require.NoError(t, err)
	otherID, err := NewIdentity()
	require.NoError(t, err)

	clusterPub, err := addresscodec.EncodeNodePublicKey(clusterID.PublicKey())
	require.NoError(t, err)

	o := &Overlay{
		cfg:     DefaultConfig(),
		cluster: cluster.New(),
		peers:   make(map[PeerID]*Peer),
	}
	require.NoError(t, o.cluster.Load([]string{clusterPub + " primary-validator"}))

	clusterPeer := makeClusterTestPeer(t, clusterID, "192.0.2.10", 51235)
	otherPeer := makeClusterTestPeer(t, otherID, "192.0.2.11", 51236)
	otherPeer.id = PeerID(2)
	o.peers[clusterPeer.id] = clusterPeer
	o.peers[otherPeer.id] = otherPeer

	out := o.PeersJSON()
	require.Len(t, out, 2)

	var clusterEntry, otherEntry map[string]any
	for _, e := range out {
		switch e["public_key"] {
		case clusterID.EncodedPublicKey():
			clusterEntry = e
		case otherID.EncodedPublicKey():
			otherEntry = e
		}
	}
	require.NotNil(t, clusterEntry, "cluster member entry not found")
	require.NotNil(t, otherEntry, "non-member entry not found")

	assert.Equal(t, true, clusterEntry["cluster"], "cluster member must have cluster:true")
	assert.Equal(t, "primary-validator", clusterEntry["name"], "configured name must round-trip")

	assert.NotContains(t, otherEntry, "cluster",
		"non-member must not emit cluster field (PeerImp.cpp:399 conditional)")
	assert.NotContains(t, otherEntry, "name",
		"non-member must not emit name field")
}

// TestPeersJSON_ClusterMemberWithoutName covers the rippled branch at
// PeerImp.cpp:403-405 where the operator left the comment empty: we
// emit cluster:true but suppress the name field.
func TestPeersJSON_ClusterMemberWithoutName(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	pub, err := addresscodec.EncodeNodePublicKey(id.PublicKey())
	require.NoError(t, err)

	o := &Overlay{
		cfg:     DefaultConfig(),
		cluster: cluster.New(),
		peers:   make(map[PeerID]*Peer),
	}
	require.NoError(t, o.cluster.Load([]string{pub}))

	p := makeClusterTestPeer(t, id, "192.0.2.20", 51235)
	o.peers[p.id] = p

	out := o.PeersJSON()
	require.Len(t, out, 1)
	assert.Equal(t, true, out[0]["cluster"])
	assert.NotContains(t, out[0], "name",
		"empty comment must suppress the name field")
}

// TestPeersJSON_NoClusterConfigured guarantees we don't accidentally
// tag every peer when the registry is empty — the common case for
// non-cluster operators.
func TestPeersJSON_NoClusterConfigured(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	o := &Overlay{
		cfg:     DefaultConfig(),
		cluster: cluster.New(),
		peers:   make(map[PeerID]*Peer),
	}
	p := makeClusterTestPeer(t, id, "192.0.2.30", 51235)
	o.peers[p.id] = p

	out := o.PeersJSON()
	require.Len(t, out, 1)
	assert.NotContains(t, out[0], "cluster")
	assert.NotContains(t, out[0], "name")
}

// TestClusterJSON_ExcludesSelfAndShapesEntries mirrors rippled
// doPeers (Peers.cpp:62-80): the local node is omitted, named members
// emit `tag`, and unreported members omit `age`.
func TestClusterJSON_ExcludesSelfAndShapesEntries(t *testing.T) {
	selfID, err := NewIdentity()
	require.NoError(t, err)
	mateID, err := NewIdentity()
	require.NoError(t, err)

	selfPub, err := addresscodec.EncodeNodePublicKey(selfID.PublicKey())
	require.NoError(t, err)
	matePub, err := addresscodec.EncodeNodePublicKey(mateID.PublicKey())
	require.NoError(t, err)

	o := &Overlay{
		cfg:      DefaultConfig(),
		identity: selfID,
		cluster:  cluster.New(),
		peers:    make(map[PeerID]*Peer),
	}
	require.NoError(t, o.cluster.Load([]string{
		selfPub + " self-skip-me",
		matePub + " peer-mate",
	}))

	out := o.ClusterJSON()
	assert.NotContains(t, out, selfPub, "local node must be excluded from cluster output")

	mateEntry, ok := out[matePub].(map[string]any)
	require.True(t, ok, "mate entry must be present and a map")
	assert.Equal(t, "peer-mate", mateEntry["tag"], "named members emit tag")
	assert.NotContains(t, mateEntry, "age", "static entries with no report time emit no age")
	assert.NotContains(t, mateEntry, "fee", "fee is suppressed when zero")
}

// TestClusterJSON_AgeFromReportTime checks the age computation for a
// member that has been refreshed by a (hypothetical) TMCluster
// report. The clock is injected via cfg.Clock so the test is
// deterministic.
func TestClusterJSON_AgeFromReportTime(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	pub, err := addresscodec.EncodeNodePublicKey(id.PublicKey())
	require.NoError(t, err)

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.Clock = func() time.Time { return now }

	reg := cluster.New()
	require.True(t, reg.Update(id.PublicKey(), "ageful", 0, now.Add(-30*time.Second)))

	o := &Overlay{cfg: cfg, cluster: reg}
	out := o.ClusterJSON()

	entry, ok := out[pub].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 30, entry["age"], "age = now - reportTime, in seconds")
}

// TestClusterJSON_FeeFromLoadFee — rippled Peers.cpp:73-74 predicate:
// emit fee = LoadFee/ref iff LoadFee != 0 && LoadFee != ref.
func TestClusterJSON_FeeFromLoadFee(t *testing.T) {
	idVar, err := NewIdentity()
	require.NoError(t, err)
	idBase, err := NewIdentity()
	require.NoError(t, err)
	idZero, err := NewIdentity()
	require.NoError(t, err)

	pubVar, err := addresscodec.EncodeNodePublicKey(idVar.PublicKey())
	require.NoError(t, err)
	pubBase, err := addresscodec.EncodeNodePublicKey(idBase.PublicKey())
	require.NoError(t, err)
	pubZero, err := addresscodec.EncodeNodePublicKey(idZero.PublicKey())
	require.NoError(t, err)

	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := DefaultConfig()
	cfg.Clock = func() time.Time { return now }

	reg := cluster.New()
	// LoadFee=512 (≠ 256 base, ≠ 0) → emit fee = 2.0
	require.True(t, reg.Update(idVar.PublicKey(), "var", 512, now))
	// LoadFee=256 (== base) → suppress
	require.True(t, reg.Update(idBase.PublicKey(), "base", 256, now))
	// LoadFee=0 → suppress (default state today, before TMClusterReport lands)
	require.True(t, reg.Update(idZero.PublicKey(), "zero", 0, now))

	o := &Overlay{cfg: cfg, cluster: reg}
	out := o.ClusterJSON()

	varEntry, ok := out[pubVar].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, 2.0, varEntry["fee"], 1e-9, "fee = 512/256 = 2.0")

	baseEntry, ok := out[pubBase].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, baseEntry, "fee", "fee == ref must suppress emit")

	zeroEntry, ok := out[pubZero].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, zeroEntry, "fee", "fee == 0 must suppress emit")
}

// TestNew_LoadsClusterNodesFromOption pins the constructor path used
// by the production wiring in internal/consensus/adaptor/startup.go.
func TestNew_LoadsClusterNodesFromOption(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)
	pub, err := addresscodec.EncodeNodePublicKey(id.PublicKey())
	require.NoError(t, err)

	o, err := New(
		WithListenAddr("127.0.0.1:0"),
		WithMaxPeers(4),
		WithMaxInbound(2),
		WithMaxOutbound(2),
		WithClusterNodes(pub+" e2e-validator"),
	)
	require.NoError(t, err)

	require.Equal(t, 1, o.Cluster().Size())
	m, ok := o.Cluster().Member(id.PublicKey())
	require.True(t, ok)
	assert.Equal(t, "e2e-validator", m.Name)

	entry, ok := o.ClusterJSON()[pub].(map[string]any)
	require.True(t, ok, "non-self member must surface in ClusterJSON")
	assert.Equal(t, "e2e-validator", entry["tag"])
}

// TestNew_RejectsMalformedClusterNodes — rippled aborts when
// Cluster::load returns false; New must too.
func TestNew_RejectsMalformedClusterNodes(t *testing.T) {
	_, err := New(
		WithListenAddr("127.0.0.1:0"),
		WithMaxPeers(4),
		WithMaxInbound(2),
		WithMaxOutbound(2),
		WithClusterNodes("definitely-not-a-pubkey"),
	)
	require.Error(t, err, "malformed cluster_nodes must fail New")
}
