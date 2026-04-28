package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// PeersMethod returns peers from ctx.PeerSource (rippled Peers.cpp).
// Empty list when no source is wired (standalone mode).
type PeersMethod struct{ AdminHandler }

func (m *PeersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var peers []map[string]any
	if ctx.PeerSource != nil {
		peers = ctx.PeerSource.PeersJSON()
	}
	if peers == nil {
		peers = []map[string]any{}
	}
	return map[string]any{"peers": peers}, nil
}

// PeerReservationsAddMethod handles the peer_reservations_add RPC method.
// STUB: Returns empty result. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Requires: PeerReservationTable service
//   - Reference: rippled Reservations.cpp → context.app.peerReservations()
//   - Params: public_key (required), description (optional)
//   - Should add a peer reservation and return previous + current state
type PeerReservationsAddMethod struct{ AdminHandler }

func (m *PeerReservationsAddMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{
		"previous": []interface{}{},
		"current":  []interface{}{},
	}, nil
}

// PeerReservationsDelMethod handles the peer_reservations_del RPC method.
// STUB: Returns empty result. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Requires: PeerReservationTable service
//   - Reference: rippled Reservations.cpp
//   - Params: public_key (required)
//   - Should remove a peer reservation and return previous + current state
type PeerReservationsDelMethod struct{ AdminHandler }

func (m *PeerReservationsDelMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{
		"previous": []interface{}{},
		"current":  []interface{}{},
	}, nil
}

// PeerReservationsListMethod handles the peer_reservations_list RPC method.
// STUB: Returns empty list. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Requires: PeerReservationTable service
//   - Reference: rippled Reservations.cpp
//   - Returns all peer reservations with their public keys and descriptions
type PeerReservationsListMethod struct{ AdminHandler }

func (m *PeerReservationsListMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{
		"reservations": []interface{}{},
	}, nil
}
