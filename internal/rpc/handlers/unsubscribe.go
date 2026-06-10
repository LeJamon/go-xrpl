package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// UnsubscribeMethod handles the unsubscribe RPC command over plain
// JSON-RPC. The WebSocket-bound implementation lives in rpc/websocket.go.
//
// rippled's url branch (Unsubscribe.cpp) looks up the per-url RPCSub
// created by subscribe and removes the listed streams from it; see
// SubscribeMethod for why go-xrpl does not implement RPCSub yet. The
// gating here mirrors the subscribe path: no url → rpcINVALID_PARAMS
// ("Must be a JSON-RPC call." branch), url from a non-admin →
// rpcNO_PERMISSION, url from an admin → notSupported.
type UnsubscribeMethod struct{ BaseHandler }

func (m *UnsubscribeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	return nil, urlSubscriptionError(ctx, params)
}
