package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// ServerStateMethod handles the server_state RPC method.
// This is the "machine-readable" variant (rippled human=false).
type ServerStateMethod struct{ BaseHandler }

func (m *ServerStateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	state := buildServerInfo(ctx, false)

	response := map[string]any{
		"state": state,
	}

	return response, nil
}
