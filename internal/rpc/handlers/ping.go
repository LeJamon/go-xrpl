package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// PingMethod handles the ping RPC method
type PingMethod struct{}

func (m *PingMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	response := map[string]interface{}{}

	// Add role info based on RPC context (matches rippled Ping.cpp)
	if ctx != nil {
		switch ctx.Role {
		case types.RoleAdmin:
			response["role"] = "admin"
		case types.RoleIdentified:
			response["role"] = "identified"
			if ctx.ClientIP != "" {
				response["ip"] = ctx.ClientIP
			}
		default:
			// Guest/User don't get role info in response
		}
	}

	return response, nil
}

func (m *PingMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *PingMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *PingMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
