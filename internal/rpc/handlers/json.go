package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// jsonMethod handles the json RPC method.
// This is a proxy that forwards calls to other RPC methods.
// Reference: rippled JSON.cpp
type jsonMethod struct{ baseHandler }

func (m *jsonMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	var request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}
	if request.Method == "" && len(request.Params) > 0 {
		var nested struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params,omitempty"`
		}
		if json.Unmarshal(request.Params, &nested) == nil && nested.Method != "" {
			request.Method = nested.Method
			request.Params = nested.Params
		}
	}

	if request.Method == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: method")
	}

	if ctx == nil || ctx.Dispatcher == nil {
		return nil, rpcInternalInvariantError("json: method dispatcher unavailable")
	}

	// The params field in the json method can be either:
	// - A JSON object (the params to forward directly)
	// - A JSON array with one element (XRPL-style params: [{...}])
	var forwardParams []byte
	if len(request.Params) > 0 {
		var arr []json.RawMessage
		if json.Unmarshal(request.Params, &arr) == nil && len(arr) > 0 {
			forwardParams = arr[0]
		} else {
			forwardParams = request.Params
		}
	}

	return ctx.Dispatcher.ExecuteMethod(ctx, request.Method, forwardParams)
}
