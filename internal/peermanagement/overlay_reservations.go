package peermanagement

// PeerReservations exposes the peer-reservation table backing the
// peer_reservations_* admin RPCs. Nil when no data directory is configured
// (standalone / RPC-only).
//
// Reservations are not yet consulted at peer admission: rippled grants a
// reserved peer a PeerFinder slot when inbound slots are full
// (OverlayImpl.cpp:263-267, activate(slot, key, reserved)), whereas goXRPL
// enforces its inbound cap pre-handshake (Overlay.canAcceptInbound) before the
// remote node key is known. Honouring reservations at admission therefore needs
// post-handshake slot re-evaluation and is left for a follow-up; this PR keeps
// the table to the RPC + persistence surface rather than granting reserved
// peers a behaviour rippled does not (unlimited resource consumption).
func (o *Overlay) PeerReservations() *ReservationTable {
	if o.discovery == nil {
		return nil
	}
	return o.discovery.Reservations()
}
