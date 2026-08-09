package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// StopMethod handles the stop RPC method.
// Initiates a graceful server shutdown.
// Reference: rippled Stop.cpp
type StopMethod struct{ adminHandler }

func (m *StopMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if ctx == nil || ctx.Services == nil {
		return nil, rpcInternalInvariantError("stop: shutdown function unavailable")
	}
	shutdown := ctx.Services.Shutdowner()
	if shutdown == nil {
		return nil, rpcInternalInvariantError("stop: shutdown function unavailable")
	}

	shutdown.RequestShutdown()

	response := map[string]any{
		"message": "ripple server stopping",
	}

	return response, nil
}
