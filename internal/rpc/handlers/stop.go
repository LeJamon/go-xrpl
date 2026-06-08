package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// StopMethod handles the stop RPC method.
// Initiates a graceful server shutdown.
// Reference: rippled Stop.cpp
type StopMethod struct{ AdminHandler }

func (m *StopMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if ctx.Services == nil || ctx.Services.ShutdownFunc == nil {
		return nil, types.RpcErrorInternal("Shutdown function not available")
	}

	// Trigger shutdown asynchronously so the response can be sent first
	ctx.Services.ShutdownFunc()

	response := map[string]any{
		"message": "ripple server stopping",
	}

	return response, nil
}
