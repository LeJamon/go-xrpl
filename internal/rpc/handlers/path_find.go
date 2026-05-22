package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// PathFindMethod handles the path_find RPC method (WebSocket only).
// STUB over plain JSON-RPC: returns noEvents, mirroring rippled
// PathFind.cpp which returns rpcError(rpcNO_EVENTS) when context.infoSub is
// null (the unconditional state for non-subscription transports).
//
// TODO [pathfinding][websocket]: Implement when both pathfinding and WebSocket
//
//	subscriptions are ready.
//	- Reference: rippled PathFind.cpp
//	- Unlike ripple_path_find (one-shot), path_find creates a persistent session
//	  that sends updated paths whenever the ledger changes
//	- Subcommands: "create" (start tracking), "close" (stop), "status" (current paths)
//	- Requires: Pathfinder engine + WebSocket session context
type PathFindMethod struct{}

func (m *PathFindMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.RpcErrorNoEvents("")
}

func (m *PathFindMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *PathFindMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *PathFindMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
