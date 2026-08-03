package peermanagement

import addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"

// PeerReservations exposes the peer-reservation table backing the
// peer_reservations_* admin RPCs and consulted at inbound admission. Nil when
// no data directory is configured (standalone / RPC-only).
func (o *Overlay) PeerReservations() *ReservationTable {
	if o.discovery == nil {
		return nil
	}
	return o.discovery.Reservations()
}

// isReservedPeer reports whether the peer's node public key has an operator
// reservation. A reserved peer is admitted beyond the inbound slot cap,
// mirroring the reservation half of rippled's
// activate(slot, key, reserved) predicate (OverlayImpl.cpp:263-265). The
// reservation key is the base58 NodePublic, matching what the
// peer_reservations_* RPCs store. Unlike cluster members, reserved peers keep
// a normal resource Consumer — rippled never grants them charge immunity.
func (o *Overlay) isReservedPeer(peer *Peer) bool {
	res := o.PeerReservations()
	if res == nil {
		return false
	}
	pk := peer.RemotePublicKeyBytes()
	if len(pk) == 0 {
		return false
	}
	enc, err := addresscodec.EncodeNodePublicKey(pk)
	if err != nil {
		return false
	}
	return res.Contains(enc)
}
