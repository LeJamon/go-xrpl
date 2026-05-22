package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// SubscribeMethod handles the subscribe RPC command.
// STUB over plain JSON-RPC: mirrors rippled Subscribe.cpp's
// "Must be a JSON-RPC call." branch — when there is no infoSub and no `url`
// parameter, rippled returns rpcError(rpcINVALID_PARAMS). The real
// WebSocket-bound implementation lives in rpc/websocket.go.
//
// TODO [websocket]: rippled also supports HTTP+url admin subscriptions
//
//	(Subscribe.cpp branch on context.params.isMember(jss::url) with
//	Role::ADMIN). Once goxrpl wires up the RPCSub callback path that
//	branch should be served here too.
//	- Reference: rippled Subscribe.cpp
//	- Streams: ledger, transactions, transactions_proposed, peer_status,
//	  consensus, server, validations, manifests, book (order book)
//	- Account subscriptions: accounts, accounts_proposed
type SubscribeMethod struct{ BaseHandler }

func (m *SubscribeMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.RpcErrorInvalidParams("Invalid parameters.")
}
