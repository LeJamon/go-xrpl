package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
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
type SubscribeMethod struct{ BaseHandler }

func (m *SubscribeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.NewRpcError(types.RpcNOT_SUPPORTED, "notSupported", "notSupported",
		"subscribe is only available via WebSocket")
}

