package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// pathFindMethod handles the path_find RPC method over plain JSON-RPC.
// It returns noEvents, mirroring rippled PathFind.cpp which returns
// rpcError(rpcNO_EVENTS) when context.infoSub is null — the unconditional
// state for non-subscription transports.
//
// The persistent path_find session (subcommands "create"/"close"/"status",
// pushing updated paths on every ledger close) is a WebSocket-only feature
// implemented separately on the WS transport: see
// (*WebSocketServer).handlePathFind in internal/rpc/websocket.go and the
// PathFindSession in internal/rpc/path_find_session.go, refreshed by the
// bounded asynchronous UpdatePathFindSessions pipeline after each ledger
// close (wired in internal/node/runtime.go).
type pathFindMethod struct{ baseHandler }

func (m *pathFindMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if rpcErr := RequirePathSearch(ctx); rpcErr != nil {
		return nil, rpcErr
	}
	if len(params) == 0 {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil || fields == nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	rawSubcommand, ok := fields["subcommand"]
	if !ok {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	var subcommand *string
	if err := json.Unmarshal(rawSubcommand, &subcommand); err != nil || subcommand == nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	return nil, types.RpcErrorNoEvents("")
}

func (m *pathFindMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}
