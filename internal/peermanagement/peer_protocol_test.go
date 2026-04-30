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

// PeerImp.cpp:419 emits `protocol` unconditionally. Production peers
// reach PeersJSON only after addPeer (post-handshake), so protocolVersion
// is always set; the unset branch below is a guard against future
// callers that bypass the handshake path.
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

// TestPeer_ProtocolVersion_NegotiationMatchesRippled exercises the two
// header shapes Peer.protocolVersion is fed from in production:
//   - inbound (peer's request advertises a list)   → NegotiateProtocolVersion
//   - outbound (server's response replies with one) → VerifyOutboundProtocolVersion
//
// The cases are aligned with rippled ProtocolVersion_test.cpp:80-97 and
// ConnectAttempt.cpp:340-351 so any future drift in negotiation rules is
// caught here.
func TestPeer_ProtocolVersion_NegotiationMatchesRippled(t *testing.T) {
	t.Run("inbound_negotiation", func(t *testing.T) {
		cases := []struct {
			name   string
			header string
			want   string
		}{
			{"single_supported_max", "XRPL/2.2", "XRPL/2.2"},
			{"single_supported_older", "XRPL/2.1", "XRPL/2.1"},
			// rippled fixture: max of intersection.
			{"rippled_intersection_2_1", "RTXP/1.2, XRPL/2.0, XRPL/2.1", "XRPL/2.1"},
			{"rippled_intersection_2_2", "RTXP/1.2, XRPL/2.2, XRPL/2.3, XRPL/999.999", "XRPL/2.2"},
			// Original Finding 1 trigger: rippled-style peer offering
			// {2.1, 2.2} — first-token parser would have stored 2.1,
			// negotiation must emit 2.2.
			{"rippled_peer_full_list", "XRPL/2.1, XRPL/2.2", "XRPL/2.2"},
			{"reordered_picks_max", "XRPL/2.2, XRPL/2.1", "XRPL/2.2"},
			{"rtxp_only_rejected", "RTXP/1.2", ""},
			{"empty_header", "", ""},
			{"unknown_only", "FOO/1.0", ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, NegotiateProtocolVersion(tc.header))
			})
		}
	})

	t.Run("outbound_verification", func(t *testing.T) {
		cases := []struct {
			name   string
			header string
			want   string
		}{
			{"server_picked_2_2", "XRPL/2.2", "XRPL/2.2"},
			{"server_picked_2_1", "XRPL/2.1", "XRPL/2.1"},
			{"server_picked_unsupported", "XRPL/3.0", ""},
			// Rippled requires exactly one token in the response.
			{"server_returned_list_rejected", "XRPL/2.1, XRPL/2.2", ""},
			{"empty", "", ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, VerifyOutboundProtocolVersion(tc.header))
			})
		}
	})
}
