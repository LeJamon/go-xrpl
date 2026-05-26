package peermanagement

import addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"

// PeerReservations exposes the peer-reservation table backing the
// peer_reservations_* admin RPCs. Nil when no data directory is configured
// (standalone / RPC-only).
func (o *Overlay) PeerReservations() *ReservationTable {
	if o.discovery == nil {
		return nil
	}
	return o.discovery.Reservations()
}

// isReservedPeer reports whether peer's node public key has an operator
// reservation. Reserved peers are bound to an unlimited resource Consumer like
// cluster members (see addPeer), so their traffic is never charge-limited —
// the reservation key is the base58 NodePublic, matching what the
// peer_reservations_* RPCs store.
func (o *Overlay) isReservedPeer(peer *Peer) bool {
	res := o.PeerReservations()
	if res == nil {
		return false
	}
	pk := peer.RemotePublicKey()
	if pk == nil {
		return false
	}
	enc, err := addresscodec.EncodeNodePublicKey(pk.Bytes())
	if err != nil {
		return false
	}
	return res.Contains(enc)
}
