package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// PeersMethod handles the peers RPC method.
// STUB: Returns empty peer list. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Requires: Overlay/PeerManager service providing connected peer info
//   - Reference: rippled Peers.cpp → context.app.overlay().json()
//   - Response should include per-peer: address, port, latency, version,
//     ledger sequence, complete_ledgers range, cluster status
type PeersMethod struct{}

func (m *PeersMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return map[string]interface{}{
		"peers": []interface{}{},
	}, nil
}

func (m *PeersMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *PeersMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}

// PeerReservationsAddMethod handles the peer_reservations_add RPC method.
// STUB: Returns empty result. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Requires: PeerReservationTable service
//   - Reference: rippled Reservations.cpp → context.app.peerReservations()
//   - Params: public_key (required), description (optional)
//   - Should add a peer reservation and return previous + current state
type PeerReservationsAddMethod struct{}

func (m *PeerReservationsAddMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return map[string]interface{}{
		"previous": []interface{}{},
		"current":  []interface{}{},
	}, nil
}

func (m *PeerReservationsAddMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *PeerReservationsAddMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}

// PeerReservationsDelMethod handles the peer_reservations_del RPC method.
// STUB: Returns empty result. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Requires: PeerReservationTable service
//   - Reference: rippled Reservations.cpp
//   - Params: public_key (required)
//   - Should remove a peer reservation and return previous + current state
type PeerReservationsDelMethod struct{}

func (m *PeerReservationsDelMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return map[string]interface{}{
		"previous": []interface{}{},
		"current":  []interface{}{},
	}, nil
}

func (m *PeerReservationsDelMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *PeerReservationsDelMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}

// PeerReservationsListMethod handles the peer_reservations_list RPC method.
// STUB: Returns empty list. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Requires: PeerReservationTable service
//   - Reference: rippled Reservations.cpp
//   - Returns all peer reservations with their public keys and descriptions
type PeerReservationsListMethod struct{}

func (m *PeerReservationsListMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return map[string]interface{}{
		"reservations": []interface{}{},
	}, nil
}

func (m *PeerReservationsListMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *PeerReservationsListMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
