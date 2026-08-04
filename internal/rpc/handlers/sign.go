package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// SignMethod handles the sign RPC method
type SignMethod struct{ baseHandler }

func (m *SignMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (result any, rpcErr *types.RpcError) {
	if rpcErr := rejectDisabledSigning(ctx); rpcErr != nil {
		return nil, rpcErr
	}
	defer func() {
		result, rpcErr = addSigningDeprecation(result, rpcErr)
	}()

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
	signed, rpcErr := signTransactionJSON(ctx, request.TxJson, request.signCredentials, request.Offline, params, request.SignatureTarget)
	if rpcErr != nil {
		return nil, rpcErr
	}

	return formatSignResult(*signed, ctx.ApiVersion), nil
}

func (m *SignMethod) RequiredRole() types.Role {
	return types.RoleUser // Signing requires user privileges
}
