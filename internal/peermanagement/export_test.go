package peermanagement

import "github.com/btcsuite/btcd/btcec/v2"

func (p *Peer) SetTracking(t PeerTracking) { p.setTracking(t) }

// NewPublicKeyTokenFromBtcec wraps an already-parsed key, letting tests
// build tokens without round-tripping through the serialized form.
func NewPublicKeyTokenFromBtcec(key *btcec.PublicKey) *PublicKeyToken {
	return &PublicKeyToken{key: key}
}
