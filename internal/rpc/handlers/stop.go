package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// StopMethod handles the stop RPC method.
// Initiates a graceful server shutdown.
// Reference: rippled Stop.cpp
type StopMethod struct{}

func (m *StopMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	if types.Services == nil || types.Services.ShutdownFunc == nil {
		return nil, types.RpcErrorInternal("Shutdown function not available")
	}

	// Trigger shutdown asynchronously so the response can be sent first
	types.Services.ShutdownFunc()

	response := map[string]interface{}{
		"message": "ripple server stopping",
	}

	return response, nil
}

func (m *StopMethod) RequiredRole() types.Role {
	return types.RoleAdmin
}

func (m *StopMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *StopMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
