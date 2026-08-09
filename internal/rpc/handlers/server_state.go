package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// ServerStateMethod handles the server_state RPC method.
// This is the "machine-readable" variant (rippled human=false).
type ServerStateMethod struct{ baseHandler }

func (m *ServerStateMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	state := buildServerInfo(ctx, false)
	if serverCountersRequested(params) {
		addServerDiagnostics(state, ctx.Services)
	}
	if warnings := buildServerWarnings(ctx.Services, ctx.IsAdmin); len(warnings) > 0 {
		state["warnings"] = warnings
	}
	return map[string]any{"state": state}, nil
}
