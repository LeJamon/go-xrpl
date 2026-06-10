package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
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

// rpcLog returns the logger for the HTTP JSON-RPC server.
// Resolved lazily so it picks up the root logger set during CLI bootstrap.
func rpcLog() xrpllog.Logger { return xrpllog.Named(xrpllog.PartitionRPC) }

// LoadCharger is the optional interface a MethodHandler may implement
// to declare its load bucket. Handlers that don't implement it pay
// loadtrack.LoadReference (rippled feeReferenceRPC parity).
type LoadCharger interface {
	LoadKind() loadtrack.LoadKind
}

type Server struct {
	registry    *types.MethodRegistry
	timeout     time.Duration
	peerSource  atomic.Pointer[types.PeerSource]
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

// SetPeerSource registers the source of per-peer entries served by the
// `peers` RPC handler. Passing nil detaches the source so the handler
// returns an empty list. Safe to call concurrently with reads.
func (s *Server) SetPeerSource(src types.PeerSource) {
	if src == nil {
		s.peerSource.Store(nil)
		return
	}
	s.peerSource.Store(&src)
}

func (s *Server) loadPeerSource() types.PeerSource {
	if p := s.peerSource.Load(); p != nil {
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
	server := &Server{
		registry:    types.NewMethodRegistry(),
		timeout:     timeout,
		services:    services,
		loadTracker: loadtrack.New(),
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
			rpcLog().Error("rpc handler panic", "err", rec, "stack", string(debug.Stack()), "method", r.Method, "remote", r.RemoteAddr)
			s.writeXrplError(w, "", nil, "internal", "Internal server error")
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
	w.Header().Set("Content-Type", "application/json")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != "POST" && r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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

	if method == "" {
		method = "server_info"
	}

	portCtx := GetPortContext(r.Context())
	peerIP := remoteAddrIP(r.RemoteAddr)
	clientIP := resolveClientIP(r, portCtx)
	user := userHeader(r)
	role := roleForRequest(peerIP, user, portCtx)
	dispatchCtx, cancel := s.withTimeout(r.Context())
	defer cancel()
	ctx := &types.RpcContext{
		Context:    dispatchCtx,
		Role:       role,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    role == types.RoleAdmin,
		Unlimited:  role.IsUnlimited(),
		ClientIP:   clientIP,
		PeerSource: s.loadPeerSource(),
		Services:   s.services,
	}

	result, rpcErr := s.executeMethod(method, nil, ctx)
	s.writeXrplResponse(w, method, nil, result, rpcErr)
}

func (s *Server) handlePostRequest(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "Request body exceeds limit", http.StatusBadRequest)
			return
		}
		s.writeXrplError(w, "", nil, "internal", "Failed to read request body")
		return
	}

	// Decode the method up front and keep params as raw JSON: a batch envelope
	// carries params as an array of full request objects, while a single
	// request carries a one-element array, and rippled inspects the method
	// before deciding which shape params must take (ServerHandler.cpp:638-649).
	var request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		s.writeXrplError(w, "", nil, "jsonInvalid", "Invalid JSON: "+err.Error())
		return
	}

	if request.Method == "" {
		s.writeXrplError(w, "", nil, "missingCommand", "Missing method field")
		return
	}

	portCtx := GetPortContext(r.Context())
	peerIP := remoteAddrIP(r.RemoteAddr)
	clientIP := resolveClientIP(r, portCtx)
	user := userHeader(r)
	// Role is derived from the socket-level peer, not header-supplied IPs,
	// so an X-Real-IP / X-Forwarded-For header from an untrusted client
	// can't elevate to admin via the localhost fallback. Matches rippled's
	// requestRole, which uses the connection's remote endpoint. The role and
	// client IP come from the connection, not request content, so they are
	// shared across every element of a batch.
	role := roleForRequest(peerIP, user, portCtx)
	dispatchCtx, cancel := s.withTimeout(r.Context())
	defer cancel()

	// rippled accepts a batch envelope — {"method":"batch","params":[ {...}, ... ]}
	// — dispatching each element as an independent request and returning a JSON
	// array of replies (ServerHandler.cpp:638-683). params must be an array;
	// missing, null, or non-array is HTTP 400 "Malformed batch request"
	// (ServerHandler.cpp:643-647). An empty array is valid: size is 0, the loop
	// runs zero times, and the reply is an empty array (ServerHandler.cpp:648-653).
	if request.Method == "batch" {
		var elements []json.RawMessage
		// A JSON null params leaves elements nil with no error, which rippled
		// rejects as "not an array"; an empty [] unmarshals to a non-nil empty
		// slice and is accepted.
		if err := json.Unmarshal(request.Params, &elements); err != nil || elements == nil {
			http.Error(w, "Malformed batch request", http.StatusBadRequest)
			return
		}
		replies := make([]map[string]any, len(elements))
		for i, el := range elements {
			replies[i] = s.dispatchBatchElement(el, dispatchCtx, role, clientIP)
		}
		w.WriteHeader(http.StatusOK)
		enc := json.NewEncoder(&trimNewlineWriter{w: w})
		if err := enc.Encode(replies); err != nil {
			rpcLog().Error("Failed to encode batch response", "err", err)
		}
		return
	}

	// XRPL JSON-RPC uses params as an array with a single object.
	var params json.RawMessage
	if len(request.Params) > 0 {
		var arr []json.RawMessage
		if err := json.Unmarshal(request.Params, &arr); err != nil {
			s.writeXrplError(w, request.Method, nil, "jsonInvalid", "Invalid JSON: params must be an array")
			return
		}
		if len(arr) > 0 {
			params = arr[0]
		}
	}

	ctx := &types.RpcContext{
		Context:    dispatchCtx,
		Role:       role,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    role == types.RoleAdmin,
		Unlimited:  role.IsUnlimited(),
		ClientIP:   clientIP,
		PeerSource: s.loadPeerSource(),
		Services:   s.services,
	}

	if params != nil {
		applyApiVersionFromObject(ctx, params)
	}

	result, rpcErr := s.executeMethod(request.Method, params, ctx)

	// rippled answers an unsupported api_version on the HTTP-single path with
	// HTTPReply(400, "invalid_API_version") — a 400 whose body is the bare
	// token, not a JSON-RPC result envelope (ServerHandler.cpp:687-690).
	if rpcErr != nil && rpcErr.IsInvalidApiVersion() {
		writeInvalidApiVersionHTTP(w)
		return
	}

	requestObj := buildRequestEcho(request.Method, params)

	s.writeXrplResponse(w, request.Method, requestObj, result, rpcErr)
}

// writeInvalidApiVersionHTTP mirrors rippled's HTTP-single rejection of an
// unsupported api_version: HTTP 400 with the bare token as the response body
// (ServerHandler.cpp:689 → HTTPReply(400, ...)). The body carries no JSON
// envelope, matching rippled's plain-string reply.
func writeInvalidApiVersionHTTP(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusBadRequest)
	if _, err := io.WriteString(w, types.InvalidApiVersionToken); err != nil {
		rpcLog().Error("Failed to write invalid_API_version response", "err", err)
	}
}

// applyApiVersionFromObject overrides ctx.ApiVersion when the given JSON object
// carries a numeric "api_version" field.
func applyApiVersionFromObject(ctx *types.RpcContext, obj json.RawMessage) {
	var m map[string]any
	if err := json.Unmarshal(obj, &m); err == nil {
		if apiVer, ok := m["api_version"]; ok {
			if ver, ok := apiVer.(float64); ok {
				ctx.ApiVersion = int(ver)
			}
		}
	}
}

// buildRequestEcho builds the request echo attached to error responses, masking
// credentials before the echo leaves the process (see redactCredentials).
func buildRequestEcho(method string, params json.RawMessage) any {
	if params != nil {
		var reqMap map[string]any
		// params may unmarshal to JSON null, which yields a nil map.
		if err := json.Unmarshal(params, &reqMap); err == nil && reqMap != nil {
			redactCredentials(reqMap)
			reqMap["command"] = method
			return reqMap
		}
	}
	return map[string]any{"command": method}
}

// dispatchBatchElement processes one element of a batch envelope and returns its
// response body. In batch mode rippled treats the element object itself as the
// request params ("params = jsonRPC", ServerHandler.cpp:681-683), with
// api_version taken from params[0] when present and otherwise from the
// element's top level (ServerHandler.cpp:668-683).
func (s *Server) dispatchBatchElement(el json.RawMessage, baseCtx context.Context, role types.Role, clientIP string) map[string]any {
	var elem map[string]any
	if err := json.Unmarshal(el, &elem); err != nil || elem == nil {
		// Non-object element: echo it under "request" with a method_not_found
		// JSON-RPC error (ServerHandler.cpp:658-665).
		var raw any
		_ = json.Unmarshal(el, &raw)
		return map[string]any{
			"request": raw,
			"error":   makeBatchJSONError(rpcMethodNotFoundCode, "Method not found"),
		}
	}

	// rippled validates the method field and emits a distinct message per
	// malformed shape, echoing the element's own fields at the top level
	// (ServerHandler.cpp:764-808).
	mv, present := elem["method"]
	if !present || mv == nil {
		return batchMalformedElement(elem, "Null method")
	}
	method, ok := mv.(string)
	if !ok {
		return batchMalformedElement(elem, "method is not string")
	}
	if method == "" {
		return batchMalformedElement(elem, "method is empty")
	}

	ctx := &types.RpcContext{
		Context:    baseCtx,
		Role:       role,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    role == types.RoleAdmin,
		Unlimited:  role.IsUnlimited(),
		ClientIP:   clientIP,
		PeerSource: s.loadPeerSource(),
		Services:   s.services,
	}
	if ver, ok := apiVersionFromBatchElement(elem); ok {
		ctx.ApiVersion = ver
	}

	result, rpcErr := s.executeMethod(method, el, ctx)

	// rippled rejects an unsupported api_version per batch element with
	// {request: <element>, error: make_json_error(wrong_version,
	// "invalid_API_version")} — a JSON-RPC error object, not the XRPL result
	// envelope (ServerHandler.cpp:692-697). The element is echoed raw (its own
	// fields, no injected `command`), so redact credentials but keep the shape.
	if rpcErr != nil && rpcErr.IsInvalidApiVersion() {
		echo := make(map[string]any, len(elem))
		maps.Copy(echo, elem)
		redactCredentials(echo)
		return map[string]any{
			"request": echo,
			"error":   makeBatchJSONError(types.WrongVersionJSONRPCCode, types.InvalidApiVersionToken),
		}
	}

	echo := make(map[string]any, len(elem)+1)
	maps.Copy(echo, elem)
	redactCredentials(echo)
	echo["command"] = method
	return buildXrplResponseBody(echo, result, rpcErr, nil)
}

// rpcMethodNotFoundCode is the JSON-RPC error code rippled attaches to malformed
// batch elements (ServerHandler.cpp:605, method_not_found = -32601). It is
// distinct from go-xrpl's XRPL-token error model and appears only inside the
// batch malformed-element replies, to match rippled byte-for-byte.
const rpcMethodNotFoundCode = -32601

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
// element's own fields are echoed at the top level — unmasked, matching
// rippled's early-exit paths which echo the raw element (ServerHandler.cpp:764-808) —
// with a method_not_found JSON-RPC error attached.
func batchMalformedElement(elem map[string]any, message string) map[string]any {
	r := make(map[string]any, len(elem)+1)
	maps.Copy(r, elem)
	r["error"] = makeBatchJSONError(rpcMethodNotFoundCode, message)
	return r
}

// apiVersionFromBatchElement resolves a batch element's api_version, preferring
// params[0].api_version and falling back to a top-level api_version, mirroring
// rippled's two-level lookup (ServerHandler.cpp:668-683).
func apiVersionFromBatchElement(elem map[string]any) (int, bool) {
	if params, ok := elem["params"].([]any); ok && len(params) > 0 {
		if first, ok := params[0].(map[string]any); ok {
			if v, ok := first["api_version"].(float64); ok {
				return int(v), true
			}
		}
	}
	if v, ok := elem["api_version"].(float64); ok {
		return int(v), true
	}
	return 0, false
}

func (s *Server) withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if s.timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, s.timeout)
}

// credentialKeys are request fields masked in error envelopes.
// rippled (ServerHandler.cpp:535-542) masks only the lowercase
// top-level keys "passphrase", "secret", "seed", "seed_hex"; goxrpl
// extends that list with the PascalCase variants used by some clients
// and traverses into the nested "tx_json" / "transaction" objects
// (see redactCredentials). This is a strict superset of rippled —
// masking more, never less.
var credentialKeys = []string{
	"secret", "seed", "passphrase", "seed_hex",
	"Secret", "Seed", "Passphrase", "SeedHex",
}

// maskedValue is the literal rippled writes in place of credential
// values (ServerHandler.cpp:536). Masking preserves the key so a
// debugging client can see a credential was supplied.
const maskedValue = "<masked>"

func redactCredentials(m map[string]any) {
	for _, k := range credentialKeys {
		if _, ok := m[k]; ok {
			m[k] = maskedValue
		}
	}
	for _, nested := range []string{"tx_json", "transaction"} {
		if sub, ok := m[nested].(map[string]any); ok {
			redactCredentials(sub)
		}
	}
}

func (s *Server) executeMethod(method string, params json.RawMessage, ctx *types.RpcContext) (any, *types.RpcError) {
	rpcLog().Debug("rpc", "method", method, "client", ctx.ClientIP)

	handler, exists := s.registry.Get(method)
	if !exists {
		return nil, types.RpcErrorMethodNotFound(method)
	}

	// Check role permissions — matches rippled RPCHandler.cpp line 166:
	// if (handler->role_ == Role::ADMIN && context.role != Role::ADMIN)
	//     return rpcNO_PERMISSION;
	if handler.RequiredRole() == types.RoleAdmin && ctx.Role != types.RoleAdmin {
		return nil, types.RpcErrorNoPermission(method)
	}

	// Enforce the method's precondition, mirroring rippled's RPC::conditionMet
	// (Handler.h:78-139).
	if rpcErr := conditionMet(handler.RequiredCondition(), ctx); rpcErr != nil {
		return nil, rpcErr
	}

	if rpcErr := validateApiVersion(ctx, handler); rpcErr != nil {
		return nil, rpcErr
	}

	if rpcErr := handlers.RequireNotBusyClient(ctx); rpcErr != nil {
		return nil, rpcErr
	}
	if err := gateLoad(s.loadTracker, ctx, method, rpcLog()); err != nil {
		return nil, err
	}
	if s.services != nil && s.services.ClientLoad != nil {
		s.services.ClientLoad.Begin()
		defer s.services.ClientLoad.End()
	}
	result, rpcErr := handler.Handle(ctx, params)
	finalizeLoad(s.loadTracker, ctx, method, handler, rpcErr, rpcLog())
	return result, rpcErr
}

// betaEnabled reports whether the operator turned on the beta RPC API for
// this request. nil-safe: a request without a service container (routing-only
// tests) is treated as non-beta.
func betaEnabled(ctx *types.RpcContext) bool {
	return ctx.Services != nil && ctx.Services.BetaRPCAPI
}

// validateApiVersion enforces the accepted api_version range, mirroring
// rippled's two checks: the dispatch-layer cap (getAPIVersionNumber rejecting
// anything above apiBetaVersion when beta is off, ServerHandler.cpp:685-695),
// and the per-handler support set (Handler.cpp:257-263). A version above the
// beta-gated maximum, or one the handler does not list, yields
// invalid_API_version.
func validateApiVersion(ctx *types.RpcContext, handler types.MethodHandler) *types.RpcError {
	maxVersion := types.MaxSupportedApiVersion
	if betaEnabled(ctx) {
		maxVersion = types.BetaApiVersion
	}
	if ctx.ApiVersion < types.ApiVersion1 || ctx.ApiVersion > maxVersion {
		return types.RpcErrorInvalidApiVersion(strconv.Itoa(ctx.ApiVersion))
	}
	supportedVersions := handler.SupportedApiVersions()
	if len(supportedVersions) > 0 && !slices.Contains(supportedVersions, ctx.ApiVersion) {
		return types.RpcErrorInvalidApiVersion(strconv.Itoa(ctx.ApiVersion))
	}
	return nil
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
		return notSyncedError(ctx.ApiVersion, types.RpcNO_NETWORK, "noNetwork",
			"Not synced to the network.")
	}

	if !info.Standalone {
		if validatedLedgerStale(info) || info.OpenLedgerSeq+10 < info.ValidatedLedgerSeq {
			return notSyncedError(ctx.ApiVersion, types.RpcNO_CURRENT, "noCurrent",
				"Current ledger is unavailable.")
		}
	}

	if info.ClosedLedgerSeq == 0 {
		return notSyncedError(ctx.ApiVersion, types.RpcNO_CLOSED, "noClosed",
			"Closed ledger is unavailable.")
	}

	return nil
}

// atLeastSyncing reports whether the operating-mode string is SYNCING or higher
// (rippled's OperatingMode >= SYNCING floor).
func atLeastSyncing(serverState string) bool {
	switch serverState {
	case "syncing", "tracking", "full":
		return true
	default:
		return false
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

// notSyncedError returns the apiVersion-1 code with its token/message, or
// rpcNOT_SYNCED for later versions, mirroring rippled conditionMet.
func notSyncedError(apiVersion, v1Code int, v1Token, v1Message string) *types.RpcError {
	if apiVersion == types.ApiVersion1 {
		return types.NewRpcError(v1Code, v1Token, v1Token, v1Message)
	}
	return types.NewRpcError(types.RpcNOT_SYNCED, "notSynced", "notSynced",
		"Not synced to the network.")
}

// gateLoad is the pre-dispatch admission check. It does NOT charge the
// caller; it only rejects when the (decayed) balance is already at or
// above DropThreshold. Mirrors rippled ServerHandler.cpp:735 where
// usage.disconnect() is consulted *before* doCommand runs.
//
// Admin / identified callers bypass tracking entirely, matching rippled
// isUnlimited() (Role.cpp:124-128).
func gateLoad(tracker *loadtrack.Tracker, ctx *types.RpcContext, method string, log xrpllog.Logger) *types.RpcError {
	if tracker == nil || ctx.Unlimited {
		return nil
	}
	if tracker.OverDropThreshold(ctx.ClientIP) {
		log.Warn("rpc dropped: client over load threshold",
			"client", ctx.ClientIP, "method", method, "balance", tracker.Balance(ctx.ClientIP))
		return types.RpcErrorSlowDown("Slow down. Server is too busy for this client.")
	}
	return nil
}

// finalizeLoad is the post-dispatch charge step. The charge applied is
// LoadMalformed when the handler returned an invalidParams / methodNotFound
// error (rippled bumps loadType to feeMalformedRPC for the same condition
// in RPCHandler.cpp:211-215 and ServerHandler.cpp:766-808). Otherwise
// the handler's declared LoadKind (or LoadReference) is used.
//
// Admin / identified callers bypass tracking entirely.
func finalizeLoad(tracker *loadtrack.Tracker, ctx *types.RpcContext, method string, handler types.MethodHandler, rpcErr *types.RpcError, log xrpllog.Logger) {
	if tracker == nil || ctx.Unlimited {
		return
	}
	kind := loadKindFor(handler, rpcErr)
	switch tracker.Charge(ctx.ClientIP, kind) {
	case loadtrack.OutcomeDrop:
		log.Warn("rpc client crossed drop threshold (post-charge)",
			"client", ctx.ClientIP, "method", method, "balance", tracker.Balance(ctx.ClientIP))
	case loadtrack.OutcomeWarn:
		log.Info("rpc client over warn threshold",
			"client", ctx.ClientIP, "method", method, "balance", tracker.Balance(ctx.ClientIP))
	}
}

// loadKindFor returns the charge bucket to apply for one dispatch. A
// malformed / unknown-method response is charged LoadMalformed so a
// client cannot use bad input as a cheap probe (matches rippled's
// feeMalformedRPC bump in RPCHandler.cpp / ServerHandler.cpp).
func loadKindFor(handler types.MethodHandler, rpcErr *types.RpcError) loadtrack.LoadKind {
	if rpcErr != nil {
		switch rpcErr.Code {
		case types.RpcINVALID_PARAMS, types.RpcMETHOD_NOT_FOUND:
			return loadtrack.LoadMalformed
		case types.RpcINTERNAL:
			return loadtrack.LoadException
		}
	}
	if c, ok := handler.(LoadCharger); ok {
		return c.LoadKind()
	}
	return loadtrack.LoadReference
}

// writeXrplResponse writes an XRPL format JSON-RPC response. Per XRPL spec
// result.status is "success" or "error" and warning/warnings/forwarded
// live at the top level, not inside result.
func (s *Server) writeXrplResponse(w http.ResponseWriter, method string, request any, result any, rpcErr *types.RpcError) {
	s.writeXrplResponseWithOptions(w, method, request, result, rpcErr, nil)
}

// buildXrplResponseBody assembles the `{"result": {...}}` envelope (plus any
// top-level warning/forwarded fields) for a single dispatched request. It is
// shared by the single-request writer and by each element of a batch envelope,
// so every batch reply has the same shape as a standalone reply.
func buildXrplResponseBody(request any, result any, rpcErr *types.RpcError, opts *JsonRpcResponseOptions) map[string]any {
	response := make(map[string]any)

	if rpcErr != nil {
		resultObj := map[string]any{
			"status": "error",
			"error":  rpcErr.ErrorString,
		}
		// rippled bare-token handlers emit only `error`; inject_error paths add
		// error_code + error_message. Mirror both.
		if !rpcErr.IsBareToken() {
			resultObj["error_code"] = rpcErr.Code
			resultObj["error_message"] = rpcErr.Message
		}
		if request != nil {
			resultObj["request"] = request
		}
		response["result"] = resultObj
	} else {
		if resultMap, ok := result.(map[string]any); ok {
			resultMap["status"] = "success"
			response["result"] = resultMap
		} else {
			response["result"] = map[string]any{
				"status": "success",
				"data":   result,
			}
		}
	}

	if opts != nil {
		if opts.Warning != "" {
			response["warning"] = opts.Warning
		}
		if len(opts.Warnings) > 0 {
			response["warnings"] = opts.Warnings
		}
		if opts.Forwarded {
			response["forwarded"] = true
		}
	}

	return response
}

func (s *Server) writeXrplResponseWithOptions(w http.ResponseWriter, method string, request any, result any, rpcErr *types.RpcError, opts *JsonRpcResponseOptions) {
	response := buildXrplResponseBody(request, result, rpcErr, opts)

	// Stream-encode straight to the response writer through trimNewlineWriter.
	// json.Encoder.Encode would otherwise emit a trailing '\n' that rippled's
	// equivalent path does not produce, changing the byte-exact wire shape
	// for parity-sensitive clients. Streaming avoids buffering large
	// payloads (book_offers, ledger_data, ripple_path_find) into a []byte
	// before w.Write; on encode error headers are already sent, so logging
	// is the only honest signal.
	//
	// rpcTOO_BUSY maps to HTTP 503, matching rippled ErrorCodes.cpp:114
	// (`{rpcTOO_BUSY, "tooBusy", ..., 503}`). All other errors ride on
	// 200 OK; XRPL clients parse result.error, not the HTTP status.
	if rpcErr != nil && rpcErr.Code == types.RpcTOO_BUSY {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	enc := json.NewEncoder(&trimNewlineWriter{w: w})
	if err := enc.Encode(response); err != nil {
		rpcLog().Error("Failed to encode response", "err", err)
	}
}

// trimNewlineWriter wraps an io.Writer and discards a single trailing
// newline byte across Write calls. json.Encoder.Encode always appends
// '\n' after the JSON value; rippled's serialiser does not.
type trimNewlineWriter struct {
	w       io.Writer
	pending bool // a '\n' was buffered from the previous Write
}

func (t *trimNewlineWriter) Write(p []byte) (int, error) {
	written := 0
	if t.pending && len(p) > 0 {
		if _, err := t.w.Write([]byte{'\n'}); err != nil {
			return 0, err
		}
		t.pending = false
	}
	if len(p) > 0 && p[len(p)-1] == '\n' {
		t.pending = true
		body := p[:len(p)-1]
		if len(body) > 0 {
			n, err := t.w.Write(body)
			written = n
			if err != nil {
				return written, err
			}
		}
		return written + 1, nil
	}
	n, err := t.w.Write(p)
	return n, err
}

func (s *Server) writeXrplError(w http.ResponseWriter, method string, request any, errorCode string, message string) {
	resultObj := map[string]any{
		"status":        "error",
		"error":         errorCode,
		"error_message": message,
	}
	if request != nil {
		resultObj["request"] = request
	}

	response := map[string]any{
		"result": resultObj,
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		rpcLog().Error("Failed to marshal error response", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(responseData)
}

// ExecuteMethod implements types.MethodDispatcher, allowing the 'json' RPC
// method to forward calls through the same method registry.
func (s *Server) ExecuteMethod(method string, params []byte) (any, *types.RpcError) {
	ctx := &types.RpcContext{
		Context:    context.Background(),
		Role:       types.RoleGuest,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    false,
		PeerSource: s.loadPeerSource(),
		Services:   s.services,
	}
	return s.executeMethod(method, json.RawMessage(params), ctx)
}

// isLocalhost returns true if the IP address is a loopback address.
// In standalone mode, connections from localhost are treated as Admin.
// This is a simplified version of rippled's admin detection (see Role.cpp:isAdmin).
func isLocalhost(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1"
}

// roleForRequest mirrors rippled's requestRole (Role.cpp:94-119):
//   - peer ∈ AdminNets → RoleAdmin
//   - peer ∈ SecureGatewayNets + non-empty user → RoleIdentified
//   - peer ∈ SecureGatewayNets + empty user      → RoleProxy
//   - else                                       → RoleGuest
//
// peerIP must be the actual TCP peer (from RemoteAddr), never a header-
// supplied IP. user is the X-User header value if present.
//
// Fallback: when no AdminNets are configured (typically in unit tests or
// standalone mode), localhost is treated as Admin so the legacy
// single-process flows keep working.
func roleForRequest(peerIP string, user string, portCtx *PortContext) types.Role {
	if portCtx == nil || (len(portCtx.AdminNets) == 0 && len(portCtx.SecureGatewayNets) == 0) {
		if isLocalhost(peerIP) {
			return types.RoleAdmin
		}
		return types.RoleGuest
	}
	ip := net.ParseIP(peerIP)
	if ip == nil {
		return types.RoleGuest
	}
	if len(portCtx.AdminNets) > 0 && config.IPInNets(ip, portCtx.AdminNets) {
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
