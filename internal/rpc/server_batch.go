package rpc

import (
	"context"
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// dispatchBatchElement processes one element of a batch envelope and returns its
// response body. In batch mode rippled treats the element object itself as the
// request params ("params = jsonRPC", ServerHandler.cpp:681-683), with
// api_version taken from params[0] when present and otherwise from the
// element's top level (ServerHandler.cpp:668-683).
func (s *Server) dispatchBatchElement(el json.RawMessage, baseCtx context.Context, role types.Role, clientIP, resourceIP string) map[string]any {
	var elem map[string]any
	if err := decodeJSONUseNumber(el, &elem); err != nil || elem == nil {
		// Non-object element: echo it under "request" with a method_not_found
		// JSON-RPC error (ServerHandler.cpp:658-665).
		var raw any
		_ = decodeJSONUseNumber(el, &raw)
		return map[string]any{
			"request": redactJSONValue(raw),
			"error":   makeBatchJSONError(rpcMethodNotFoundCode, "Method not found"),
		}
	}
	ctx := newRpcContext(baseCtx, role, types.DefaultApiVersion, clientIP, s.loadPeerSource(), s.services)
	ctx.ResourceIP = resourceIP
	if ver, ok := apiVersionFromBatchElement(el); ok {
		ctx.ApiVersion = ver
	}
	if rpcErr := validateApiVersion(ctx); rpcErr != nil {
		echo := redactedRequestMap(elem)
		return map[string]any{
			"request": echo,
			"error":   makeBatchJSONError(types.WrongVersionJSONRPCCode, types.InvalidApiVersionToken),
		}
	}
	method, _ := elem["method"].(string)
	resolution := resolveMethod(s.registry, method, ctx.ApiVersion)
	if rpcErr := admitMethod(s.resourceManager, ctx, method, resolution, types.RpcErrorForbidden, true, rpcLog()); rpcErr != nil {
		if rpcErr.IsOverloaded() {
			return batchOverloadedElement(elem)
		}
		return batchForbiddenElement(elem)
	}
	defer ctx.ResourceAdmission.Finish(resource.FeeExceptionRPC(), method)

	// rippled validates the method field and emits a distinct message per
	// malformed shape, echoing the element's own fields at the top level
	// (ServerHandler.cpp:764-808).
	mv, present := elem["method"]
	if !present || mv == nil {
		chargeLoad(s.resourceManager, ctx, method, resource.FeeMalformedRPC(), rpcLog())
		return batchMalformedElement(elem, "Null method")
	}
	method, ok := mv.(string)
	if !ok {
		chargeLoad(s.resourceManager, ctx, method, resource.FeeMalformedRPC(), rpcLog())
		return batchMalformedElement(elem, "method is not string")
	}
	if method == "" {
		chargeLoad(s.resourceManager, ctx, method, resource.FeeMalformedRPC(), rpcLog())
		return batchMalformedElement(elem, "method is empty")
	}
	if _, valid := ripplerpcVersion(elem); !valid {
		chargeLoad(s.resourceManager, ctx, method, resource.FeeMalformedRPC(), rpcLog())
		return batchMalformedElement(elem, "ripplerpc is not a string")
	}

	result, rpcErr := dispatchResolvedMethod(s.resourceManager, s.services, ctx, method, el, resolution, rpcLog())

	echo := redactedRequestMap(elem)
	echo["command"] = method
	return buildXrplResponseBody(echo, result, rpcErr, loadWarningOpts(ctx))
}

// makeBatchJSONError mirrors rippled's make_json_error (ServerHandler.cpp:594-603):
// it returns {"error": {"code": code, "message": message}}. rippled assigns this
// whole object to the element's "error" field, so a malformed batch element's
// wire shape is the (intentional, rippled-faithful) double-nested
// {"error": {"error": {"code": ..., "message": ...}}}. Do not flatten it.
func makeBatchJSONError(code int, message string) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
}

// batchMalformedElement builds the reply for a method-less batch element: the
// element's own fields are echoed at the top level with credentials redacted
// and a method_not_found JSON-RPC error attached.
func batchMalformedElement(elem map[string]any, message string) map[string]any {
	r := redactedRequestMap(elem)
	r["error"] = makeBatchJSONError(rpcMethodNotFoundCode, message)
	return r
}

// batchForbiddenElement builds the reply for a batch element whose admin-only
// command is refused for a non-admin caller. Like rippled's FORBID branch
// (ServerHandler.cpp:758-760), the element's own fields are echoed at the top
// level with credentials redacted and a forbidden JSON-RPC error attached,
// rather than the XRPL result envelope.
func batchForbiddenElement(elem map[string]any) map[string]any {
	r := redactedRequestMap(elem)
	r["error"] = makeBatchJSONError(forbiddenJSONRPCCode, "Forbidden")
	return r
}

// batchOverloadedElement builds the reply for a batch element whose caller is
// over its per-IP resource budget. Like rippled's overload branch
// (ServerHandler.cpp:742-746), the element's own fields are echoed at the top
// level with credentials redacted and a server_overloaded JSON-RPC error
// attached, rather than the XRPL result envelope.
func batchOverloadedElement(elem map[string]any) map[string]any {
	r := redactedRequestMap(elem)
	r["error"] = makeBatchJSONError(serverOverloadedJSONRPCCode, "Server is overloaded")
	return r
}

// apiVersionFromBatchElement resolves a batch element's api_version, preferring
// params[0].api_version and falling back to a top-level api_version, mirroring
// rippled's two-level lookup (ServerHandler.cpp:668-683).
func apiVersionFromBatchElement(raw json.RawMessage) (int, bool) {
	var elem map[string]json.RawMessage
	if err := json.Unmarshal(raw, &elem); err != nil {
		return 0, false
	}
	nestedDefault := false
	if paramsRaw, exists := elem["params"]; exists {
		var params []json.RawMessage
		if err := json.Unmarshal(paramsRaw, &params); err == nil && len(params) > 0 {
			var first map[string]json.RawMessage
			if err := json.Unmarshal(params[0], &first); err == nil {
				value, present := first["api_version"]
				if present {
					version, valid := strictAPIVersion(value)
					if !valid {
						return types.ApiVersion1 - 1, true
					}
					if version != types.DefaultApiVersion {
						return version, true
					}
					nestedDefault = true
				}
			}
		}
	}
	if value, exists := elem["api_version"]; exists {
		version, valid := strictAPIVersion(value)
		if !valid {
			return types.ApiVersion1 - 1, true
		}
		return version, true
	}
	if nestedDefault {
		return types.DefaultApiVersion, true
	}
	return 0, false
}
