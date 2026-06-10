package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// VersionMethod handles the version RPC method.
// Returns API version information for the server. The reported upper bound
// (`last`) tracks the beta_rpc_api config knob: BetaApiVersion when beta is
// enabled, otherwise MaxSupportedApiVersion — matching rippled setVersion
// (RPCHelpers.h) which caps `last` at apiBetaVersion only when BETA_RPC_API is
// configured.
type VersionMethod struct{ BaseHandler }

func (m *VersionMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	last := types.MaxSupportedApiVersion
	if ctx.Services != nil && ctx.Services.BetaRPCAPI {
		last = types.BetaApiVersion
	}

	response := map[string]any{
		"version": map[string]any{
			"first": types.ApiVersion1,
			"last":  last,
			"good":  types.ApiVersion2,
		},
	}

	return response, nil
}
