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
		resultObj = map[string]any{
			"status": "error",
			"error":  rpcErr.ErrorString,
		}
		maps.Copy(resultObj, rpcErr.Extra)
		if rpcErr.ErrorException != "" {
			resultObj["error_exception"] = rpcErr.ErrorException
		} else if !rpcErr.IsBareToken() {
			resultObj["error_code"] = rpcErr.Code
			resultObj["error_message"] = rpcErr.Message
		}
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
	switch code {
	case types.RpcLGR_NOT_VALIDATED:
		return http.StatusAccepted
	case types.RpcHIGH_FEE:
		return http.StatusPaymentRequired
	case types.RpcBAD_SECRET, types.RpcBAD_SEED, types.RpcFORBIDDEN, types.RpcMASTER_DISABLED:
		return http.StatusForbidden
	case types.RpcBAD_MARKET, types.RpcDST_ACT_NOT_FOUND, types.RpcLGR_NOT_FOUND,
		types.RpcNO_PF_REQUEST, types.RpcOBJECT_NOT_FOUND, types.RpcSRC_ACT_NOT_FOUND,
		types.RpcDELEGATE_ACT_NOT_FOUND, types.RpcTXN_NOT_FOUND:
		return http.StatusNotFound
	case types.RpcNO_EVENTS, types.RpcMETHOD_NOT_FOUND:
		return http.StatusMethodNotAllowed
	case types.RpcSLOW_DOWN:
		return http.StatusTooManyRequests
	case types.RpcNO_PERMISSION:
		return http.StatusUnauthorized
	case types.RpcDB_DESERIALIZATION:
		return http.StatusBadGateway
	case types.RpcNOT_ENABLED, types.RpcNOT_IMPL, types.RpcNOT_SUPPORTED:
		return http.StatusNotImplemented
	case types.RpcAMENDMENT_BLOCKED, types.RpcEXPIRED_VALIDATOR_LIST, types.RpcNOT_READY,
		types.RpcNO_CLOSED, types.RpcNO_CURRENT, types.RpcNOT_SYNCED, types.RpcNO_NETWORK,
		types.RpcWRONG_NETWORK, types.RpcTOO_BUSY:
		return http.StatusServiceUnavailable
	case types.RpcBAD_FEATURE, types.RpcINTERNAL, types.RpcJSON_RPC:
		return http.StatusInternalServerError
	case types.RpcATX_DEPRECATED, types.RpcBAD_KEY_TYPE, types.RpcBAD_ISSUER,
		types.RpcBAD_SYNTAX, types.RpcCHANNEL_MALFORMED, types.RpcCHANNEL_AMT_MALFORMED,
		types.RpcMISSING_COMMAND, types.RpcDST_ACT_MALFORMED, types.RpcDST_ACT_MISSING,
		types.RpcDST_AMT_MALFORMED, types.RpcDST_AMT_MISSING, types.RpcDST_ISR_MALFORMED,
		types.RpcEXCESSIVE_LGR_RANGE, types.RpcINVALID_LGR_RANGE, types.RpcINVALID_PARAMS,
		types.RpcINVALID_HOTWALLET, types.RpcISSUE_MALFORMED, types.RpcLGR_IDXS_INVALID,
		types.RpcLGR_IDX_MALFORMED, types.RpcPUBLIC_MALFORMED, types.RpcSENDMAX_MALFORMED,
		types.RpcSIGNING_MALFORMED, types.RpcSRC_ACT_MALFORMED, types.RpcSRC_ACT_MISSING,
		types.RpcSRC_CUR_MALFORMED, types.RpcSRC_ISR_MALFORMED, types.RpcSTREAM_MALFORMED,
		types.RpcORACLE_MALFORMED, types.RpcBAD_CREDENTIALS, types.RpcTX_SIGNED,
		types.RpcDOMAIN_MALFORMED:
		return http.StatusBadRequest
	default:
		return http.StatusOK
	}
}
