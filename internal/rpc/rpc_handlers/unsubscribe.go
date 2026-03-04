package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// UnsubscribeMethod handles the unsubscribe RPC command (WebSocket only).
// STUB over HTTP: Returns notSupported. The real implementation is in websocket.go.
//
// TODO [websocket]: Same as subscribe — this HTTP stub is correct.
//   The WebSocket server handles actual unsubscriptions.
//   - Reference: rippled Unsubscribe.cpp
//   - Removes subscriptions created by subscribe command
type UnsubscribeMethod struct{}

func (m *UnsubscribeMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return nil, rpc_types.NewRpcError(rpc_types.RpcNOT_SUPPORTED, "notSupported", "notSupported",
		"unsubscribe is only available via WebSocket")
}

func (m *UnsubscribeMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleGuest
}

func (m *UnsubscribeMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
