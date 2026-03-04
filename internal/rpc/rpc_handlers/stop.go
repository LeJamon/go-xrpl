package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// StopMethod handles the stop RPC method.
// Initiates a graceful server shutdown.
// Reference: rippled Stop.cpp
type StopMethod struct{}

func (m *StopMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	if rpc_types.Services == nil || rpc_types.Services.ShutdownFunc == nil {
		return nil, rpc_types.RpcErrorInternal("Shutdown function not available")
	}

	// Trigger shutdown asynchronously so the response can be sent first
	rpc_types.Services.ShutdownFunc()

	response := map[string]interface{}{
		"message": "ripple server stopping",
	}

	return response, nil
}

func (m *StopMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleAdmin
}

func (m *StopMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
