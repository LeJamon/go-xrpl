package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeValidProposeSet returns a ProposeSet whose non-pubkey fields sit
// comfortably inside the bounds validateProposeBounds enforces, so tests
// that only mutate NodePubKey can isolate that axis without tripping
// other checks.
func makeValidProposeSet() *message.ProposeSet {
	return &message.ProposeSet{
		ProposeSeq:     1,
		CurrentTxHash:  make([]byte, 32),
		NodePubKey:     nil, // caller fills
		CloseTime:      timeToXrplEpoch(time.Unix(1_700_000_000, 0)),
		Signature:      make([]byte, signatureMinLen),
		PreviousLedger: make([]byte, 32),
	}
}

// TestValidateProposeBounds_RejectsEd25519Prefix pins the B6 rippled
// parity rule: proposal pubkeys MUST be compressed secp256k1 (0x02/0x03
// prefix). A 33-byte ed25519 key (0xED || 32 bytes) has the correct
// length, so a length-only check would let it through — but rippled
// explicitly rejects it at PeerImp.cpp:1679-1680 via
// `publicKeyType(...) != KeyType::secp256k1`. The malformed-bounds
// charge path must attribute this to the peer specifically as a type
// error, not a size error.
func TestValidateProposeBounds_RejectsEd25519Prefix(t *testing.T) {
	p := makeValidProposeSet()
	// ed25519: 0xED prefix followed by 32 bytes of key material. Length
	// is 33 — the same as a compressed secp256k1 point — so only a
	// prefix check catches it.
	p.NodePubKey = make([]byte, 33)
	p.NodePubKey[0] = 0xED

	field, ok := validateProposeBounds(p)
	assert.False(t, ok, "ed25519-prefixed pubkey must be rejected")
	assert.Equal(t, "pubkey-type", field,
		"reason must be pubkey-type (not pubkey-size) so the peer is charged with the right class of violation")
}

// TestValidateProposeBounds_AcceptsSecp256k1Prefix verifies that valid
// compressed secp256k1 pubkey prefixes (0x02 and 0x03) both pass. This
// is the happy-path counterpart to the ed25519 rejection above, and
// guards against an over-tight prefix check (e.g. accepting only one of
// 0x02/0x03).
func TestValidateProposeBounds_AcceptsSecp256k1Prefix(t *testing.T) {
	for _, prefix := range []byte{0x02, 0x03} {
		p := makeValidProposeSet()
		p.NodePubKey = make([]byte, 33)
		p.NodePubKey[0] = prefix

		field, ok := validateProposeBounds(p)
		assert.True(t, ok, "secp256k1 prefix %#x must be accepted", prefix)
		assert.Equal(t, "", field, "no bad_field should be reported for a valid pubkey")
	}
}

// TestHandleProposal_Ed25519PubKeyChargesPeer is the end-to-end guard:
// a TMProposeSet whose NodePubKey has an ed25519 prefix must be dropped
// BEFORE the engine sees it, and the peer must be attributed a bad-data
// event with reason "proposal-malformed-pubkey-type". Mirrors rippled's
// PeerImp feeInvalidSignature charge path at PeerImp.cpp:1682-1686.
func TestHandleProposal_Ed25519PubKeyChargesPeer(t *testing.T) {
	r, rs := makeRouterWithBadDataRecorder(t)

	p := makeValidProposeSet()
	p.NodePubKey = make([]byte, 33)
	p.NodePubKey[0] = 0xED // ed25519 prefix, length 33 — passes size check

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID:  77,
		Type:    uint16(message.TypeProposeLedger),
		Payload: encodePayload(t, p),
	})

	calls := rs.getBadDataCalls()
	require.Len(t, calls, 1,
		"ed25519-prefixed proposal must trigger exactly one IncPeerBadData call")
	assert.Equal(t, uint64(77), calls[0].peerID,
		"bad-data must be attributed to the peer that sent the disallowed pubkey")
	assert.Equal(t, "proposal-malformed-pubkey-type", calls[0].reason,
		"reason label must be proposal-malformed-pubkey-type so the diagnostic distinguishes the prefix violation from a size violation")
}
