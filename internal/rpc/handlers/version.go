package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// VersionMethod handles the version RPC method.
// Returns API version information for the server.
// IMPLEMENTED: Returns the supported API version range.
// Reference: rippled Version.h (VersionHandler)
type VersionMethod struct{ BaseHandler }

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

