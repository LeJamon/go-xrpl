package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// PingMethod handles the ping RPC method
type PingMethod struct{}

func (m *PingMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Ping method is used to test connectivity and measure round-trip time
	// It simply returns an empty success response

	response := map[string]interface{}{
		// Empty response indicates successful ping
	}

	return response, nil
}

func (m *PingMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *PingMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
