package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// PathFindMethod handles the path_find RPC method over plain JSON-RPC.
// It returns noEvents, mirroring rippled PathFind.cpp which returns
// rpcError(rpcNO_EVENTS) when context.infoSub is null — the unconditional
// state for non-subscription transports.
//
// The persistent path_find session (subcommands "create"/"close"/"status",
// pushing updated paths on every ledger close) is a WebSocket-only feature
// implemented separately on the WS transport: see
// (*WebSocketServer).handlePathFind in internal/rpc/websocket.go and the
// PathFindSession in internal/rpc/path_find_session.go, refreshed via
// UpdatePathFindSessions on each ledger close (wired in cli/server.go).
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
