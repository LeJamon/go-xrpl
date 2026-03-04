package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// VersionMethod handles the version RPC method.
// Returns API version information for the server.
// IMPLEMENTED: Returns the supported API version range.
// Reference: rippled Version.h (VersionHandler)
type VersionMethod struct{}

func (m *VersionMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	response := map[string]interface{}{
		"version": map[string]interface{}{
			"first": rpc_types.ApiVersion1,
			"last":  rpc_types.ApiVersion3,
			"good":  rpc_types.ApiVersion2,
		},
	}

	return response, nil
}

func (m *VersionMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleGuest
}

func (m *VersionMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
