package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// PathFindMethod handles the path_find RPC method (WebSocket only).
// STUB: Returns notSupported over HTTP. WebSocket-only persistent subscription.
//
// TODO [pathfinding][websocket]: Implement when both pathfinding and WebSocket
//   subscriptions are ready.
//   - Reference: rippled PathFind.cpp
//   - Unlike ripple_path_find (one-shot), path_find creates a persistent session
//     that sends updated paths whenever the ledger changes
//   - Subcommands: "create" (start tracking), "close" (stop), "status" (current paths)
//   - Requires: Pathfinder engine + WebSocket session context
//   - The HTTP handler should always return notSupported
type PathFindMethod struct{}

func (m *PathFindMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return nil, rpc_types.NewRpcError(rpc_types.RpcNOT_SUPPORTED, "notSupported", "notSupported",
		"path_find is only available via WebSocket")
}

func (m *PathFindMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleGuest
}

func (m *PathFindMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
