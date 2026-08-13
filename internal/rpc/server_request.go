package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func (s *Server) handlePostRequest(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writePlainHTTPError(w, http.StatusBadRequest, unableToParseRequest)
			return
		}
		rpcLog().Error("Failed to read request body", "err", err)
		s.writeXrplResponseWithOptions(w, nil, nil, rpcInternalError(), nil)
		return
	}

	// Decode the method up front and keep params as raw JSON: a batch envelope
	// carries params as an array of full request objects, while a single
	// request carries a one-element array, and rippled inspects the method
	// before deciding which shape params must take (ServerHandler.cpp:638-649).
	var request struct {
		Method json.RawMessage `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	// rippled answers a malformed single request with an HTTP 400, not a 200 +
	// JSON-RPC envelope (ServerHandler.cpp:629-635, :764-808): an unparseable
	// body, then the method field validated for the same three malformed shapes
	// the batch path distinguishes. The bodies are application/json (rippled's
	// HTTPReply quirk), not Go's http.Error text/plain default.
	if !validJSONObjectDocument(body) {
		writePlainHTTPError(w, http.StatusBadRequest, unableToParseRequest)
		return
	}
	if err := json.Unmarshal(body, &request); err != nil {
		writePlainHTTPError(w, http.StatusBadRequest, unableToParseRequest)
		return
	}

	var method string
	_ = json.Unmarshal(request.Method, &method)

	portCtx := GetPortContext(r.Context())
	peerIP := remoteAddrIP(r.RemoteAddr)
	clientIP := resolveClientIP(r, portCtx)
	user := userHeader(r)
	// Role is derived from the socket-level peer, not header-supplied IPs,
	// so an X-Real-IP / X-Forwarded-For header from an untrusted client
	// can't elevate the caller to admin. Matches rippled's
	// requestRole, which uses the connection's remote endpoint. The role and
	// client IP are anchored to the connection; each batch element still
	// supplies its own configured admin credentials.
	dispatchCtx, cancel := s.withTimeout(r.Context())
	defer cancel()

	// rippled accepts a batch envelope — {"method":"batch","params":[ {...}, ... ]}
	// — dispatching each element as an independent request and returning a JSON
	// array of replies (ServerHandler.cpp:638-683). params must be an array;
	// missing, null, or non-array is HTTP 400 "Malformed batch request"
	// (ServerHandler.cpp:643-647). An empty array is valid: size is 0, the loop
	// runs zero times, and the reply is an empty array (ServerHandler.cpp:648-653).
	if method == "batch" {
		var elements []json.RawMessage
		// A JSON null params leaves elements nil with no error, which rippled
		// rejects as "not an array"; an empty [] unmarshals to a non-nil empty
		// slice and is accepted.
		if err := json.Unmarshal(request.Params, &elements); err != nil || elements == nil {
			writePlainHTTPError(w, http.StatusBadRequest, "Malformed batch request")
			return
		}
		// Defensive cap (go-xrpl superset of rippled): reject an oversized batch
		// whole rather than amplifying one request into hundreds of ledger reads.
		if len(elements) > maxBatchElements {
			writePlainHTTPError(w, http.StatusBadRequest, "Malformed batch request")
			return
		}
		replies := make([]map[string]any, len(elements))
		for i, el := range elements {
			role := roleForRequest(peerIP, user, roleParamsFromBatchElement(el), portCtx)
			replies[i] = s.dispatchBatchElement(el, dispatchCtx, role, clientIP, peerIP)
		}
		w.Header().Set("Content-Type", jsonContentType)
		w.WriteHeader(http.StatusOK)
		if err := writeJSONRPCBody(w, replies); err != nil {
			rpcLog().Error("Failed to encode batch response", "err", err)
		}
		return
	}

	role := roleForRequest(peerIP, user, roleParamsFromRawParams(request.Params), portCtx)
	apiVersion := types.DefaultApiVersion
	if version, present := apiVersionFromSingleParams(request.Params); present {
		apiVersion = version
	}
	versionCtx := newRpcContext(dispatchCtx, role, apiVersion, clientIP, s.loadPeerSource(), s.services, s, s.urlSubscriptions)
	versionCtx.ResourceIP = peerIP
	if rpcErr := validateApiVersion(versionCtx); rpcErr != nil {
		writeInvalidApiVersionHTTP(w)
		return
	}
	ctx := versionCtx
	resolution := resolveMethod(s.registry, method, ctx.ApiVersion)
	if rpcErr := admitMethod(s.resourceManager, ctx, method, resolution, rpcerrors.RpcErrorForbidden, true, rpcLog()); rpcErr != nil {
		if rpcErr.IsOverloaded() {
			writePlainHTTPError(w, http.StatusServiceUnavailable, "Server is overloaded")
		} else {
			writePlainHTTPError(w, http.StatusForbidden, "Forbidden")
		}
		return
	}
	defer ctx.ResourceAdmission.Finish(resource.FeeExceptionRPC(), method)

	var methodErr string
	method, methodErr = decodeMethodField(request.Method)
	if methodErr != "" {
		chargeLoad(s.resourceManager, ctx, method, resource.FeeMalformedRPC(), rpcLog())
		writePlainHTTPError(w, http.StatusBadRequest, methodErr)
		return
	}

	// XRPL JSON-RPC uses params as an array with a single object.
	params := json.RawMessage("{}")
	if len(request.Params) > 0 && !rawJSONNull(request.Params) {
		var arr []json.RawMessage
		if err := json.Unmarshal(request.Params, &arr); err != nil || len(arr) != 1 {
			chargeLoad(s.resourceManager, ctx, method, resource.FeeMalformedRPC(), rpcLog())
			writePlainHTTPError(w, http.StatusBadRequest, "params unparseable")
			return
		}
		params = arr[0]
		if !rawJSONNull(params) {
			var object map[string]any
			if err := json.Unmarshal(params, &object); err != nil || object == nil {
				chargeLoad(s.resourceManager, ctx, method, resource.FeeMalformedRPC(), rpcLog())
				writePlainHTTPError(w, http.StatusBadRequest, "params unparseable")
				return
			}
		}
	}

	requestObj := buildRequestEcho(method, params)
	if _, valid := ripplerpcVersion(requestObj); !valid {
		chargeLoad(s.resourceManager, ctx, method, resource.FeeMalformedRPC(), rpcLog())
		writePlainHTTPError(w, http.StatusBadRequest, "ripplerpc is not a string")
		return
	}

	result, rpcErr := dispatchResolvedMethod(s.resourceManager, s.services, ctx, method, params, resolution, rpcLog())

	s.writeXrplResponseWithOptions(w, requestObj, result, rpcErr, loadWarningOpts(ctx))
}
func rawJSONNull(value json.RawMessage) bool {
	return strings.TrimSpace(string(value)) == "null"
}
func decodeJSONUseNumber(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	switch out := target.(type) {
	case *map[string]any:
		normalized, err := normalizeJSONValue(*out)
		if err != nil {
			return err
		}
		if normalized == nil {
			*out = nil
		} else {
			*out = normalized.(map[string]any)
		}
	case *any:
		normalized, err := normalizeJSONValue(*out)
		if err != nil {
			return err
		}
		*out = normalized
	}
	return nil
}
func validJSONObjectDocument(data []byte) bool {
	var value any
	if err := decodeJSONUseNumber(data, &value); err != nil {
		return false
	}
	object, ok := value.(map[string]any)
	return ok && object != nil
}
func (value jsonReal) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(value), 'g', 16, 64)), nil
}
func normalizeJSONValue(value any) (any, error) {
	return normalizeJSONValueAtDepth(value, 0)
}
func normalizeJSONValueAtDepth(value any, depth int) (any, error) {
	if depth > 25 {
		return nil, errors.New("maximum JSON nesting depth exceeded")
	}
	switch value := value.(type) {
	case json.Number:
		raw := value.String()
		if strings.ContainsAny(raw, ".eE") {
			number, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid JSON number %q: %w", raw, err)
			}
			return jsonReal(number), nil
		}
		if strings.HasPrefix(raw, "-") {
			number, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || number < -1<<31 {
				return nil, fmt.Errorf("JSON integer %q exceeds the allowable range", raw)
			}
			return int32(number), nil
		}
		number, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || number > 1<<32-1 {
			return nil, fmt.Errorf("JSON integer %q exceeds the allowable range", raw)
		}
		if number <= 1<<31-1 {
			return int32(number), nil
		}
		return uint32(number), nil
	case map[string]any:
		for key, item := range value {
			normalized, err := normalizeJSONValueAtDepth(item, depth+1)
			if err != nil {
				return nil, err
			}
			value[key] = normalized
		}
	case []any:
		for i, item := range value {
			normalized, err := normalizeJSONValueAtDepth(item, depth+1)
			if err != nil {
				return nil, err
			}
			value[i] = normalized
		}
	}
	return value, nil
}

// writeInvalidApiVersionHTTP mirrors rippled's HTTP-single rejection of an
// unsupported api_version: HTTP 400 with the bare token as the response body
// (ServerHandler.cpp:689 → HTTPReply(400, ...)). The body carries no JSON
// envelope, matching rippled's plain-string reply.
func writeInvalidApiVersionHTTP(w http.ResponseWriter) {
	writePlainHTTPError(w, http.StatusBadRequest, rpcerrors.InvalidApiVersionToken)
}

// writePlainHTTPError mirrors rippled's HTTPReply(status, content): a bare
// string body labelled application/json (rippled labels even non-JSON error
// strings that way) terminated with CRLF (JSONRPCUtil.cpp:148-158), rather
// than Go's http.Error text/plain + LF default.
func writePlainHTTPError(w http.ResponseWriter, status int, content string) {
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(status)
	if _, err := io.WriteString(w, content+"\r\n"); err != nil {
		rpcLog().Error("Failed to write HTTP error response", "err", err)
	}
}

// decodeMethodField validates the HTTP-single "method" field the same way the
// batch path does, mirroring rippled's three distinct messages
// (ServerHandler.cpp:764-808): a missing/null method → "Null method", a
// non-string method → "method is not string", an empty string → "method is
// empty". On success it returns the method name and an empty errMsg.
func decodeMethodField(raw json.RawMessage) (method string, errMsg string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", "Null method"
	}
	if err := json.Unmarshal(raw, &method); err != nil {
		return "", "method is not string"
	}
	if method == "" {
		return "", "method is empty"
	}
	return method, ""
}
func apiVersionFromObject(obj json.RawMessage) (int, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(obj, &fields); err != nil {
		return 0, false
	}
	raw, present := fields["api_version"]
	if !present {
		return 0, false
	}
	version, valid := strictAPIVersion(raw)
	if !valid {
		return types.ApiVersion1 - 1, true
	}
	return version, true
}
func apiVersionFromSingleParams(paramsRaw json.RawMessage) (int, bool) {
	var params []json.RawMessage
	if err := json.Unmarshal(paramsRaw, &params); err != nil || len(params) == 0 {
		return 0, false
	}
	return apiVersionFromObject(params[0])
}
func strictAPIVersion(raw json.RawMessage) (int, bool) {
	var version *int
	if err := json.Unmarshal(raw, &version); err != nil || version == nil {
		return 0, false
	}
	return *version, true
}

// buildRequestEcho builds the request echo attached to error responses, masking
// credentials before the echo leaves the process.
func buildRequestEcho(method string, params json.RawMessage) map[string]any {
	if params != nil {
		var reqMap map[string]any
		// params may unmarshal to JSON null, which yields a nil map.
		if err := decodeJSONUseNumber(params, &reqMap); err == nil && reqMap != nil {
			reqMap = redactedRequestMap(reqMap)
			reqMap["command"] = method
			return reqMap
		}
	}
	return map[string]any{"command": method}
}
func ripplerpcVersion(request any) (string, bool) {
	requestMap, ok := request.(map[string]any)
	if !ok {
		return "1.0", true
	}
	value, exists := requestMap["ripplerpc"]
	if !exists {
		return "1.0", true
	}
	version, ok := value.(string)
	return version, ok
}
