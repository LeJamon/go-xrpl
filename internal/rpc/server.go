package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/goXRPLd/config"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	xrpllog "github.com/LeJamon/goXRPLd/log"
)

// MaxRequestBytes caps the size of a single JSON-RPC request body. XRPL
// requests are small (a few KB at most for tx_blob); 1 MiB is generous
// without leaving the server vulnerable to memory-buffering DoS via
// io.ReadAll on an unbounded body.
const MaxRequestBytes = 1 << 20

// rpcLog returns the logger for the HTTP JSON-RPC server.
// Resolved lazily so it picks up the root logger set during CLI bootstrap.
func rpcLog() xrpllog.Logger { return xrpllog.Named(xrpllog.PartitionRPC) }

// Server handles HTTP JSON-RPC requests using XRPL format
type Server struct {
	registry   *types.MethodRegistry
	timeout    time.Duration
	peerSource atomic.Pointer[types.PeerSource]
	services   *types.ServiceContainer

	// corsAllowedOrigins, if non-empty, restricts Access-Control-Allow-Origin
	// to the listed origins (set via SetCORSAllowedOrigins). Empty means
	// `*` — the historical wide-open default kept for backwards compat.
	corsMu             sync.RWMutex
	corsAllowedOrigins []string

	// trustedProxies is the set of TCP peer networks whose
	// X-Forwarded-For / X-Real-IP headers are believed for the purpose of
	// logging / client-IP attribution. Empty means "no proxy is trusted"
	// — we always log the socket peer. Role/admin decisions never consult
	// these headers regardless of this setting (see roleForRequest).
	trustedProxiesMu sync.RWMutex
	trustedProxies   []net.IPNet
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
// Pass nil/empty to fall back to `*` (the historical default; matches
// rippled's wide-open setting). Origins are matched exactly against the
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
		registry: types.NewMethodRegistry(),
		timeout:  timeout,
		services: services,
	}

	// Register all RPC methods
	server.registerAllMethods()

	return server
}

// Services returns the service container wired to this server. Used by
// callers that need to attach the dispatcher (this server itself) or
// the shutdown hook after construction.
func (s *Server) Services() *types.ServiceContainer { return s.services }

// XrplRequest represents an XRPL JSON-RPC request
// Format: {"method": "method_name", "params": [{...}]}
type XrplRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params,omitempty"`
}

// JsonRpcResponseOptions contains optional fields for JSON-RPC responses
// These fields are at the top level, not inside the result object
type JsonRpcResponseOptions struct {
	Warning   string                // "load" when approaching rate limit
	Warnings  []types.WarningObject // Array of warning objects
	Forwarded bool                  // True if forwarded from Clio to P2P server
}

// ServeHTTP implements http.Handler interface
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			rpcLog().Error("rpc handler panic", "err", rec, "stack", string(debug.Stack()), "method", r.Method, "remote", r.RemoteAddr)
			s.writeXrplError(w, "", nil, "internal", "Internal server error")
		}
	}()

	// Set CORS headers. Default is `*` to match rippled; an explicit
	// allowlist may be configured via SetCORSAllowedOrigins, in which case
	// we only echo back the request's Origin when it is on the list.
	if allow := s.resolveCORSOrigin(r.Header.Get("Origin")); allow != "" {
		w.Header().Set("Access-Control-Allow-Origin", allow)
		if allow != "*" {
			w.Header().Set("Vary", "Origin")
		}
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json")

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Only accept POST and GET methods
	if r.Method != "POST" && r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Handle GET request (for simple queries like server_info)
	if r.Method == "GET" {
		s.handleGetRequest(w, r)
		return
	}

	// Handle POST request (standard XRPL JSON-RPC)
	s.handlePostRequest(w, r)
}

// handleGetRequest processes GET requests with query parameters
func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	method := query.Get("command")

	if method == "" {
		// Default to server_info for GET requests without command
		method = "server_info"
	}

	peerIP := remoteAddrIP(r.RemoteAddr)
	clientIP := s.getClientIP(r)
	portCtx := GetPortContext(r.Context())
	role := roleForRequest(peerIP, portCtx)
	dispatchCtx, cancel := s.withTimeout(r.Context())
	defer cancel()
	ctx := &types.RpcContext{
		Context:    dispatchCtx,
		Role:       role,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    role == types.RoleAdmin,
		ClientIP:   clientIP,
		PeerSource: s.loadPeerSource(),
		Services:   s.services,
	}

	result, rpcErr := s.executeMethod(method, nil, ctx)
	s.writeXrplResponse(w, method, nil, result, rpcErr)
}

// handlePostRequest processes POST requests with XRPL JSON-RPC payload
func (s *Server) handlePostRequest(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.writeXrplError(w, "", nil, "invalidParams", "Request body exceeds limit")
			return
		}
		s.writeXrplError(w, "", nil, "internal", "Failed to read request body")
		return
	}

	var request XrplRequest
	if err := json.Unmarshal(body, &request); err != nil {
		s.writeXrplError(w, "", nil, "jsonInvalid", "Invalid JSON: "+err.Error())
		return
	}

	if request.Method == "" {
		s.writeXrplError(w, "", nil, "missingCommand", "Missing method field")
		return
	}

	// Extract params - XRPL uses params as an array with one object
	var params json.RawMessage
	if len(request.Params) > 0 {
		params = request.Params[0]
	}

	peerIP := remoteAddrIP(r.RemoteAddr)
	clientIP := s.getClientIP(r)
	portCtx := GetPortContext(r.Context())
	// Role is derived from the socket-level peer, not header-supplied IPs,
	// so an X-Real-IP / X-Forwarded-For header from an untrusted client
	// can't elevate to admin via the localhost fallback. Matches rippled's
	// requestRole, which uses the connection's remote endpoint.
	role := roleForRequest(peerIP, portCtx)
	dispatchCtx, cancel := s.withTimeout(r.Context())
	defer cancel()
	ctx := &types.RpcContext{
		Context:    dispatchCtx,
		Role:       role,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    role == types.RoleAdmin,
		ClientIP:   clientIP,
		PeerSource: s.loadPeerSource(),
		Services:   s.services,
	}

	// Parse API version from params if present
	if params != nil {
		var paramsMap map[string]interface{}
		if err := json.Unmarshal(params, &paramsMap); err == nil {
			if apiVer, ok := paramsMap["api_version"]; ok {
				if ver, ok := apiVer.(float64); ok {
					ctx.ApiVersion = int(ver)
				}
			}
		}
	}

	result, rpcErr := s.executeMethod(request.Method, params, ctx)

	// Build request object for error responses. Credential-bearing fields
	// (secret, seed, passphrase, key, seed_hex) are stripped so they don't
	// leak back to the client when a request fails.
	var requestObj interface{}
	if params != nil {
		var reqMap map[string]interface{}
		// Check both for unmarshal error AND nil map (params could be JSON null)
		if err := json.Unmarshal(params, &reqMap); err == nil && reqMap != nil {
			redactCredentials(reqMap)
			reqMap["command"] = request.Method
			requestObj = reqMap
		} else {
			requestObj = map[string]interface{}{"command": request.Method}
		}
	} else {
		requestObj = map[string]interface{}{"command": request.Method}
	}

	s.writeXrplResponse(w, request.Method, requestObj, result, rpcErr)
}

// withTimeout wraps the request context with s.timeout if a positive
// timeout is configured. Returns the (possibly new) context plus a cancel
// func the caller must call. A zero/negative timeout returns the context
// unchanged with a no-op cancel.
func (s *Server) withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if s.timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, s.timeout)
}

// credentialKeys are request fields whose values must be masked in error
// envelopes. Matches rippled's strip list in ServerHandler.cpp:535-542
// (passphrase, secret, seed, seed_hex). Both lower-case and PascalCase
// shapes are covered so clients using either casing don't leak.
var credentialKeys = []string{
	"secret", "seed", "passphrase", "seed_hex",
	"Secret", "Seed", "Passphrase", "SeedHex",
}

// maskedValue is the literal rippled writes in place of credential
// values — see ServerHandler.cpp:536. Masking (rather than deleting) lets
// a debugging client see that a credential field was supplied without
// learning its value.
const maskedValue = "<masked>"

// redactCredentials replaces credential-bearing values with maskedValue
// in place, recursing into tx_json/transaction objects so signing fields
// nested under those keys are also scrubbed.
func redactCredentials(m map[string]interface{}) {
	for _, k := range credentialKeys {
		if _, ok := m[k]; ok {
			m[k] = maskedValue
		}
	}
	for _, nested := range []string{"tx_json", "transaction"} {
		if sub, ok := m[nested].(map[string]interface{}); ok {
			redactCredentials(sub)
		}
	}
}

// executeMethod executes an RPC method with the given parameters
func (s *Server) executeMethod(method string, params json.RawMessage, ctx *types.RpcContext) (interface{}, *types.RpcError) {
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

	// Check amendment blocking - matching rippled's conditionMet() in Handler.h
	// When the server is amendment-blocked, methods with any condition
	// other than NoCondition are blocked with rpcAMENDMENT_BLOCKED.
	if handler.RequiredCondition() != types.NoCondition {
		if ctx.Services != nil && ctx.Services.Ledger != nil {
			if ctx.Services.Ledger.IsAmendmentBlocked() {
				return nil, types.NewRpcError(types.RpcAMENDMENT_BLOCKED,
					"amendmentBlocked", "amendmentBlocked",
					"Amendment blocked, need upgrade.")
			}
		}
	}

	supportedVersions := handler.SupportedApiVersions()
	if len(supportedVersions) > 0 {
		supported := false
		for _, version := range supportedVersions {
			if ctx.ApiVersion == version {
				supported = true
				break
			}
		}
		if !supported {
			return nil, types.RpcErrorInvalidApiVersion(strconv.Itoa(ctx.ApiVersion))
		}
	}

	return handler.Handle(ctx, params)
}

// writeXrplResponse writes an XRPL format JSON-RPC response
// Per XRPL spec:
// - result.status = "success" or "error"
// - warning, warnings, forwarded are at top level (outside result)
func (s *Server) writeXrplResponse(w http.ResponseWriter, method string, request interface{}, result interface{}, rpcErr *types.RpcError) {
	s.writeXrplResponseWithOptions(w, method, request, result, rpcErr, nil)
}

// writeXrplResponseWithOptions writes an XRPL format JSON-RPC response with optional fields
func (s *Server) writeXrplResponseWithOptions(w http.ResponseWriter, method string, request interface{}, result interface{}, rpcErr *types.RpcError, opts *JsonRpcResponseOptions) {
	response := make(map[string]interface{})

	if rpcErr != nil {
		resultObj := map[string]interface{}{
			"status":        "error",
			"error":         rpcErr.ErrorString,
			"error_code":    rpcErr.Code,
			"error_message": rpcErr.Message,
		}
		if request != nil {
			resultObj["request"] = request
		}
		response["result"] = resultObj
	} else {
		if resultMap, ok := result.(map[string]interface{}); ok {
			resultMap["status"] = "success"
			response["result"] = resultMap
		} else {
			response["result"] = map[string]interface{}{
				"status": "success",
				"data":   result,
			}
		}
	}

	// Add optional fields at top level (per XRPL JSON-RPC spec)
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

	// Stream-encode straight to the response writer. Large payloads
	// (book_offers, ledger_data, ripple_path_find) used to be fully
	// buffered into a []byte via json.Marshal before w.Write, doubling
	// peak memory under load. NewEncoder pipes directly into the socket
	// buffer; on encode error we've already sent headers, so a 200 with
	// a truncated body is the only honest outcome — we log the failure.
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		rpcLog().Error("Failed to encode response", "err", err)
	}
}

// writeXrplError writes an XRPL format error response
func (s *Server) writeXrplError(w http.ResponseWriter, method string, request interface{}, errorCode string, message string) {
	resultObj := map[string]interface{}{
		"status":        "error",
		"error":         errorCode,
		"error_message": message,
	}
	if request != nil {
		resultObj["request"] = request
	}

	response := map[string]interface{}{
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
func (s *Server) ExecuteMethod(method string, params []byte) (interface{}, *types.RpcError) {
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

// roleForRequest determines the Role for an incoming request based on the
// client IP and the port's admin network list. When a PortContext with
// AdminNets is available, it checks the client IP against those networks
// (matching rippled's requestRole in Role.cpp). Otherwise it falls back to
// the legacy localhost-only check for backward compatibility.
func roleForRequest(clientIP string, portCtx *PortContext) types.Role {
	if portCtx != nil && len(portCtx.AdminNets) > 0 {
		ip := net.ParseIP(clientIP)
		if ip != nil && config.IPInNets(ip, portCtx.AdminNets) {
			return types.RoleAdmin
		}
		return types.RoleGuest
	}
	// Fallback: no port context or no admin nets configured — use localhost check
	if isLocalhost(clientIP) {
		return types.RoleAdmin
	}
	return types.RoleGuest
}

// SetTrustedProxies installs the set of TCP peer networks whose
// X-Forwarded-For / X-Real-IP headers are honoured for client-IP
// attribution. Passing nil/empty disables proxy-header trust entirely
// (logs always show the socket peer). Auth decisions ignore these
// headers regardless — see roleForRequest.
func (s *Server) SetTrustedProxies(nets []net.IPNet) {
	s.trustedProxiesMu.Lock()
	defer s.trustedProxiesMu.Unlock()
	if len(nets) == 0 {
		s.trustedProxies = nil
		return
	}
	s.trustedProxies = append(s.trustedProxies[:0:0], nets...)
}

func (s *Server) loadTrustedProxies() []net.IPNet {
	s.trustedProxiesMu.RLock()
	defer s.trustedProxiesMu.RUnlock()
	if len(s.trustedProxies) == 0 {
		return nil
	}
	out := make([]net.IPNet, len(s.trustedProxies))
	copy(out, s.trustedProxies)
	return out
}

// getClientIP extracts the client IP for logging and identification.
// X-Forwarded-For / X-Real-IP are honoured only when the actual TCP peer
// is in the server's trustedProxies set; otherwise the socket peer is
// returned. This MUST NOT be used for role or admin gating — callers
// that need a security decision should use remoteAddrIP, which always
// returns the socket-level peer.
func (s *Server) getClientIP(r *http.Request) string {
	peer := remoteAddrIP(r.RemoteAddr)
	trusted := s.loadTrustedProxies()
	if len(trusted) == 0 {
		return peer
	}
	peerIP := net.ParseIP(peer)
	if peerIP == nil || !config.IPInNets(peerIP, trusted) {
		return peer
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		return strings.TrimSpace(ips[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	return peer
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
