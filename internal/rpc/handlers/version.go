package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// VersionMethod reports the server's API version range, mirroring rippled's
// setVersion (RPCHelpers.h:211-231), whose output shape depends on the
// resolved request api_version:
//
//   - api_version 1 (apiVersionIfUnspecified): first/good/last are the
//     SemanticVersion strings firstVersion/goodVersion/lastVersion, all "1.0.0"
//     (RPCHelpers.cpp:1001-1003).
//   - api_version >= 2: numeric first (= apiMinimumSupportedVersion, 1) and
//     last (apiBetaVersion when beta is on, else apiMaximumSupportedVersion),
//     with NO `good` field.
type VersionMethod struct{ BaseHandler }

// semanticVersion1 is the fixed "1.0.0" SemanticVersion rippled prints for
// first/good/last under api_version 1 (firstVersion/goodVersion/lastVersion,
// RPCHelpers.cpp:1001-1003).
const semanticVersion1 = "1.0.0"

func (m *VersionMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	var version map[string]any
	if ctx.ApiVersion == types.ApiVersion1 {
		version = map[string]any{
			"first": semanticVersion1,
			"good":  semanticVersion1,
			"last":  semanticVersion1,
		}
	} else {
		last := types.MaxSupportedApiVersion
		if ctx.Services != nil && ctx.Services.BetaRPCAPI {
			last = types.BetaApiVersion
		}
		version = map[string]any{
			"first": types.ApiVersion1,
			"last":  last,
		}
	}

	return map[string]any{"version": version}, nil
}
