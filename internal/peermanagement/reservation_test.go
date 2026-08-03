package peermanagement

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testReservationNodeID(t *testing.T, seed byte) string {
	t.Helper()
	raw := make([]byte, 33)
	raw[0] = 0x02
	for i := 1; i < len(raw); i++ {
		raw[i] = seed + byte(i)
	}
	nodeID, err := addresscodec.EncodeNodePublicKey(raw)
	require.NoError(t, err)
	return nodeID
}

func TestReservationTablePersistence(t *testing.T) {
	dir := t.TempDir()
	tbl := NewReservationTable(dir)
	nodeID := testReservationNodeID(t, 1)

	if prev, err := tbl.Insert(&PeerReservation{NodeID: nodeID, Description: "first"}); err != nil || prev != nil {
		t.Fatalf("first insert should have no previous and no error, got prev=%+v err=%v", prev, err)
	}
	if prev, err := tbl.Insert(&PeerReservation{NodeID: nodeID, Description: "second"}); err != nil || prev == nil || prev.Description != "first" {
		t.Fatalf("replace should return previous 'first' and no error, got prev=%+v err=%v", prev, err)
	}
	if !tbl.Contains(nodeID) {
		t.Fatal("Contains should be true after insert")
	}

	// A fresh table loads the persisted entry from disk.
	reloaded := NewReservationTable(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	list := reloaded.List()
	if len(list) != 1 || list[0].NodeID != nodeID || list[0].Description != "second" {
		t.Fatalf("reloaded list mismatch: %+v", list)
	}

	// Erase persists too.
	if prev, err := reloaded.Erase(nodeID); err != nil || prev == nil || prev.Description != "second" {
		t.Fatalf("erase should return previous 'second' and no error, got prev=%+v err=%v", prev, err)
	}
	final := NewReservationTable(dir)
	if err := final.Load(); err != nil {
		t.Fatalf("Load after erase: %v", err)
	}
	if len(final.List()) != 0 {
		t.Fatalf("expected empty after erase+reload, got %+v", final.List())
	}
}

func TestReservationTableCopiesCallerOwnedValues(t *testing.T) {
	tbl := NewReservationTable("")
	nodeID := testReservationNodeID(t, 2)
	input := &PeerReservation{NodeID: nodeID, Description: "original"}
	_, err := tbl.Insert(input)
	require.NoError(t, err)
	input.Description = "mutated caller"
	assert.Equal(t, "original", tbl.List()[0].Description)

	previous, err := tbl.Insert(&PeerReservation{NodeID: nodeID, Description: "replacement"})
	require.NoError(t, err)
	previous.Description = "mutated returned value"
	assert.Equal(t, "replacement", tbl.List()[0].Description)

	list := tbl.List()
	list[0].Description = "mutated list"
	assert.Equal(t, "replacement", tbl.List()[0].Description)
}

func TestReservationTableRejectsNilAndMalformedEntries(t *testing.T) {
	tbl := NewReservationTable("")
	_, err := tbl.Insert(nil)
	require.Error(t, err)
	_, err = tbl.Insert(&PeerReservation{NodeID: "  "})
	require.Error(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, DefaultReservationFile)
	wrongType := make([]byte, 33)
	wrongType[0] = 0x04
	wrongTypeEncoded, err := addresscodec.EncodeNodePublicKey(wrongType)
	require.NoError(t, err)
	for _, raw := range []string{"null", "[null]", `[{"node_id":""}]`, `[{"node_id":"nA"},{"node_id":"nA"}]`, `[{"node_id":"` + wrongTypeEncoded + `"}]`} {
		require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
		loaded := NewReservationTable(dir)
		require.Error(t, loaded.Load(), "malformed reservation payload %q must be rejected", raw)
	}
}

func TestReservationTablePostRenameErrorsKeepMemoryAligned(t *testing.T) {
	dir := t.TempDir()
	tbl := NewReservationTable(dir)
	nodeID := testReservationNodeID(t, 4)
	tbl.writeFile = func(path string, data []byte, mode os.FileMode) (bool, error) {
		committed, err := writeAtomicFile(path, data, mode)
		require.NoError(t, err)
		return committed, errors.New("directory durability uncertain")
	}

	prev, err := tbl.Insert(&PeerReservation{NodeID: nodeID, Description: "inserted"})
	assert.Nil(t, prev)
	require.EqualError(t, err, "directory durability uncertain")
	assert.True(t, tbl.Contains(nodeID), "post-rename insert must remain in memory")
	reloaded := NewReservationTable(dir)
	require.NoError(t, reloaded.Load())
	assert.Len(t, reloaded.List(), 1)

	prev, err = tbl.Erase(nodeID)
	require.NotNil(t, prev)
	require.EqualError(t, err, "directory durability uncertain")
	assert.False(t, tbl.Contains(nodeID), "post-rename erase must remain erased in memory")
	reloaded = NewReservationTable(dir)
	require.NoError(t, reloaded.Load())
	assert.Empty(t, reloaded.List())
}

func TestReservationTablePreRenameErrorsRollbackMemory(t *testing.T) {
	dir := t.TempDir()
	tbl := NewReservationTable(dir)
	nodeID := testReservationNodeID(t, 5)
	tbl.writeFile = func(string, []byte, os.FileMode) (bool, error) {
		return false, errors.New("write failed before rename")
	}

	prev, err := tbl.Insert(&PeerReservation{NodeID: nodeID, Description: "not committed"})
	assert.Nil(t, prev)
	require.EqualError(t, err, "write failed before rename")
	assert.False(t, tbl.Contains(nodeID), "pre-rename insert must roll back in memory")

	tbl.writeFile = nil
	_, err = tbl.Insert(&PeerReservation{NodeID: nodeID, Description: "committed"})
	require.NoError(t, err)
	tbl.writeFile = func(string, []byte, os.FileMode) (bool, error) {
		return false, errors.New("write failed before rename")
	}
	prev, err = tbl.Erase(nodeID)
	require.NotNil(t, prev)
	require.EqualError(t, err, "write failed before rename")
	assert.True(t, tbl.Contains(nodeID), "pre-rename erase must roll back in memory")
}

func TestReservationTableConcurrentMutationsAreSerialized(t *testing.T) {
	dir := t.TempDir()
	tbl := NewReservationTable(dir)
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = tbl.Insert(&PeerReservation{NodeID: testReservationNodeID(t, byte(i)), Description: "peer"})
		}(i)
	}
	wg.Wait()
	reloaded := NewReservationTable(dir)
	require.NoError(t, reloaded.Load())
	assert.Len(t, reloaded.List(), 24)
}

func TestDiscoveryStartSurfacesReservationLoadFailure(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, DefaultReservationFile), []byte("null"), 0o600))
	d := NewDiscovery(&Config{DataDir: dir}, make(chan Event, 1))
	err := d.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load peer reservations")
}

// TestHasInboundSlot_ReservedBypassesCap verifies the reserved/cluster bypass
// of the inbound slot limit, mirroring rippled's activate(slot, key, reserved)
// (OverlayImpl.cpp:263-267): when inbound slots are full, only cluster members
// and reserved peers are admitted beyond the cap.
func TestHasInboundSlot_ReservedBypassesCap(t *testing.T) {
	occupantID, err := NewIdentity()
	require.NoError(t, err)
	reservedID, err := NewIdentity()
	require.NoError(t, err)
	clusterID, err := NewIdentity()
	require.NoError(t, err)
	strangerID, err := NewIdentity()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.MaxInbound = 1

	tbl := NewReservationTable("")
	reservedPub, err := addresscodec.EncodeNodePublicKey(reservedID.PublicKey())
	require.NoError(t, err)
	_, err = tbl.Insert(&PeerReservation{NodeID: reservedPub, Description: "ops"})
	require.NoError(t, err)

	clusterPub, err := addresscodec.EncodeNodePublicKey(clusterID.PublicKey())
	require.NoError(t, err)
	reg := cluster.New()
	require.NoError(t, reg.Load([]string{clusterPub}))

	o := &Overlay{
		cfg:       cfg,
		peers:     make(map[PeerID]*Peer),
		cluster:   reg,
		discovery: &Discovery{reservation: tbl},
	}

	stranger := makeClusterTestPeer(t, strangerID, "192.0.2.50", 51235)
	stranger.inbound = true

	// A free slot admits anyone.
	require.True(t, o.hasInboundSlot(stranger), "slot free → admit")

	// Fill the single inbound slot.
	occupant := makeClusterTestPeer(t, occupantID, "192.0.2.51", 51235)
	occupant.inbound = true
	occupant.id = PeerID(99)
	o.peers[occupant.id] = occupant

	// Full now: a stranger is rejected, but reserved and cluster peers pass.
	require.False(t, o.hasInboundSlot(stranger), "full + not reserved/cluster → reject")

	reserved := makeClusterTestPeer(t, reservedID, "192.0.2.52", 51235)
	reserved.inbound = true
	require.True(t, o.hasInboundSlot(reserved), "full but reserved → admit")

	clusterPeer := makeClusterTestPeer(t, clusterID, "192.0.2.53", 51235)
	clusterPeer.inbound = true
	require.True(t, o.hasInboundSlot(clusterPeer), "full but cluster → admit")
}

// A table with no data directory persists nothing and never errors.
func TestReservationTableInMemory(t *testing.T) {
	tbl := NewReservationTable("")
	nodeID := testReservationNodeID(t, 3)
	if _, err := tbl.Insert(&PeerReservation{NodeID: nodeID, Description: "mem"}); err != nil {
		t.Fatalf("in-memory insert should not error, got %v", err)
	}
	if !tbl.Contains(nodeID) {
		t.Fatal("in-memory reservation should be present")
	}
	if err := tbl.Save(); err != nil {
		t.Fatalf("Save with no dir should be a no-op, got %v", err)
	}
}
