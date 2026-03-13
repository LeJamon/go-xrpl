package rpc

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	xrpllog "github.com/LeJamon/goXRPLd/log"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// rpcLog is the logger for the HTTP JSON-RPC server.
var rpcLog = xrpllog.Named(xrpllog.PartitionRPC)

// Server handles HTTP JSON-RPC requests using XRPL format
type Server struct {
	registry *types.MethodRegistry
	timeout  time.Duration
}

// NewServer creates a new RPC server with the given timeout
func NewServer(timeout time.Duration) *Server {
	server := &Server{
		registry: types.NewMethodRegistry(),
		timeout:  timeout,
	}

	// Register all RPC methods
	server.registerAllMethods()

	return server
}

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
	// Set CORS headers to match rippled
	w.Header().Set("Access-Control-Allow-Origin", "*")
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

	// Create RPC context
	clientIP := getClientIP(r)
	role := roleForRequest(clientIP)
	ctx := &types.RpcContext{
		Context:    r.Context(),
		Role:       role,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    role == types.RoleAdmin,
		ClientIP:   clientIP,
	}

	// Execute method
	result, rpcErr := s.executeMethod(method, nil, ctx)

	// Build XRPL format response
	s.writeXrplResponse(w, method, nil, result, rpcErr)
}

// handlePostRequest processes POST requests with XRPL JSON-RPC payload
func (s *Server) handlePostRequest(w http.ResponseWriter, r *http.Request) {
	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeXrplError(w, "", nil, "internal", "Failed to read request body")
		return
	}
	defer r.Body.Close()

	// Parse XRPL request
	var request XrplRequest
	if err := json.Unmarshal(body, &request); err != nil {
		s.writeXrplError(w, "", nil, "jsonInvalid", "Invalid JSON: "+err.Error())
		return
	}

	// Check for method
	if request.Method == "" {
		s.writeXrplError(w, "", nil, "missingCommand", "Missing method field")
		return
	}

	// Extract params - XRPL uses params as an array with one object
	var params json.RawMessage
	if len(request.Params) > 0 {
		params = request.Params[0]
	}

	// Create RPC context
	clientIP := getClientIP(r)
	role := roleForRequest(clientIP)
	ctx := &types.RpcContext{
		Context:    r.Context(),
		Role:       role,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    role == types.RoleAdmin,
		ClientIP:   clientIP,
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

	// Execute method
	result, rpcErr := s.executeMethod(request.Method, params, ctx)

	// Build request object for error responses
	var requestObj interface{}
	if params != nil {
		var reqMap map[string]interface{}
		// Check both for unmarshal error AND nil map (params could be JSON null)
		if err := json.Unmarshal(params, &reqMap); err == nil && reqMap != nil {
			reqMap["command"] = request.Method
			requestObj = reqMap
		} else {
			requestObj = map[string]interface{}{"command": request.Method}
		}
	} else {
		requestObj = map[string]interface{}{"command": request.Method}
	}

	// Build XRPL format response
	s.writeXrplResponse(w, request.Method, requestObj, result, rpcErr)
}

// executeMethod executes an RPC method with the given parameters
func (s *Server) executeMethod(method string, params json.RawMessage, ctx *types.RpcContext) (interface{}, *types.RpcError) {
	rpcLog.Debug("rpc", "method", method, "client", ctx.ClientIP)

	// Get method handler
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
		if types.Services != nil && types.Services.Ledger != nil {
			if types.Services.Ledger.IsAmendmentBlocked() {
				return nil, types.NewRpcError(types.RpcAMENDMENT_BLOCKED,
					"amendmentBlocked", "amendmentBlocked",
					"Amendment blocked, need upgrade.")
			}
		}
	}

	// Check API version support
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

	// Execute handler
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
		// Error response format - XRPL includes error, error_code, error_message inside result
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
		// Success response format
		// If result is already a map, add status to it
		if resultMap, ok := result.(map[string]interface{}); ok {
			resultMap["status"] = "success"
			response["result"] = resultMap
		} else {
			// Wrap non-map results
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

	responseData, err := json.Marshal(response)
	if err != nil {
		rpcLog.Error("Failed to marshal response", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(responseData)
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
		rpcLog.Error("Failed to marshal error response", "err", err)
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
		Context:    nil,
		Role:       types.RoleGuest,
		ApiVersion: types.DefaultApiVersion,
		IsAdmin:    false,
	}
	return s.executeMethod(method, json.RawMessage(params), ctx)
}

// isLocalhost returns true if the IP address is a loopback address.
// In standalone mode, connections from localhost are treated as Admin.
// This is a simplified version of rippled's admin detection (see Role.cpp:isAdmin).
func isLocalhost(ip string) bool {
	return ip == "127.0.0.1" || ip == "::1"
}

// roleForRequest determines the Role for an incoming request.
// In standalone mode (the only mode currently supported), localhost
// connections are Admin; everything else is Guest.
func roleForRequest(clientIP string) types.Role {
	if isLocalhost(clientIP) {
		return types.RoleAdmin
	}
	return types.RoleGuest
}

// getClientIP extracts the client IP from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		return strings.TrimSpace(ips[0])
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}

	return ip
}
