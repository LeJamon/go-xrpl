package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// UnsubscribeMethod handles the unsubscribe RPC command (WebSocket only).
// STUB over HTTP: Returns notSupported. The real implementation is in websocket.go.
//
// TODO [websocket]: Same as subscribe — this HTTP stub is correct.
//
//	The WebSocket server handles actual unsubscriptions.
//	- Reference: rippled Unsubscribe.cpp
//	- Removes subscriptions created by subscribe command
type UnsubscribeMethod struct{ BaseHandler }

func (m *UnsubscribeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.NewRpcError(types.RpcNOT_SUPPORTED, "notSupported", "notSupported",
		"unsubscribe is only available via WebSocket")
}
