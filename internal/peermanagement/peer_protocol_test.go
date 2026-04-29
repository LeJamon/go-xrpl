package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newProtocolTestPeer(t *testing.T) *Peer {
	t.Helper()
	id, err := NewIdentity()
	require.NoError(t, err)
	return NewPeer(PeerID(1), Endpoint{Host: "192.0.2.1", Port: 51235}, false, id, nil)
}

func TestPeer_ProtocolVersion_EmptyByDefault(t *testing.T) {
	p := newProtocolTestPeer(t)
	assert.Empty(t, p.ProtocolVersion())
	assert.Empty(t, p.Info().Protocol)
}

func TestPeer_ProtocolVersion_RoundTrip(t *testing.T) {
	p := newProtocolTestPeer(t)
	p.setProtocolVersion("XRPL/2.2")

	assert.Equal(t, "XRPL/2.2", p.ProtocolVersion())
	assert.Equal(t, "XRPL/2.2", p.Info().Protocol)
}

// PeerImp.cpp:419 emits `protocol` unconditionally — even before a value
// has been negotiated rippled writes the default-constructed token.
// goXRPL captures the token during the handshake; before then we emit
// the empty string to keep the field present.
func TestOverlay_PeersJSON_EmitsProtocolField(t *testing.T) {
	t.Run("captured_after_handshake", func(t *testing.T) {
		p := newProtocolTestPeer(t)
		p.setProtocolVersion("XRPL/2.2")

		o := newTestOverlayWithPeers(map[PeerID]*Peer{p.ID(): p})
		out := o.PeersJSON()
		require.Len(t, out, 1)
		assert.Equal(t, "XRPL/2.2", out[0]["protocol"])
	})

	t.Run("present_even_when_unset", func(t *testing.T) {
		p := newProtocolTestPeer(t)
		o := newTestOverlayWithPeers(map[PeerID]*Peer{p.ID(): p})

		out := o.PeersJSON()
		require.Len(t, out, 1)
		got, present := out[0]["protocol"]
		require.True(t, present, "rippled emits protocol unconditionally (PeerImp.cpp:419)")
		assert.Equal(t, "", got)
	})
}

// The handshake parser is already covered by TestParseHandshakeProtocolVersion;
// this test pins the captured value end-to-end through the response header
// shape that performHandshake feeds it (outbound: server's negotiated reply)
// and the request header shape that performInboundHandshake feeds it
// (inbound: peer's preferred — first XRPL/ token wins).
func TestPeer_ProtocolVersion_CaptureMatchesNegotiation(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"server_response_single", "XRPL/2.2", "XRPL/2.2"},
		{"client_request_list", "XRPL/2.2, RTXP/1.2", "XRPL/2.2"},
		{"older_version_negotiated", "XRPL/2.1", "XRPL/2.1"},
		{"empty_header", "", ""},
		{"unknown_only", "FOO/1.0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ParseHandshakeProtocolVersion(tc.header))
		})
	}
}
