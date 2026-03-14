package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// JsonMethod handles the json RPC method.
// This is a proxy that forwards calls to other RPC methods.
// Reference: rippled JSON.cpp
type JsonMethod struct{ BaseHandler }

func (m *JsonMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if request.Method == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: method")
	}

	if types.Services == nil || types.Services.Dispatcher == nil {
		return nil, types.RpcErrorInternal("Method dispatcher not available")
	}

	// The params field in the json method can be either:
	// - A JSON object (the params to forward directly)
	// - A JSON array with one element (XRPL-style params: [{...}])
	var forwardParams []byte
	if len(request.Params) > 0 {
		// Check if it's an array
		var arr []json.RawMessage
		if json.Unmarshal(request.Params, &arr) == nil && len(arr) > 0 {
			forwardParams = arr[0]
		} else {
			forwardParams = request.Params
		}
	}

	return types.Services.Dispatcher.ExecuteMethod(request.Method, forwardParams)
}
