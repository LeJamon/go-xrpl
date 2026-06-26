package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// UnsubscribeMethod handles the unsubscribe RPC command over plain
// JSON-RPC. The WebSocket-bound implementation lives in rpc/websocket.go.
// Like subscribe, only the url (RPCSub) branch exists on this path: the
// listed streams are removed from the per-url subscriber and its registry
// entry is dropped once no stream subscriptions remain. The gating mirrors
// the subscribe path: no url → rpcINVALID_PARAMS ("Must be a JSON-RPC
// call." branch), url from a non-admin → rpcNO_PERMISSION.
type UnsubscribeMethod struct{ BaseHandler }

func (m *UnsubscribeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	request, svc, rpcErr := urlSubscriptionRequest(ctx, params, "unsubscribe")
	if rpcErr != nil {
		return nil, rpcErr
	}
	result, rpcErr := svc.Unsubscribe(ctx, request)
	if rpcErr != nil {
		return nil, rpcErr
	}
	return result, nil
}
