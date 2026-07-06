package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRelayValidationsPolicy(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want RelayValidationsPolicy
	}{
		{"", RelayValidationsAll},
		{"all", RelayValidationsAll},
		{"ALL", RelayValidationsAll},
		{"trusted", RelayValidationsTrusted},
		{"Trusted", RelayValidationsTrusted},
		{"drop_untrusted", RelayValidationsDropUntrusted},
		{"Drop_Untrusted", RelayValidationsDropUntrusted},
		// Unknown values are rejected upstream by config validation;
		// the parser itself falls back to the rippled default.
		{"garbage", RelayValidationsAll},
	} {
		if got := ParseRelayValidationsPolicy(tc.in); got != tc.want {
			t.Errorf("ParseRelayValidationsPolicy(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestAdaptor_RelayValidationsPolicyAccessors pins the policy → accessor
// mapping the engine (relay gate) and router (pre-verify drop) read.
func TestAdaptor_RelayValidationsPolicyAccessors(t *testing.T) {
	for _, tc := range []struct {
		policy    RelayValidationsPolicy
		wantRelay bool
		wantDrop  bool
	}{
		{RelayValidationsAll, true, false},
		{RelayValidationsTrusted, false, false},
		{RelayValidationsDropUntrusted, false, true},
	} {
		a := New(Config{RelayValidations: tc.policy})
		assert.Equal(t, tc.wantRelay, a.RelayUntrustedValidations(), "policy %v relay", tc.policy)
		assert.Equal(t, tc.wantDrop, a.DropUntrustedValidations(), "policy %v drop", tc.policy)
	}
}

// TestAdaptor_IsListed: nothing is listed until a lookup is wired; then the
// adaptor defers to it (Aggregator.IsListed in production).
func TestAdaptor_IsListed(t *testing.T) {
	a := New(Config{})
	listed := consensus.NodeID{0x11}

	assert.False(t, a.IsListed(listed), "no lookup wired")

	a.SetListedLookup(func(n consensus.NodeID) bool { return n == listed })
	assert.True(t, a.IsListed(listed))
	assert.False(t, a.IsListed(consensus.NodeID{0x22}))
}

// TestAdaptor_SetTrustedValidatorsFiresTrustChange: every UNL swap invokes
// the registered trust-change callback (the engine's tracker refresh) after
// the new set is visible through GetTrustedValidators.
func TestAdaptor_SetTrustedValidatorsFiresTrustChange(t *testing.T) {
	a := New(Config{})
	n := consensus.NodeID{0x33}

	var sawTrusted bool
	fired := 0
	a.OnTrustChanged(func() {
		fired++
		sawTrusted = a.IsTrusted(n)
	})

	a.SetTrustedValidators([]consensus.NodeID{n}, [][33]byte{{1}})
	require.Equal(t, 1, fired, "callback must fire once per swap")
	assert.True(t, sawTrusted, "callback must observe the post-swap trusted set")

	a.SetTrustedValidators(nil, nil)
	assert.Equal(t, 2, fired)
}

// TestRouter_DropUntrustedValidations: under [relay_validations] =
// drop_untrusted the router sheds validations signed outside the UNL before
// the engine (and its signature verification) ever sees them — rippled's
// PeerImp pre-verify drop under RELAY_UNTRUSTED_VALIDATIONS == -1.
// TestRouterDispatchesValidation covers the default-policy positive path
// for the same untrusted payload.
func TestRouter_DropUntrustedValidations(t *testing.T) {
	engine := &mockEngine{}
	a := newTestAdaptor(t)
	a.relayValidations = RelayValidationsDropUntrusted

	router := NewRouter(engine, a, make(chan *peermanagement.InboundMessage))

	build := func(node consensus.NodeID, signingKey [33]byte) *peermanagement.InboundMessage {
		v := &consensus.Validation{
			Full:      true,
			LedgerSeq: 42,
			NodeID:    node,
			SignTime:  time.Now(),
		}
		v.LedgerID = consensus.LedgerID{1}
		v.SigningPubKey = signingKey
		v.Signature = make([]byte, 70)
		return &peermanagement.InboundMessage{
			PeerID:  2,
			Type:    uint16(message.TypeValidation),
			Payload: encodePayload(t, &message.Validation{Validation: SerializeSTValidation(v)}),
		}
	}

	// Untrusted signer → dropped pre-engine.
	untrustedKey := [33]byte{0x02, 0x99}
	router.handleValidation(build(consensus.CalcNodeID(untrustedKey), untrustedKey))
	assert.Empty(t, engine.getValidations(), "untrusted validation must be shed before the engine")

	// Trusted signer (the adaptor's own identity) still passes through.
	identityKey := [33]byte(a.identity.SigningPubKey())
	router.handleValidation(build(a.identity.NodeID, identityKey))
	require.Len(t, engine.getValidations(), 1, "trusted validation must reach the engine")
}
