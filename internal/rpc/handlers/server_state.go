package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// ServerStateMethod handles the server_state RPC method.
// This is the "machine-readable" variant (rippled human=false).
type ServerStateMethod struct{ BaseHandler }

func (m *ServerStateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	state := buildServerInfo(ctx.Services, false)

	response := map[string]interface{}{
		"state": state,
	}

	return response, nil
}
