package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// SubscribeMethod handles the subscribe RPC command over plain JSON-RPC.
// The WebSocket-bound implementation lives in rpc/websocket.go.
//
// rippled additionally supports url-based (RPCSub) subscriptions on this
// path: an admin supplies url/url_username/url_password and rippled keeps
// a per-url InfoSub (NetworkOPs::mRpcSubMap) whose events are delivered as
// outbound JSON-RPC "event" calls (RPCSub.cpp: per-url sequence numbers,
// http/https with basic auth, fire-and-forget with logged failures).
// go-xrpl does not implement RPCSub: it needs a url-keyed registry shared
// between this handler and the WebSocket broadcast fan-out, an outbound
// HTTP delivery loop, and the subscribe ack/snapshot building that today
// lives on WebSocketServer — a subsystem of its own. Until then the
// admin+url case returns notSupported; without url this matches rippled's
// "Must be a JSON-RPC call." branch (rpcINVALID_PARAMS), and url from a
// non-admin returns rpcNO_PERMISSION exactly as rippled does.
type SubscribeMethod struct{ BaseHandler }

// urlSubscriptionError implements the shared subscribe/unsubscribe gating
// for plain JSON-RPC calls described on SubscribeMethod.
func urlSubscriptionError(ctx *types.RpcContext, params json.RawMessage) *types.RpcError {
	var request struct {
		URL string `json:"url"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &request)
	}

	if request.URL == "" {
		// Must be a JSON-RPC call (rippled: no infoSub and no url).
		return types.RpcErrorInvalidParams("Invalid parameters.")
	}
	if ctx.Role != types.RoleAdmin {
		return types.RpcErrorNoPermission("subscribe")
	}
	return types.RpcErrorNotSupported("url-based (RPCSub) subscriptions are not supported")
}

func (m *SubscribeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	return nil, urlSubscriptionError(ctx, params)
}
