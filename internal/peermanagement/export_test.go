package peermanagement

func (p *Peer) SetTracking(t PeerTracking) { p.setTracking(t) }

func NewPublicKeyTokenFromBytes(key []byte) *PublicKeyToken {
	token, err := NewPublicKeyToken(key)
	if err != nil {
		panic(err)
	}
	return token
}
