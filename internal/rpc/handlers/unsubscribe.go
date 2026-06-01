package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// UnsubscribeMethod handles the unsubscribe RPC command.
// STUB over plain JSON-RPC: mirrors rippled Unsubscribe.cpp's
// "Must be a JSON-RPC call." branch — when there is no infoSub and no `url`
// parameter, rippled returns rpcError(rpcINVALID_PARAMS). The real
// WebSocket-bound implementation lives in rpc/websocket.go.
//
// TODO [websocket]: rippled also supports HTTP+url admin unsubscribes
//
//	(Unsubscribe.cpp branch on context.params.isMember(jss::url) with
//	Role::ADMIN). Once goxrpl wires up the RPCSub callback path that
//	branch should be served here too.
//	- Reference: rippled Unsubscribe.cpp
//	- Removes subscriptions created by subscribe command
type UnsubscribeMethod struct{ BaseHandler }

func (m *UnsubscribeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.RpcErrorInvalidParams("Invalid parameters.")
}
