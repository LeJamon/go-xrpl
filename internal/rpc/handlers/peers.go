package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
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

func (m *PeersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{
		"peers": []interface{}{},
	}, nil
}

func (m *PeersMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *PeersMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *PeersMethod) RequiredCondition() types.Condition {
	return types.NoCondition
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

func (m *PeerReservationsAddMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{
		"previous": []interface{}{},
		"current":  []interface{}{},
	}, nil
}

func (m *PeerReservationsAddMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *PeerReservationsAddMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *PeerReservationsAddMethod) RequiredCondition() types.Condition {
	return types.NoCondition
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

func (m *PeerReservationsDelMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{
		"previous": []interface{}{},
		"current":  []interface{}{},
	}, nil
}

func (m *PeerReservationsDelMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *PeerReservationsDelMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *PeerReservationsDelMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}

// PeerReservationsListMethod handles the peer_reservations_list RPC method.
// STUB: Returns empty list. Network-only — not needed for standalone mode.
//
// TODO [network]: Implement when adding P2P networking layer.
//   - Requires: PeerReservationTable service
//   - Reference: rippled Reservations.cpp
//   - Returns all peer reservations with their public keys and descriptions
type PeerReservationsListMethod struct{}

func (m *PeerReservationsListMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return map[string]interface{}{
		"reservations": []interface{}{},
	}, nil
}

func (m *PeerReservationsListMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *PeerReservationsListMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *PeerReservationsListMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
