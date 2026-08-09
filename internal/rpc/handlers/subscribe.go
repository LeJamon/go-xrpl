package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// SubscribeMethod handles the subscribe RPC command over plain JSON-RPC.
// The WebSocket-bound implementation lives in rpc/websocket.go. On this
// path only url-based (RPCSub) subscriptions exist: an admin supplies
// url/url_username/url_password and the url-subscription registry keeps a
// per-url subscriber whose events are delivered as outbound JSON-RPC
// "event" calls. Without url this is rippled's "Must be a JSON-RPC call."
// branch (rpcINVALID_PARAMS); url from a non-admin is rpcNO_PERMISSION.
type SubscribeMethod struct{ baseHandler }

// urlSubscriptionRequest applies the shared subscribe/unsubscribe gating
// for plain JSON-RPC calls described on SubscribeMethod and resolves the
// url-subscription service.
func urlSubscriptionRequest(ctx *types.RpcContext, params json.RawMessage, method string) (types.SubscriptionRequest, types.URLSubscriptionService, *rpcerrors.RpcError) {
	var request types.SubscriptionRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &request); err != nil {
			return request, nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
		}
	}

	if !request.HasURL() {
		// Must be a JSON-RPC call (rippled: no infoSub and no url).
		return request, nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	if ctx.Role != types.RoleAdmin {
		return request, nil, rpcerrors.RpcErrorNoPermission(method)
	}
	if ctx == nil || ctx.URLSubscriptions == nil {
		return request, nil, rpcerrors.RpcErrorNotSupported("url-based (RPCSub) subscriptions are not supported")
	}
	return request, ctx.URLSubscriptions, nil
}

func (m *SubscribeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	request, svc, rpcErr := urlSubscriptionRequest(ctx, params, "subscribe")
	if rpcErr != nil {
		return nil, rpcErr
	}
	result, rpcErr := svc.Subscribe(ctx, request)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return result, nil
}
