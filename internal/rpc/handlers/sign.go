package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// SignMethod handles the sign RPC method
type SignMethod struct{ BaseHandler }

func (m *SignMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	setLoadHeavy(ctx)
	var request signingRequest

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	if len(request.TxJson) == 0 {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: tx_json")
	}

	// Sign the transaction using the shared helper
	signed, rpcErr := signTransactionJSON(ctx.Context, ctx.Services, request.TxJson, request.signCredentials, request.Offline, ctx.Unlimited, ctx.ApiVersion, params, request.SignatureTarget)
	if rpcErr != nil {
		return nil, rpcErr
	}

	return formatSignResult(*signed, ctx.ApiVersion), nil
}

func (m *SignMethod) RequiredRole() types.Role {
	return types.RoleUser // Signing requires user privileges
}
