package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/loadtrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
)

// MaxRequestBytes caps the size of a single JSON-RPC request body.
// Matches rippled's RPC::Tuning::maxRequestSize (Tuning.h) exactly so
// goxrpl and rippled reject the same oversized payloads on the wire.
const MaxRequestBytes = 1_000_000

// MaxBatchElements caps one batch envelope to prevent request amplification.
const MaxBatchElements = 256

const jsonContentType = "application/json; charset=UTF-8"

// The size-only rippled rejection has no parser detail. Use that same fixed
// response for every parse-boundary failure so Go decoder diagnostics and
// request fragments never reach clients.
const unableToParseRequest = "Unable to parse request: "

// rpcLog returns the logger for the HTTP JSON-RPC server.
// Resolved lazily so it picks up the root logger set during CLI bootstrap.
func rpcLog() xrpllog.Logger { return xrpllog.Named(xrpllog.PartitionRPC) }

type Server struct {
	peerSourceHolder
	registry    *types.MethodRegistry
	timeout     time.Duration
	services    *types.ServiceContainer
	loadTracker *loadtrack.Tracker

	// corsAllowedOrigins, if non-empty, restricts Access-Control-Allow-Origin
	// to the listed origins (set via SetCORSAllowedOrigins). Empty means
	// `*` — a deliberate goxrpl divergence from rippled, which emits no
	// CORS header at all (JSONRPCUtil.cpp:143-145 leaves the
	// Access-Control-Allow-Origin line commented out). Browser clients
	// won't work cross-origin against a vanilla rippled; emitting `*` by
	// default keeps the goxrpl HTTP endpoint usable from web tools.
	corsMu             sync.RWMutex
	corsAllowedOrigins []string
}

var _ types.MethodDispatcher = (*Server)(nil)

// peerSourceHolder is the atomic peer-source slot embedded by both the HTTP
// and WebSocket servers, exposed through the `peers` RPC. Embedding it gives
// both the same SetPeerSource / loadPeerSource without duplicating the field
// and methods on each server.
type peerSourceHolder struct {
	peerSource atomic.Pointer[types.PeerSource]
}

// SetPeerSource registers the source of per-peer entries served by the
// `peers` RPC handler. Passing nil detaches the source so the handler
// returns an empty list. Safe to call concurrently with reads.
func (h *peerSourceHolder) SetPeerSource(src types.PeerSource) {
	if src == nil {
		h.peerSource.Store(nil)
		return
	}
	h.peerSource.Store(&src)
}

func (h *peerSourceHolder) loadPeerSource() types.PeerSource {
	if p := h.peerSource.Load(); p != nil {
		return *p
	}
	return nil
}

// SetCORSAllowedOrigins replaces the list of origins accepted for CORS.
// Pass nil/empty to fall back to `*` (the goxrpl default — rippled emits
// no CORS header at all). Origins are matched exactly against the
// request's Origin header; a leading wildcard `*` in the list keeps the
// permissive behaviour. Safe to call after the server has started.
func (s *Server) SetCORSAllowedOrigins(origins []string) {
	s.corsMu.Lock()
	defer s.corsMu.Unlock()
	if len(origins) == 0 {
		s.corsAllowedOrigins = nil
		return
	}
	s.corsAllowedOrigins = append(s.corsAllowedOrigins[:0:0], origins...)
}

// resolveCORSOrigin returns the value to echo in
// Access-Control-Allow-Origin. When no allowlist is configured the legacy
// `*` is returned; otherwise the request's Origin is echoed only when it
// matches an entry (or `*` is in the list), so misconfigured browsers
// don't get a cross-origin pass.
func (s *Server) resolveCORSOrigin(requestOrigin string) string {
	s.corsMu.RLock()
	defer s.corsMu.RUnlock()
	if len(s.corsAllowedOrigins) == 0 {
		return "*"
	}
	for _, o := range s.corsAllowedOrigins {
		if o == "*" {
			return "*"
		}
		if o == requestOrigin {
			return requestOrigin
		}
	}
	return ""
}

// NewServer creates a new RPC server with the given timeout and the
// service container handlers will read through ctx.Services. The
// container may be nil for test contexts that exercise routing only.
func NewServer(timeout time.Duration, services *types.ServiceContainer) *Server {
	return NewServerWithLoadTracker(timeout, services, nil)
}

// NewServerWithLoadTracker creates a server using tracker for transport-level
// admission and charging. A nil tracker preserves NewServer's standalone
// default.
func NewServerWithLoadTracker(timeout time.Duration, services *types.ServiceContainer, tracker *loadtrack.Tracker) *Server {
	if tracker == nil {
		tracker = loadtrack.New()
	}
	server := &Server{
		registry:    types.NewMethodRegistry(),
		timeout:     timeout,
		services:    services,
		loadTracker: tracker,
	}

	if services != nil && services.ClientLoad == nil {
		services.ClientLoad = types.NewClientLoadShedder()
	}

	server.registerAllMethods()

	return server
}

// Services returns the service container wired to this server. Used by
// callers that need to attach the dispatcher (this server itself) or
// the shutdown hook after construction.
func (s *Server) Services() *types.ServiceContainer { return s.services }

// JsonRpcResponseOptions contains optional fields for JSON-RPC responses
// These fields are at the top level, not inside the result object
type JsonRpcResponseOptions struct {
	Warning   string                // "load" when approaching rate limit
	Warnings  []types.WarningObject // Array of warning objects
	Forwarded bool                  // True if forwarded from Clio to P2P server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			rpcLog().Error("rpc transport panic", "err", rec, "stack", string(debug.Stack()), "method", r.Method, "remote", r.RemoteAddr)
			s.writeXrplResponseWithOptions(w, nil, nil, rpcInternalError(), nil)
		}
	}()

	// Set CORS headers. Default is `*` (goxrpl divergence from rippled,
	// which emits no CORS header — see Server.corsAllowedOrigins comment).
	// An explicit allowlist may be configured via SetCORSAllowedOrigins,
	// in which case we echo back the request's Origin only when it is on
	// the list.
	if allow := s.resolveCORSOrigin(r.Header.Get("Origin")); allow != "" {
		w.Header().Set("Access-Control-Allow-Origin", allow)
		if allow != "*" {
			w.Header().Set("Vary", "Origin")
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", jsonContentType)

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "POST" && r.Method != "GET" {
		writePlainHTTPError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	if r.Method == "GET" {
		s.handleGetRequest(w, r)
		return
	}

	s.handlePostRequest(w, r)
}

func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	method := query.Get("command")

	// Deliberate goxrpl extension: a GET with no `command` defaults to
	// server_info. rippled has no GET form (it feeds every request body
	// through the JSON parser); this keeps the endpoint browser-pokeable,
	// like the permissive default CORS header above.
	if method == "" {
		method = "server_info"
	}

	portCtx := GetPortContext(r.Context())
	peerIP := remoteAddrIP(r.RemoteAddr)
	clientIP := resolveClientIP(r, portCtx)
	user := userHeader(r)
	role := roleForRequest(peerIP, user, nil, portCtx)
	dispatchCtx, cancel := s.withTimeout(r.Context())
	defer cancel()
	ctx := newRpcContext(dispatchCtx, role, types.DefaultApiVersion, clientIP, s.loadPeerSource(), s.services)

	result, rpcErr := s.executeMethod(method, nil, ctx)
	s.writeXrplResponseWithOptions(w, nil, result, rpcErr, loadWarningOpts(ctx))
}

func (s *Server) handlePostRequest(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)

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
		if len(elements) > MaxBatchElements {
			http.Error(w, "Malformed batch request", http.StatusBadRequest)
			return
		}
		replies := make([]map[string]any, len(elements))
		for i, el := range elements {
			role := roleForRequest(peerIP, user, roleParamsFromBatchElement(el), portCtx)
			replies[i] = s.dispatchBatchElement(el, dispatchCtx, role, clientIP)
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
	versionCtx := newRpcContext(dispatchCtx, role, apiVersion, clientIP, s.loadPeerSource(), s.services)
	if rpcErr := validateApiVersion(versionCtx); rpcErr != nil {
		writeInvalidApiVersionHTTP(w)
		return
	}
	ctx := versionCtx
	resolution := resolveMethod(s.registry, method, ctx.ApiVersion)
	if rpcErr := admitMethod(s.loadTracker, ctx, method, resolution, types.RpcErrorForbidden, true, rpcLog()); rpcErr != nil {
		if rpcErr.IsOverloaded() {
			writePlainHTTPError(w, http.StatusServiceUnavailable, "Server is overloaded")
		} else {
			writePlainHTTPError(w, http.StatusForbidden, "Forbidden")
		}
		return
	}

	var methodErr string
	method, methodErr = decodeMethodField(request.Method)
	if methodErr != "" {
		chargeLoad(s.loadTracker, ctx, method, loadtrack.LoadMalformed, rpcLog())
		writePlainHTTPError(w, http.StatusBadRequest, methodErr)
		return
	}

	// XRPL JSON-RPC uses params as an array with a single object.
	params := json.RawMessage("{}")
	if len(request.Params) > 0 && !rawJSONNull(request.Params) {
		var arr []json.RawMessage
		if err := json.Unmarshal(request.Params, &arr); err != nil || len(arr) != 1 {
			chargeLoad(s.loadTracker, ctx, method, loadtrack.LoadMalformed, rpcLog())
			writePlainHTTPError(w, http.StatusBadRequest, "params unparseable")
			return
		}
		params = arr[0]
		if !rawJSONNull(params) {
			var object map[string]any
			if err := json.Unmarshal(params, &object); err != nil || object == nil {
				chargeLoad(s.loadTracker, ctx, method, loadtrack.LoadMalformed, rpcLog())
				writePlainHTTPError(w, http.StatusBadRequest, "params unparseable")
				return
			}
		}
	}

	requestObj := buildRequestEcho(method, params)
	if _, valid := ripplerpcVersion(requestObj); !valid {
		chargeLoad(s.loadTracker, ctx, method, loadtrack.LoadMalformed, rpcLog())
		writePlainHTTPError(w, http.StatusBadRequest, "ripplerpc is not a string")
		return
	}

	result, rpcErr := dispatchResolvedMethod(s.loadTracker, s.services, ctx, method, params, resolution, rpcLog())

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

type jsonReal float64

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
	writePlainHTTPError(w, http.StatusBadRequest, types.InvalidApiVersionToken)
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

// dispatchBatchElement processes one element of a batch envelope and returns its
// response body. In batch mode rippled treats the element object itself as the
// request params ("params = jsonRPC", ServerHandler.cpp:681-683), with
// api_version taken from params[0] when present and otherwise from the
// element's top level (ServerHandler.cpp:668-683).
func (s *Server) dispatchBatchElement(el json.RawMessage, baseCtx context.Context, role types.Role, clientIP string) map[string]any {
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
	if rpcErr := admitMethod(s.loadTracker, ctx, method, resolution, types.RpcErrorForbidden, true, rpcLog()); rpcErr != nil {
		if rpcErr.IsOverloaded() {
			return batchOverloadedElement(elem)
		}
		return batchForbiddenElement(elem)
	}

	// rippled validates the method field and emits a distinct message per
	// malformed shape, echoing the element's own fields at the top level
	// (ServerHandler.cpp:764-808).
	mv, present := elem["method"]
	if !present || mv == nil {
		chargeLoad(s.loadTracker, ctx, method, loadtrack.LoadMalformed, rpcLog())
		return batchMalformedElement(elem, "Null method")
	}
	method, ok := mv.(string)
	if !ok {
		chargeLoad(s.loadTracker, ctx, method, loadtrack.LoadMalformed, rpcLog())
		return batchMalformedElement(elem, "method is not string")
	}
	if method == "" {
		chargeLoad(s.loadTracker, ctx, method, loadtrack.LoadMalformed, rpcLog())
		return batchMalformedElement(elem, "method is empty")
	}
	if _, valid := ripplerpcVersion(elem); !valid {
		chargeLoad(s.loadTracker, ctx, method, loadtrack.LoadMalformed, rpcLog())
		return batchMalformedElement(elem, "ripplerpc is not a string")
	}

	result, rpcErr := dispatchResolvedMethod(s.loadTracker, s.services, ctx, method, el, resolution, rpcLog())

	echo := redactedRequestMap(elem)
	echo["command"] = method
	return buildXrplResponseBody(echo, result, rpcErr, loadWarningOpts(ctx))
}

// rpcMethodNotFoundCode is the JSON-RPC error code rippled attaches to malformed
// batch elements (ServerHandler.cpp:605, method_not_found = -32601). It is
// distinct from go-xrpl's XRPL-token error model and appears only inside the
// batch malformed-element replies, to match rippled byte-for-byte.
const rpcMethodNotFoundCode = -32601

// forbiddenJSONRPCCode is the JSON-RPC error code rippled attaches to a batch
// element refused at the role layer (ServerHandler.cpp:607, forbidden = -32605).
// It is distinct from method_not_found and appears only inside batch
// forbidden-element replies, matching rippled byte-for-byte.
const forbiddenJSONRPCCode = -32605

// serverOverloadedJSONRPCCode is the JSON-RPC error code rippled attaches to a
// batch element whose caller is over its per-IP resource budget
// (ServerHandler.cpp:606, server_overloaded = -32604). It appears only inside
// batch overload-element replies, matching rippled byte-for-byte.
const serverOverloadedJSONRPCCode = -32604

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

func (s *Server) withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if s.timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, s.timeout)
}

// newRpcContext assembles an RpcContext, deriving IsAdmin / Unlimited from the
// role so every HTTP/WS dispatch site computes them identically.
func newRpcContext(ctx context.Context, role types.Role, apiVersion int, clientIP string, peers types.PeerSource, services *types.ServiceContainer) *types.RpcContext {
	return &types.RpcContext{
		Context:    ctx,
		Role:       role,
		ApiVersion: apiVersion,
		IsAdmin:    role == types.RoleAdmin,
		Unlimited:  role.IsUnlimited(),
		ClientIP:   clientIP,
		PeerSource: peers,
		Services:   services,
	}
}

// credentialKeys are request fields masked in error envelopes. Matching is
// case-insensitive and recursive so alternate client spellings cannot bypass
// redaction.
var credentialKeys = []string{
	"secret",
	"seed",
	"passphrase",
	"seed_hex",
	"seedhex",
	"password",
	"url_password",
	"urlpassword",
	"admin_user",
	"adminuser",
	"admin_password",
	"adminpassword",
}

// maskedValue is the literal rippled writes in place of credential
// values (ServerHandler.cpp:536). Masking preserves the key so a
// debugging client can see a credential was supplied.
const maskedValue = "<masked>"

func redactedRequestMap(request map[string]any) map[string]any {
	if request == nil {
		return nil
	}
	return redactJSONValue(request).(map[string]any)
}

func redactJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(value))
		for key, item := range value {
			if isCredentialKey(key) {
				redacted[key] = maskedValue
				continue
			}
			if strings.EqualFold(key, "url") {
				if rawURL, ok := item.(string); ok && urlContainsUserinfo(rawURL) {
					redacted[key] = maskedValue
					continue
				}
			}
			redacted[key] = redactJSONValue(item)
		}
		return redacted
	case []any:
		redacted := make([]any, len(value))
		for i, item := range value {
			redacted[i] = redactJSONValue(item)
		}
		return redacted
	default:
		return value
	}
}

func urlContainsUserinfo(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.User != nil {
		return true
	}

	authority := rawURL
	if _, after, ok := strings.Cut(authority, "://"); ok {
		authority = after
	} else if strings.HasPrefix(authority, "//") {
		authority = authority[2:]
	} else {
		return false
	}
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	return strings.Contains(authority, "@")
}

func isCredentialKey(key string) bool {
	for _, credentialKey := range credentialKeys {
		if strings.EqualFold(key, credentialKey) {
			return true
		}
	}
	return false
}

func rpcInternalError() *types.RpcError {
	return types.RpcErrorInternal()
}

func (s *Server) executeMethod(method string, params json.RawMessage, ctx *types.RpcContext) (any, *types.RpcError) {
	clientIP := ""
	if ctx != nil {
		clientIP = ctx.ClientIP
	}
	rpcLog().Debug("rpc", "method", method, "client", clientIP)
	// Both transports signal a forbidden admin-only command via RpcErrorForbidden.
	// rippled resolves it at the role layer (Role::FORBID) ahead of the handler
	// and renders it per transport — HTTP single 403 "Forbidden", batch
	// make_json_error(forbidden), WS rpcError(rpcFORBIDDEN) (ServerHandler.cpp:482-486,
	// 750-762). The writers special-case IsForbidden; in-handler permission
	// denials keep returning rpcNO_PERMISSION on the normal result envelope.
	return dispatchMethod(s.registry, s.loadTracker, s.services, ctx, method, params, types.RpcErrorForbidden, rpcLog())
}

type methodResolution struct {
	handler  types.MethodHandler
	resolved bool
}

func resolveMethod(registry *types.MethodRegistry, method string, apiVersion int) methodResolution {
	handler, exists := registry.Get(method)
	return methodResolution{
		handler:  handler,
		resolved: exists && handlerSupportsVersion(handler, apiVersion),
	}
}

func dispatchMethod(
	registry *types.MethodRegistry,
	tracker *loadtrack.Tracker,
	services *types.ServiceContainer,
	ctx *types.RpcContext,
	method string,
	params json.RawMessage,
	adminGate func(string) *types.RpcError,
	log xrpllog.Logger,
) (any, *types.RpcError) {
	if rpcErr := validateApiVersion(ctx); rpcErr != nil {
		return nil, rpcErr
	}
	resolution := resolveMethod(registry, method, ctx.ApiVersion)
	if rpcErr := admitMethod(tracker, ctx, method, resolution, adminGate, true, log); rpcErr != nil {
		return nil, rpcErr
	}
	return dispatchResolvedMethod(tracker, services, ctx, method, params, resolution, log)
}

func admitMethod(
	tracker *loadtrack.Tracker,
	ctx *types.RpcContext,
	method string,
	resolution methodResolution,
	adminGate func(string) *types.RpcError,
	checkLoad bool,
	log xrpllog.Logger,
) *types.RpcError {
	if checkLoad {
		if rpcErr := gateLoad(tracker, ctx, method, log); rpcErr != nil {
			return rpcErr
		}
	}
	if resolution.resolved && resolution.handler.RequiredRole() == types.RoleAdmin && ctx.Role != types.RoleAdmin {
		rpcErr := adminGate(method)
		chargeLoad(tracker, ctx, method, loadtrack.LoadMalformed, log)
		return rpcErr
	}
	return nil
}

func dispatchResolvedMethod(
	tracker *loadtrack.Tracker,
	services *types.ServiceContainer,
	ctx *types.RpcContext,
	method string,
	params json.RawMessage,
	resolution methodResolution,
	log xrpllog.Logger,
) (any, *types.RpcError) {
	ctx.LoadCost = loadtrack.ChargeReference
	ctx.LoadWarning = false

	if rpcErr := handlers.RequireNotBusyClient(ctx); rpcErr != nil {
		finalizeLoad(tracker, ctx, method, loadtrack.LoadReference, log)
		return nil, rpcErr
	}

	if !resolution.resolved {
		rpcErr := types.RpcErrorMethodNotFound()
		finalizeLoad(tracker, ctx, method, loadtrack.LoadReference, log)
		return nil, rpcErr
	}

	if rpcErr := conditionMet(resolution.handler.RequiredCondition(), ctx); rpcErr != nil {
		finalizeLoad(tracker, ctx, method, loadtrack.LoadReference, log)
		return nil, rpcErr
	}

	if services != nil && services.ClientLoad != nil {
		services.ClientLoad.Begin()
		defer services.ClientLoad.End()
	}
	result, rpcErr, recovered := invokeHandler(resolution.handler, ctx, params, method, log)
	kind := loadtrack.LoadKind(ctx.LoadCost)
	if recovered && kind == loadtrack.LoadReference {
		kind = loadtrack.LoadException
	}
	finalizeLoad(tracker, ctx, method, kind, log)
	return result, rpcErr
}

func dispatchNestedMethod(
	registry *types.MethodRegistry,
	ctx *types.RpcContext,
	method string,
	params json.RawMessage,
	log xrpllog.Logger,
) (any, *types.RpcError) {
	if rpcErr := validateApiVersion(ctx); rpcErr != nil {
		return nil, rpcErr
	}
	resolution := resolveMethod(registry, method, ctx.ApiVersion)
	if !resolution.resolved {
		return nil, types.RpcErrorMethodNotFound()
	}
	if resolution.handler.RequiredRole() == types.RoleAdmin && ctx.Role != types.RoleAdmin {
		ctx.LoadCost = loadtrack.ChargeMalformed
		return nil, types.RpcErrorForbidden(method)
	}
	if rpcErr := conditionMet(resolution.handler.RequiredCondition(), ctx); rpcErr != nil {
		return nil, rpcErr
	}
	result, rpcErr, recovered := invokeHandler(resolution.handler, ctx, params, method, log)
	if recovered && ctx.LoadCost == loadtrack.ChargeReference {
		ctx.LoadCost = loadtrack.ChargeException
	}
	return result, rpcErr
}

func invokeHandler(handler types.MethodHandler, ctx *types.RpcContext, params json.RawMessage, method string, log xrpllog.Logger) (result any, rpcErr *types.RpcError, recovered bool) {
	defer func() {
		if rec := recover(); rec != nil {
			clientIP := ""
			if ctx != nil {
				clientIP = ctx.ClientIP
			}
			log.Error("rpc handler panic", "err", rec, "stack", string(debug.Stack()), "method", method, "client", clientIP)
			result = nil
			rpcErr = rpcInternalError()
			recovered = true
		}
	}()
	result, rpcErr = handler.Handle(ctx, redactStructuredID(params))
	return result, rpcErr, false
}

func redactStructuredID(params json.RawMessage) json.RawMessage {
	if len(params) == 0 {
		return params
	}
	var request map[string]any
	if err := decodeJSONUseNumber(params, &request); err != nil || request == nil {
		return params
	}
	id, ok := request["id"]
	if !ok {
		return params
	}
	switch id.(type) {
	case map[string]any, []any:
		request["id"] = redactJSONValue(id)
	default:
		return params
	}
	redacted, err := json.Marshal(request)
	if err != nil {
		return params
	}
	return redacted
}

// betaEnabled reports whether the operator turned on the beta RPC API for
// this request. nil-safe: a request without a service container (routing-only
// tests) is treated as non-beta.
func betaEnabled(ctx *types.RpcContext) bool {
	return ctx.Services != nil && ctx.Services.BetaRPCAPI
}

// validateApiVersion enforces the accepted api_version range, mirroring
// rippled's dispatch-layer cap (getAPIVersionNumber rejecting anything above
// apiBetaVersion when beta is off, ServerHandler.cpp:685-695). It is handler-
// independent: a version outside the range yields invalid_API_version ahead of
// command resolution, exactly as rippled rejects it before reaching a handler.
// The narrower per-handler support set is enforced separately as part of
// command resolution — see handlerSupportsVersion.
func validateApiVersion(ctx *types.RpcContext) *types.RpcError {
	maxVersion := types.MaxSupportedApiVersion
	if betaEnabled(ctx) {
		maxVersion = types.BetaApiVersion
	}
	if ctx.ApiVersion < types.ApiVersion1 || ctx.ApiVersion > maxVersion {
		return types.RpcErrorInvalidApiVersion(strconv.Itoa(ctx.ApiVersion))
	}
	return nil
}

// handlerSupportsVersion reports whether the handler serves the requested
// api_version; a handler listing no versions serves every in-range version.
// rippled folds this per-handler range match into getHandler (Handler.cpp:265-
// 272): a name match whose version falls outside the handler's range returns
// null, which the caller treats as an unknown command — never as an invalid
// api_version (that error is reserved for the out-of-range dispatch-layer cap).
func handlerSupportsVersion(handler types.MethodHandler, version int) bool {
	supportedVersions := handler.SupportedApiVersions()
	return len(supportedVersions) == 0 || slices.Contains(supportedVersions, version)
}

// maxValidatedLedgerAge mirrors rippled's Tuning::maxValidatedLedgerAge
// (2 minutes): a non-standalone node whose validated ledger is older than this
// is treated as out of sync.
const maxValidatedLedgerAge = 120 * time.Second

// conditionMet mirrors rippled's RPC::conditionMet (Handler.h:78-139). A method
// whose RequiredCondition is NoCondition is always allowed. Otherwise the node
// must be usable: not amendment-blocked, at least SYNCING, not lagging the
// validated ledger (the age / current-vs-valid checks are skipped in
// standalone), and holding a closed ledger.
//
// On failure it returns the apiVersion-1 code (rpcNO_NETWORK / rpcNO_CURRENT /
// rpcNO_CLOSED) and rpcNOT_SYNCED for later versions, matching rippled. The
// rpcEXPIRED_VALIDATOR_LIST branch fires when the UNL is blocked, driven by the
// optional ServiceContainer.UNLBlocked signal (nil ⇒ never blocked).
func conditionMet(cond types.Condition, ctx *types.RpcContext) *types.RpcError {
	if cond == types.NoCondition {
		return nil
	}
	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil
	}
	svc := ctx.Services.Ledger

	if svc.IsAmendmentBlocked() {
		return types.NewRpcError(types.RpcAMENDMENT_BLOCKED,
			"amendmentBlocked", "amendmentBlocked", "Amendment blocked, need upgrade.")
	}

	if ctx.Services.UNLBlocked != nil && ctx.Services.UNLBlocked() {
		return types.NewRpcError(types.RpcEXPIRED_VALIDATOR_LIST,
			"unlBlocked", "unlBlocked", "Validator list expired.")
	}

	info := svc.GetServerInfo()

	if !atLeastSyncing(info.ServerState) {
		return notSyncedError(ctx.ApiVersion, syncFailureNoNetwork)
	}

	if !info.Standalone {
		if validatedLedgerStale(info) || info.OpenLedgerSeq+10 < info.ValidatedLedgerSeq {
			return notSyncedError(ctx.ApiVersion, syncFailureNoCurrent)
		}
	}

	if info.ClosedLedgerSeq == 0 {
		return notSyncedError(ctx.ApiVersion, syncFailureNoClosed)
	}

	return nil
}

// atLeastSyncing reports whether the server_state is SYNCING or higher,
// mirroring rippled's OperatingMode >= SYNCING floor. The comparison is
// ordinal rather than a positive allow-list so it cannot silently reject the
// modes above FULL: a validator presents server_state "proposing" or
// "validating", which are FULL-mode aliases and so sit above the floor.
func atLeastSyncing(serverState string) bool {
	return serverStateRank(serverState) >= serverStateRank("syncing")
}

// serverStateRank maps a server_state presentation string to its operating
// mode rank. "proposing" and "validating" are the aliases a FULL-mode
// validator reports, so they rank with "full". An unrecognised string ranks
// below every operating mode and fails any floor comparison.
func serverStateRank(serverState string) int {
	switch serverState {
	case "disconnected":
		return 0
	case "connected":
		return 1
	case "syncing":
		return 2
	case "tracking":
		return 3
	case "full", "proposing", "validating":
		return 4
	default:
		return -1
	}
}

// validatedLedgerStale reports whether the node lacks a validated ledger or its
// validated ledger is older than maxValidatedLedgerAge (rippled
// getValidatedLedgerAge > Tuning::maxValidatedLedgerAge).
func validatedLedgerStale(info types.LedgerServerInfo) bool {
	if !info.HaveValidated || info.ValidatedLedgerCloseTime == 0 {
		return true
	}
	nowRipple := time.Now().Unix() - protocol.RippleEpochUnix
	age := nowRipple - info.ValidatedLedgerCloseTime
	return age > int64(maxValidatedLedgerAge/time.Second)
}

type syncFailure uint8

const (
	syncFailureNoNetwork syncFailure = iota
	syncFailureNoCurrent
	syncFailureNoClosed
)

func notSyncedError(apiVersion int, failure syncFailure) *types.RpcError {
	if apiVersion == types.ApiVersion1 {
		switch failure {
		case syncFailureNoCurrent:
			return types.NewRpcError(types.RpcNO_CURRENT, "noCurrent", "noCurrent", "Current ledger is unavailable.")
		case syncFailureNoClosed:
			return types.NewRpcError(types.RpcNO_CLOSED, "noClosed", "noClosed", "Closed ledger is unavailable.")
		default:
			return types.NewRpcError(types.RpcNO_NETWORK, "noNetwork", "noNetwork", "Not synced to the network.")
		}
	}
	return types.NewRpcError(types.RpcNOT_SYNCED, "notSynced", "notSynced",
		"Not synced to the network.")
}

func gateLoad(tracker *loadtrack.Tracker, ctx *types.RpcContext, method string, log xrpllog.Logger) *types.RpcError {
	if tracker == nil || ctx == nil || ctx.Unlimited {
		return nil
	}
	if tracker.Disconnect(ctx.ClientIP) {
		log.Warn("rpc dropped: client over load threshold",
			"client", ctx.ClientIP, "method", method, "balance", tracker.Balance(ctx.ClientIP))
		return types.RpcErrorOverloaded()
	}
	return nil
}

func chargeLoad(tracker *loadtrack.Tracker, ctx *types.RpcContext, method string, kind loadtrack.LoadKind, log xrpllog.Logger) {
	if tracker == nil || ctx == nil || ctx.Unlimited {
		return
	}
	switch tracker.Charge(ctx.ClientIP, kind) {
	case loadtrack.OutcomeDrop:
		log.Warn("rpc client crossed drop threshold (post-charge)",
			"client", ctx.ClientIP, "method", method, "balance", tracker.Balance(ctx.ClientIP))
	case loadtrack.OutcomeWarn:
		log.Info("rpc client over warn threshold",
			"client", ctx.ClientIP, "method", method, "balance", tracker.Balance(ctx.ClientIP))
	}
}

func finalizeLoad(tracker *loadtrack.Tracker, ctx *types.RpcContext, method string, kind loadtrack.LoadKind, log xrpllog.Logger) {
	chargeLoad(tracker, ctx, method, kind, log)
	warnLoad(tracker, ctx, method, log)
}

func warnLoad(tracker *loadtrack.Tracker, ctx *types.RpcContext, method string, log xrpllog.Logger) {
	if tracker == nil || ctx == nil || ctx.Unlimited || !tracker.Warn(ctx.ClientIP) {
		return
	}
	ctx.LoadWarning = true
	log.Info("rpc load warning issued",
		"client", ctx.ClientIP, "method", method, "balance", tracker.Balance(ctx.ClientIP))
}

// loadWarningOpts returns response options carrying warning:"load" when the
// dispatch crossed the resource warn threshold (recorded on ctx by
// finalizeLoad), and nil otherwise. Mirrors rippled attaching
// jr[warning]=load after the post-dispatch charge.
func loadWarningOpts(ctx *types.RpcContext) *JsonRpcResponseOptions {
	if ctx != nil && ctx.LoadWarning {
		return &JsonRpcResponseOptions{Warning: "load"}
	}
	return nil
}

// buildXrplResponseBody assembles one versioned JSON-RPC response. ripplerpc
// 1.x uses the legacy result envelope; 2.x and later move errors to the top
// level and preserve the request metadata alongside either result or error.
func buildXrplResponseBody(request any, result any, rpcErr *types.RpcError, opts *JsonRpcResponseOptions) map[string]any {
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

func (s *Server) writeXrplResponseWithOptions(w http.ResponseWriter, request any, result any, rpcErr *types.RpcError, opts *JsonRpcResponseOptions) {
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

// ExecuteMethod implements types.MethodDispatcher, allowing the 'json' RPC
// method to forward calls through the same method registry. The caller's
// context is reused so the forwarded method keeps the request's timeout,
// role, client IP and api version, and is charged for load under the real
// client IP (a fresh guest/empty-IP context previously let `json` callers
// dodge per-IP charging and escape the request timeout).
func (s *Server) ExecuteMethod(ctx *types.RpcContext, method string, params []byte) (any, *types.RpcError) {
	return dispatchNestedMethod(s.registry, ctx, method, json.RawMessage(params), rpcLog())
}

// roleForRequest mirrors rippled's requestRole (Role.cpp:94-119):
//   - peer ∈ AdminNets + valid configured credentials → RoleAdmin
//   - peer ∈ SecureGatewayNets + non-empty user → RoleIdentified
//   - peer ∈ SecureGatewayNets + empty user      → RoleProxy
//   - else                                       → RoleGuest
//
// peerIP must be the actual TCP peer (from RemoteAddr), never a header-
// supplied IP. user is the X-User header value if present. params is the
// request parameter object containing optional admin_user/admin_password fields.
func roleForRequest(peerIP string, user string, params map[string]any, portCtx *PortContext) types.Role {
	if portCtx == nil {
		return types.RoleGuest
	}
	ip := net.ParseIP(peerIP)
	if ip == nil {
		return types.RoleGuest
	}
	if len(portCtx.AdminNets) > 0 && config.IPInNets(ip, portCtx.AdminNets) && adminCredentialsMatch(params, portCtx) {
		return types.RoleAdmin
	}
	if len(portCtx.SecureGatewayNets) > 0 && config.IPInNets(ip, portCtx.SecureGatewayNets) {
		if strings.TrimSpace(user) != "" {
			return types.RoleIdentified
		}
		return types.RoleProxy
	}
	return types.RoleGuest
}

func adminCredentialsMatch(params map[string]any, portCtx *PortContext) bool {
	if portCtx.AdminUser == "" && portCtx.AdminPassword == "" {
		return true
	}
	user, userOK := params["admin_user"].(string)
	password, passwordOK := params["admin_password"].(string)
	return userOK && passwordOK && user == portCtx.AdminUser && password == portCtx.AdminPassword
}

func roleParamsFromRawParams(raw json.RawMessage) map[string]any {
	var entries []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &entries) != nil || len(entries) == 0 {
		return nil
	}
	var params map[string]any
	if json.Unmarshal(entries[0], &params) != nil {
		return nil
	}
	return params
}

func roleParamsFromBatchElement(raw json.RawMessage) map[string]any {
	var request struct {
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return nil
	}
	return roleParamsFromRawParams(request.Params)
}

// resolveClientIP extracts the client IP for logging and identification.
// X-Forwarded-For / X-Real-IP are honoured only when the actual TCP peer
// is in the per-port SecureGatewayNets set (PortContext); otherwise the
// socket peer is returned. This MUST NOT be used for role or admin
// gating — callers that need a security decision should use
// remoteAddrIP, which always returns the socket-level peer.
//
// Per-port scoping matches rippled, which passes a single Port& into
// requestRole and forwardedFor — XFF trust does not bleed across ports
// (ServerHandler.cpp:709-734).
func resolveClientIP(r *http.Request, portCtx *PortContext) string {
	peer := remoteAddrIP(r.RemoteAddr)
	if portCtx == nil || len(portCtx.SecureGatewayNets) == 0 {
		return peer
	}
	peerIP := net.ParseIP(peer)
	if peerIP == nil || !config.IPInNets(peerIP, portCtx.SecureGatewayNets) {
		return peer
	}
	if fwd := forwardedForHeader(r); fwd != "" {
		return fwd
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	return peer
}

// forwardedForHeader returns the originating client IP carried by the
// RFC 7239 Forwarded header (preferred) or the legacy X-Forwarded-For,
// mirroring rippled's forwardedFor in Role.cpp:261-312. Returns "" when
// neither header is present or parseable.
func forwardedForHeader(r *http.Request) string {
	if fwd := r.Header.Get("Forwarded"); fwd != "" {
		if ip := extractForwardedFor(fwd); ip != "" {
			return ip
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first := xff
		if before, _, ok := strings.Cut(xff, ","); ok {
			first = before
		}
		return extractIPAddrFromField(first)
	}
	return ""
}

// extractForwardedFor returns the IP from the first `for=` token in an
// RFC 7239 Forwarded header value. Case-insensitive token search; the
// value is terminated by `,` or `;` per the RFC.
func extractForwardedFor(value string) string {
	lower := strings.ToLower(value)
	idx := strings.Index(lower, "for=")
	if idx < 0 {
		return ""
	}
	rest := value[idx+len("for="):]
	if i := strings.IndexAny(rest, ",;"); i >= 0 {
		rest = rest[:i]
	}
	return extractIPAddrFromField(rest)
}

// extractIPAddrFromField strips whitespace, surrounding double quotes,
// IPv6 square brackets, and a trailing ":port" from a single Forwarded /
// X-Forwarded-For element. Mirrors rippled's extractIpAddrFromField
// (Role.cpp:156-259).
func extractIPAddrFromField(field string) string {
	s := strings.TrimSpace(field)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		if !strings.HasSuffix(s, `"`) || len(s) < 2 {
			return ""
		}
		s = strings.TrimSpace(s[1 : len(s)-1])
		if s == "" {
			return ""
		}
	}
	if strings.HasPrefix(s, "[") {
		// Bracketed form is IPv6 (or IPv4-mapped). Scan until the first
		// character that is not hex / ':' / '.' / space, matching
		// rippled Role.cpp:214-234. If that scan-terminator isn't ']',
		// the bracketed value is malformed → empty result.
		inner := s[1:]
		end := -1
		for i := 0; i < len(inner); i++ {
			c := inner[i]
			if isHexDigit(c) || c == ':' || c == '.' || c == ' ' {
				continue
			}
			end = i
			break
		}
		if end < 0 || inner[end] != ']' {
			return ""
		}
		return strings.TrimSpace(inner[:end])
	}
	// Unbracketed: a colon means either an IPv6 address (multiple colons)
	// or a host:port pair (single colon). Strip port only for the latter.
	if strings.Count(s, ":") == 1 {
		s = s[:strings.IndexByte(s, ':')]
	}
	return s
}

// remoteAddrIP returns the host portion of an http.Request.RemoteAddr
// (or any "host:port" string). Used wherever the IP must be the actual
// TCP peer — never spoofable via headers.
func remoteAddrIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// userHeader returns the X-User header value (matches rippled
// ServerHandler.cpp:582-585). Only consulted by roleForRequest when the
// peer is already in the secure_gateway set, so an untrusted client
// cannot use X-User to upgrade their role.
func userHeader(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-User"))
}

// isHexDigit reports whether c is an ASCII hex digit. Used by
// extractIPAddrFromField's bracket validator (matches rippled
// std::isxdigit in Role.cpp:222).
func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
