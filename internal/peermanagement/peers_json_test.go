package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PeersJSON mirrors rippled PeerImp::json (PeerImp.cpp:388-503). The
// `load` field tracks rippled's Resource::Consumer::balance(); goXRPL
// surfaces its narrower badDataBalance under the same key. Rippled emits
// `load` unconditionally even when the balance is zero
// (PeerImp.cpp:414).
func TestOverlay_PeersJSON_EmitsLoad(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	mk := func(pid PeerID, host string, inbound bool, charge int64) *Peer {
		p := NewPeer(pid, Endpoint{Host: host, Port: 51235}, inbound, id, nil)
		p.setState(PeerStateConnected)
		if charge != 0 {
			p.badDataBalance.Add(charge)
		}
		return p
	}

	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{
		1: mk(1, "10.0.0.1", false, 0),  // zero balance must still emit
		2: mk(2, "10.0.0.2", true, 250), // positive charge
		3: mk(3, "10.0.0.3", false, -7), // negative balance, post-decay
	})

	got := overlay.PeersJSON()
	require.Len(t, got, 3)

	by := map[string]map[string]any{}
	for _, e := range got {
		by[e["address"].(string)] = e
	}

	assert.Equal(t, int64(0), by["10.0.0.1:51235"]["load"],
		"rippled emits `load` unconditionally even when zero")
	assert.Equal(t, int64(250), by["10.0.0.2:51235"]["load"])
	assert.Equal(t, int64(-7), by["10.0.0.3:51235"]["load"],
		"signed JSON contract is preserved (rippled-style decay could go negative)")

	for addr, entry := range by {
		_, hasLoad := entry["load"]
		assert.True(t, hasLoad, "peer %q missing load field", addr)
	}
}

func TestPeer_Load_TracksBadDataBalance(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, nil)
	assert.Equal(t, int64(0), p.Load())

	p.IncBadData("invalid-message")
	assert.Greater(t, p.Load(), int64(0))

	// signed return type is preserved so the JSON contract still holds
	// if decay is ever rewritten to allow negatives (rippled-style)
	p.badDataBalance.Store(-3)
	assert.Equal(t, int64(-3), p.Load())
}
