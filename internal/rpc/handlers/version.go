package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// VersionMethod handles the version RPC method.
// Returns API version information for the server.
// IMPLEMENTED: Returns the supported API version range.
// Reference: rippled Version.h (VersionHandler)
type VersionMethod struct{ BaseHandler }

func (m *VersionMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	response := map[string]any{
		"version": map[string]any{
			"first": types.ApiVersion1,
			"last":  types.ApiVersion3,
			"good":  types.ApiVersion2,
		},
	}

	return response, nil
}
