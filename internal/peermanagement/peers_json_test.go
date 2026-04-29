package peermanagement

import (
	"net/http"
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

// PeersJSON must round-trip the peer's reported Network-ID header
// matching rippled PeerImp::json (PeerImp.cpp:411-412): emit
// `network_id` only when the peer set the header.
func TestOverlay_PeersJSON_NetworkID(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	mk := func(pid PeerID, host string, networkID string) *Peer {
		p := NewPeer(pid, Endpoint{Host: host, Port: 51235}, false, id, nil)
		p.setState(PeerStateConnected)
		p.applyHandshakeExtras(HandshakeExtras{NetworkID: networkID})
		return p
	}

	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{
		1: mk(1, "10.0.0.1", "21337"), // testnet-style id
		2: mk(2, "10.0.0.2", ""),      // peer omitted the header
	})

	got := overlay.PeersJSON()
	require.Len(t, got, 2)

	by := map[string]map[string]any{}
	for _, e := range got {
		by[e["address"].(string)] = e
	}

	assert.Equal(t, "21337", by["10.0.0.1:51235"]["network_id"],
		"peer that sent Network-ID must round-trip via PeersJSON")
	_, hasNID := by["10.0.0.2:51235"]["network_id"]
	assert.False(t, hasNID,
		"peer without Network-ID must omit network_id (rippled's !nid.empty() gate)")
}

// Peer.NetworkID accessor surfaces the handshake-stored value, and
// HandshakeExtras carries it through ParseHandshakeExtras.
func TestPeer_NetworkID_AccessorAndHandshakeExtras(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, nil)
	assert.Equal(t, "", p.NetworkID(),
		"NetworkID defaults to empty before handshake")

	p.applyHandshakeExtras(HandshakeExtras{NetworkID: "1024"})
	assert.Equal(t, "1024", p.NetworkID())
	assert.Equal(t, "1024", p.Info().NetworkID,
		"PeerInfo.NetworkID must mirror the accessor")

	p.applyHandshakeExtras(HandshakeExtras{}) // re-handshake without header
	assert.Equal(t, "", p.NetworkID(),
		"applyHandshakeExtras must clear NetworkID when the new header is absent")
}

// ParseHandshakeExtras must round-trip the raw Network-ID header.
// rippled stores the header as-is on PeerImp::headers_ — the numeric
// validation in verifyHandshake (Handshake.cpp:241-249) lives upstream
// of extras parsing.
func TestParseHandshakeExtras_NetworkID(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		h := http.Header{}
		h.Set(HeaderNetworkID, "21338")

		extras, err := ParseHandshakeExtras(h, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "21338", extras.NetworkID)
	})

	t.Run("absent", func(t *testing.T) {
		extras, err := ParseHandshakeExtras(http.Header{}, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "", extras.NetworkID)
	})
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
