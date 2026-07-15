package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// PingMethod handles the ping RPC method
type PingMethod struct{ BaseHandler }

func (m *PingMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	response := map[string]any{}

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
