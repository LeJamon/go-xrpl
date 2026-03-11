package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// VersionMethod handles the version RPC method.
// Returns API version information for the server.
// IMPLEMENTED: Returns the supported API version range.
// Reference: rippled Version.h (VersionHandler)
type VersionMethod struct{}

func (m *VersionMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	response := map[string]interface{}{
		"version": map[string]interface{}{
			"first": types.ApiVersion1,
			"last":  types.ApiVersion3,
			"good":  types.ApiVersion2,
		},
	}

	return response, nil
}

func (m *VersionMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *VersionMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *VersionMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
