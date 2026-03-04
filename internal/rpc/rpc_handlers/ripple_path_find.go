package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// RipplePathFindMethod handles the ripple_path_find RPC method.
// STUB: Returns notImplemented. Requires full pathfinding engine.
//
// TODO [pathfinding]: Implement payment path finding.
//   - Requires: Pathfinder engine (major feature — rippled's is ~5000 lines)
//   - Reference: rippled RipplePathFind.cpp, Pathfinder.cpp, PathRequest.cpp
//   - Steps:
//     1. Parse: source_account, destination_account, destination_amount,
//        send_max (optional), source_currencies (optional)
//     2. Resolve target ledger state
//     3. Run pathfinding algorithm:
//        a. Direct paths (same currency, direct trust line)
//        b. Rippling paths (through intermediary trust lines)
//        c. Order book paths (through DEX offers)
//        d. Combined multi-hop paths
//     4. For each path, calculate: source_amount, paths_computed
//     5. Sort by cost (lowest source_amount first)
//     6. Return alternatives array with path details
//   - Depends on: payment engine's flow/strand infrastructure already exists
//     in internal/core/tx/payment/ — pathfinding uses similar strand logic
//     but searches backwards from destination to source
type RipplePathFindMethod struct{}

func (m *RipplePathFindMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return nil, rpc_types.NewRpcError(rpc_types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"Path finding engine not yet implemented")
}

func (m *RipplePathFindMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleGuest
}

func (m *RipplePathFindMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
