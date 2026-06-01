package peermanagement

import (
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/stretchr/testify/require"
)

func TestReservationTablePersistence(t *testing.T) {
	dir := t.TempDir()
	tbl := NewReservationTable(dir)

	if prev, err := tbl.Insert(&PeerReservation{NodeID: "nABC", Description: "first"}); err != nil || prev != nil {
		t.Fatalf("first insert should have no previous and no error, got prev=%+v err=%v", prev, err)
	}
	if prev, err := tbl.Insert(&PeerReservation{NodeID: "nABC", Description: "second"}); err != nil || prev == nil || prev.Description != "first" {
		t.Fatalf("replace should return previous 'first' and no error, got prev=%+v err=%v", prev, err)
	}
	if !tbl.Contains("nABC") {
		t.Fatal("Contains should be true after insert")
	}

	// A fresh table loads the persisted entry from disk.
	reloaded := NewReservationTable(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	list := reloaded.List()
	if len(list) != 1 || list[0].NodeID != "nABC" || list[0].Description != "second" {
		t.Fatalf("reloaded list mismatch: %+v", list)
	}

	// Erase persists too.
	if prev, err := reloaded.Erase("nABC"); err != nil || prev == nil || prev.Description != "second" {
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
	if _, err := tbl.Insert(&PeerReservation{NodeID: "nXYZ", Description: "mem"}); err != nil {
		t.Fatalf("in-memory insert should not error, got %v", err)
	}
	if !tbl.Contains("nXYZ") {
		t.Fatal("in-memory reservation should be present")
	}
	if err := tbl.Save(); err != nil {
		t.Fatalf("Save with no dir should be a no-op, got %v", err)
	}
}
