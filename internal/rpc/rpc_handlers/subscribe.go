package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// SubscribeMethod handles the subscribe RPC command (WebSocket only).
// STUB over HTTP: Returns notSupported. The real implementation is in websocket.go.
//
// TODO [websocket]: This HTTP stub is correct — subscribe requires a persistent
//   WebSocket connection. The WebSocket server (rpc/websocket.go) already has
//   a working subscription system. This HTTP handler exists only so the method
//   is registered and returns a meaningful error over HTTP.
//   - Reference: rippled Subscribe.cpp
//   - Streams: ledger, transactions, transactions_proposed, peer_status,
//     consensus, server, validations, manifests, book (order book)
//   - Account subscriptions: accounts, accounts_proposed
type SubscribeMethod struct{}

func (m *SubscribeMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	return nil, rpc_types.NewRpcError(rpc_types.RpcNOT_SUPPORTED, "notSupported", "notSupported",
		"subscribe is only available via WebSocket")
}

func (m *SubscribeMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleGuest
}

func (m *SubscribeMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
