package rpc

import (
	"encoding/json"
	"io"
	"maps"
	"net/http"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// loadWarningOpts returns response options carrying warning:"load" when the
// dispatch crossed the resource warn threshold (recorded on ctx by
// finalizeLoad), and nil otherwise. Mirrors rippled attaching
// jr[warning]=load after the post-dispatch charge.
func loadWarningOpts(ctx *types.RpcContext) *jsonRPCResponseOptions {
	if ctx != nil && ctx.LoadWarning {
		return &jsonRPCResponseOptions{Warning: "load"}
	}
	return nil
}

// buildXrplResponseBody assembles one versioned JSON-RPC response. ripplerpc
// 1.x uses the legacy result envelope; 2.x and later move errors to the top
// level and preserve the request metadata alongside either result or error.
func buildXrplResponseBody(request any, result any, rpcErr *types.RpcError, opts *jsonRPCResponseOptions) map[string]any {
	response := make(map[string]any)
	ripplerpc, _ := ripplerpcVersion(request)

	var resultObj map[string]any
	if rpcErr != nil {
		resultObj = rpcErr.ResponseFields()
		resultObj["status"] = "error"
	} else if resultMap, ok := result.(map[string]any); ok {
		resultObj = make(map[string]any, len(resultMap)+1)
		maps.Copy(resultObj, resultMap)
		resultObj["status"] = "success"
	} else {
		resultObj = map[string]any{
			"status": "success",
			"data":   result,
		}
	}

	if opts != nil {
		// On the HTTP path rippled writes warning:"load" INTO result, before
		// wrapping it in the envelope (ServerHandler.cpp:919-920 → :938/:971);
		// the WS path keeps it top-level (:519) and is handled separately.
		if opts.Warning != "" {
			resultObj["warning"] = opts.Warning
		}
	}
	if rpcErr != nil && ripplerpc < "2.0" && request != nil {
		resultObj["request"] = request
	}
	if rpcErr != nil && ripplerpc >= "2.0" {
		if _, exists := resultObj["error_code"]; !exists {
			resultObj["error_code"] = nil
		}
		resultObj["code"] = resultObj["error_code"]
		resultObj["message"] = resultObj["error_message"]
		delete(resultObj, "error_message")
		response["error"] = resultObj
	} else {
		response["result"] = resultObj
	}

	if requestMap, ok := request.(map[string]any); ok {
		for _, key := range []string{"jsonrpc", "ripplerpc", "id"} {
			if value, exists := requestMap[key]; exists {
				response[key] = value
			}
		}
	}

	if opts != nil {
		if len(opts.Warnings) > 0 {
			response["warnings"] = opts.Warnings
		}
		if opts.Forwarded {
			response["forwarded"] = true
		}
	}

	return response
}
func (s *Server) writeXrplResponseWithOptions(w http.ResponseWriter, request any, result any, rpcErr *types.RpcError, opts *jsonRPCResponseOptions) {
	response := buildXrplResponseBody(request, result, rpcErr, opts)
	status := http.StatusOK
	ripplerpc, _ := ripplerpcVersion(request)
	if rpcErr != nil {
		if rpcErr.IsOverloaded() {
			status = http.StatusServiceUnavailable
		} else if ripplerpc >= "3.0" && rpcErr.ErrorException == "" && !rpcErr.IsBareToken() {
			status = rpcErrorHTTPStatus(rpcErr.Code)
		}
	}
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(status)
	if err := writeJSONRPCBody(w, response); err != nil {
		rpcLog().Error("Failed to encode response", "err", err)
	}
}
func writeJSONRPCBody(w io.Writer, response any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(response); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}
func rpcErrorHTTPStatus(code int) int {
	return types.RpcErrorHTTPStatus(code)
}
