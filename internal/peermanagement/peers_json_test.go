package peermanagement

import (
	"net/http"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PeersJSON mirrors rippled PeerImp::json (PeerImp.cpp:388-503). The
// `load` field tracks rippled's Resource::Consumer::balance() — go-xrpl
// sources it from the per-peer resource.Consumer balance. Rippled
// emits `load` unconditionally even when the balance is zero
// (PeerImp.cpp:414).
func TestOverlay_PeersJSON_EmitsLoad(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	rm := resource.NewManager(nil, nil)
	mk := func(pid PeerID, host string, inbound bool, costPump int) *Peer {
		ep := Endpoint{Host: host, Port: 51235}
		p := NewPeer(pid, ep, inbound, id, nil)
		p.setState(PeerStateConnected)
		c := rm.NewInboundEndpoint(ep.String())
		p.attachUsage(c, func() {})
		if costPump != 0 {
			c.Charge(resource.NewCharge(costPump, "seed"), "test")
		}
		return p
	}

	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{
		1: mk(1, "10.0.0.1", false, 0),                                                    // zero balance must still emit
		2: mk(2, "10.0.0.2", true, resource.WarningThreshold*resource.DecayWindowSeconds), // positive charge
	})

	got := overlay.PeersJSON()
	require.Len(t, got, 2)

	by := map[string]map[string]any{}
	for _, e := range got {
		by[e["address"].(string)] = e
	}

	assert.Equal(t, int64(0), by["10.0.0.1:51235"]["load"],
		"rippled emits `load` unconditionally even when zero")
	assert.Greater(t, by["10.0.0.2:51235"]["load"].(int64), int64(0),
		"non-zero charge must surface as positive load")

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

// PeerImp.cpp:416-417 — version is sourced from the peer's User-Agent
// header (inbound) or Server header (outbound). Emit only when non-empty.
func TestPeersJSON_Version(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	t.Run("emits_user_agent_for_inbound", func(t *testing.T) {
		headers := http.Header{}
		headers.Set(HeaderUserAgent, "rippled-2.5.0")
		extras, err := ParseHandshakeExtras(headers, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "rippled-2.5.0", extras.UserAgentHeader)

		p := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, true, id, nil)
		p.applyHandshakeExtras(extras)

		o := newTestOverlayWithPeers(map[PeerID]*Peer{1: p})
		entries := o.PeersJSON()
		require.Len(t, entries, 1)
		assert.Equal(t, "rippled-2.5.0", entries[0]["version"])
	})

	t.Run("emits_server_for_outbound", func(t *testing.T) {
		// Outbound responses carry the version on the Server header
		// (BuildHandshakeResponse mirrors rippled).
		headers := http.Header{}
		headers.Set(HeaderServer, "rippled-2.6.0")
		extras, err := ParseHandshakeExtras(headers, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, "rippled-2.6.0", extras.ServerHeader)

		p := NewPeer(2, Endpoint{Host: "192.0.2.2", Port: 51235}, false, id, nil)
		p.applyHandshakeExtras(extras)

		o := newTestOverlayWithPeers(map[PeerID]*Peer{2: p})
		entries := o.PeersJSON()
		require.Len(t, entries, 1)
		assert.Equal(t, "rippled-2.6.0", entries[0]["version"])
	})

	t.Run("inbound_ignores_spurious_server_header", func(t *testing.T) {
		headers := http.Header{}
		headers.Set(HeaderUserAgent, "rippled-ua")
		headers.Set(HeaderServer, "rippled-server")
		extras, err := ParseHandshakeExtras(headers, nil, nil)
		require.NoError(t, err)

		p := NewPeer(4, Endpoint{Host: "192.0.2.4", Port: 51235}, true, id, nil)
		p.applyHandshakeExtras(extras)

		o := newTestOverlayWithPeers(map[PeerID]*Peer{4: p})
		entries := o.PeersJSON()
		require.Len(t, entries, 1)
		assert.Equal(t, "rippled-ua", entries[0]["version"],
			"inbound peer reads User-Agent regardless of Server")
	})

	t.Run("outbound_ignores_spurious_user_agent_header", func(t *testing.T) {
		headers := http.Header{}
		headers.Set(HeaderUserAgent, "rippled-ua")
		headers.Set(HeaderServer, "rippled-server")
		extras, err := ParseHandshakeExtras(headers, nil, nil)
		require.NoError(t, err)

		p := NewPeer(5, Endpoint{Host: "192.0.2.5", Port: 51235}, false, id, nil)
		p.applyHandshakeExtras(extras)

		o := newTestOverlayWithPeers(map[PeerID]*Peer{5: p})
		entries := o.PeersJSON()
		require.Len(t, entries, 1)
		assert.Equal(t, "rippled-server", entries[0]["version"],
			"outbound peer reads Server regardless of User-Agent")
	})

	t.Run("absent_when_no_version", func(t *testing.T) {
		p := NewPeer(3, Endpoint{Host: "192.0.2.3", Port: 51235}, false, id, nil)
		o := newTestOverlayWithPeers(map[PeerID]*Peer{3: p})
		entries := o.PeersJSON()
		require.Len(t, entries, 1)
		_, present := entries[0]["version"]
		assert.False(t, present, "rippled omits `version` when getVersion() is empty")
	})
}

func TestPeer_Load_TracksBadDataBalance(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	p := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, nil)
	rm := resource.NewManager(nil, nil)
	c := rm.NewInboundEndpoint(p.Endpoint().String())
	p.attachUsage(c, func() {})
	assert.Equal(t, int64(0), p.Load())

	// Pump enough cost that the normalized window value is positive.
	c.Charge(resource.NewCharge(resource.WarningThreshold*resource.DecayWindowSeconds, "seed"), "")
	assert.Greater(t, p.Load(), int64(0))
}
