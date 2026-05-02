package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/subscription"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	xrpllog "github.com/LeJamon/goXRPLd/log"
	"github.com/gorilla/websocket"
)

// wsLog returns the logger for the WebSocket server.
// Resolved lazily so it picks up the root logger set during CLI bootstrap.
func wsLog() xrpllog.Logger { return xrpllog.Named(xrpllog.PartitionRPC) }

// DefaultSendQueueLimit is the default WebSocket send channel buffer size,
// matching rippled's default ws_queue_limit of 100 (Port.cpp).
const DefaultSendQueueLimit = 100

// WebSocketServer handles WebSocket connections for real-time subscriptions
type WebSocketServer struct {
	upgrader            websocket.Upgrader
	subscriptionManager *subscription.Manager
	methodRegistry      *types.MethodRegistry
	connections         map[string]*WebSocketConnection
	connectionsMutex    sync.RWMutex
	timeout             time.Duration
	ledgerInfoProvider  types.LedgerInfoProvider
	connLimiter         *ConnLimiter
	services            *types.ServiceContainer
}

// WebSocketConnection represents a single WebSocket connection
type WebSocketConnection struct {
	ID              string
	conn            *websocket.Conn
	subscriptions   map[types.SubscriptionType]types.SubscriptionConfig
	sendChannel     chan []byte
	closeChannel    chan struct{}
	mutex           sync.RWMutex
	ctx             context.Context
	cancel          context.CancelFunc
	pathFindSession *PathFindSession // At most one active path_find session per connection
	portCtx         *PortContext     // per-port config for role determination
}

// NewWebSocketServer creates a new WebSocket server. The provided
// service container is attached to every RpcContext routed through the
// server so handlers reach the ledger via ctx.Services. May be nil for
// test contexts.
//
// allowedOrigins controls which Origin headers may upgrade to a WebSocket
// connection. An empty list (or a list containing "*") allows all origins,
// matching rippled's default behavior. Otherwise, the request Origin's host
// (with optional port) is matched case-insensitively against the list.
// Passed as a variadic for backwards compatibility with existing callers
// that do not configure an allowlist.
func NewWebSocketServer(timeout time.Duration, services *types.ServiceContainer, allowedOrigins ...[]string) *WebSocketServer {
	var origins []string
	if len(allowedOrigins) > 0 {
		origins = allowedOrigins[0]
	}
	return &WebSocketServer{
		upgrader: websocket.Upgrader{
			CheckOrigin: makeCheckOrigin(origins),
			// Don't require specific subprotocol - xrpl.js doesn't use one
		},
		subscriptionManager: &subscription.Manager{
			Connections: make(map[string]*types.Connection),
		},
		methodRegistry: types.NewMethodRegistry(),
		connections:    make(map[string]*WebSocketConnection),
		timeout:        timeout,
		services:       services,
	}
}

// makeCheckOrigin builds a websocket.Upgrader CheckOrigin function from a
// configurable allowlist. An empty allowlist (or "*") permits any origin,
// preserving the historical "allow all" default and matching rippled's
// behavior. Non-empty allowlists are matched case-insensitively against
// the request Origin's host (with optional :port). Malformed Origin
// headers are rejected when an allowlist is configured.
func makeCheckOrigin(allowed []string) func(*http.Request) bool {
	cleaned := make([]string, 0, len(allowed))
	allowAll := len(allowed) == 0
	for _, entry := range allowed {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == "*" {
			allowAll = true
			continue
		}
		cleaned = append(cleaned, strings.ToLower(entry))
	}

	if allowAll {
		return func(*http.Request) bool { return true }
	}

	allowSet := make(map[string]struct{}, len(cleaned))
	for _, h := range cleaned {
		allowSet[h] = struct{}{}
	}

	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Non-browser clients (curl, native xrpl.js in Node) do not send
			// Origin. Allow them through; an allowlist only constrains
			// browser-originated requests where Origin is mandatory.
			return true
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return false
		}
		host := strings.ToLower(u.Host)
		if _, ok := allowSet[host]; ok {
			return true
		}
		// Also try the bare hostname without port, so an allowlist entry of
		// "example.com" matches an Origin of "https://example.com:8443".
		if h := u.Hostname(); h != "" {
			if _, ok := allowSet[strings.ToLower(h)]; ok {
				return true
			}
		}
		return false
	}
}

// SetLedgerInfoProvider sets the provider used to return current ledger info
// in subscribe responses (e.g., when subscribing to the "ledger" stream).
func (ws *WebSocketServer) SetLedgerInfoProvider(provider types.LedgerInfoProvider) {
	ws.ledgerInfoProvider = provider
}

// SetConnLimiter sets the connection limiter used to release per-port slots
// when WebSocket connections close.
func (ws *WebSocketServer) SetConnLimiter(limiter *ConnLimiter) {
	ws.connLimiter = limiter
}

// ServeHTTP handles WebSocket upgrade requests
func (ws *WebSocketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract per-port context injected by PortMiddleware
	portCtx := GetPortContext(r.Context())

	// Upgrade HTTP connection to WebSocket
	conn, err := ws.upgrader.Upgrade(w, r, nil)
	if err != nil {
		wsLog().Error("WebSocket upgrade failed", "err", err)
		return
	}

	// Determine send queue size from port config, default to 100 (rippled default)
	sendQueueLimit := DefaultSendQueueLimit
	if portCtx != nil && portCtx.SendQueue > 0 {
		sendQueueLimit = portCtx.SendQueue
	}

	// Create connection context - use Background() not r.Context()
	// because the WebSocket connection lives beyond the HTTP request lifecycle
	ctx, cancel := context.WithCancel(context.Background())

	wsConn := &WebSocketConnection{
		ID:            generateConnectionID(),
		conn:          conn,
		subscriptions: make(map[types.SubscriptionType]types.SubscriptionConfig),
		sendChannel:   make(chan []byte, sendQueueLimit),
		closeChannel:  make(chan struct{}),
		ctx:           ctx,
		cancel:        cancel,
		portCtx:       portCtx,
	}

	// Register connection
	ws.connectionsMutex.Lock()
	ws.connections[wsConn.ID] = wsConn
	ws.connectionsMutex.Unlock()

	legacyConn := &types.Connection{
		ID:            wsConn.ID,
		Subscriptions: wsConn.subscriptions,
		SendChannel:   wsConn.sendChannel,
		CloseChannel:  wsConn.closeChannel,
	}
	ws.subscriptionManager.AddConnection(legacyConn)

	// Start connection handlers
	go ws.handleConnection(wsConn)
	go ws.handleSend(wsConn)
}

// handleConnection processes messages from a WebSocket connection
func (ws *WebSocketServer) handleConnection(wsConn *WebSocketConnection) {
	defer ws.closeConnection(wsConn)

	wsConn.conn.SetReadLimit(512 * 1024) // 512KB max message size

	// Set up pong handler to reset read deadline on pong received
	wsConn.conn.SetPongHandler(func(string) error {
		wsConn.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	// Start ping goroutine to keep connection alive
	go ws.pingLoop(wsConn)

	// Read loop - this is blocking and runs until error or close
	for {
		wsConn.conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		// Read message from WebSocket (blocking)
		_, message, err := wsConn.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
				wsLog().Debug("WebSocket read error", "err", err)
			}
			return
		}

		select {
		case <-wsConn.ctx.Done():
			return
		default:
		}

		// Process message
		ws.handleMessage(wsConn, message)
	}
}

// pingLoop sends periodic pings to keep the connection alive
func (ws *WebSocketServer) pingLoop(wsConn *WebSocketConnection) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-wsConn.ctx.Done():
			return
		case <-ticker.C:
			wsConn.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := wsConn.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				wsLog().Debug("WebSocket ping failed", "err", err)
				return
			}
		}
	}
}

// handleSend processes outgoing messages for a WebSocket connection
func (ws *WebSocketServer) handleSend(wsConn *WebSocketConnection) {
	for {
		select {
		case <-wsConn.ctx.Done():
			return
		case <-wsConn.closeChannel:
			return
		case message := <-wsConn.sendChannel:
			wsConn.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := wsConn.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				wsLog().Debug("WebSocket send failed", "err", err)
				return
			}
		}
	}
}

// handleMessage processes a single message from WebSocket
func (ws *WebSocketServer) handleMessage(wsConn *WebSocketConnection, message []byte) {
	// Parse WebSocket command - XRPL format has command and params at top level
	var cmdMap map[string]interface{}
	if err := json.Unmarshal(message, &cmdMap); err != nil {
		ws.sendError(wsConn, types.RpcErrorInvalidParams("Invalid JSON: "+err.Error()), nil)
		return
	}

	// Extract command
	command, ok := cmdMap["command"].(string)
	if !ok || command == "" {
		ws.sendError(wsConn, types.NewRpcError(types.RpcMISSING_COMMAND, "missingCommand", "missingCommand", "Missing command field"), nil)
		return
	}

	// Extract ID (optional)
	var id interface{}
	if idVal, exists := cmdMap["id"]; exists {
		id = idVal
	}

	// Build cmd struct
	cmd := types.WebSocketCommand{
		Command: command,
		ID:      id,
	}

	// Remove command and id from params, pass the rest as params
	delete(cmdMap, "command")
	delete(cmdMap, "id")

	// Handle api_version
	var apiVersion int = types.DefaultApiVersion
	if apiVer, exists := cmdMap["api_version"]; exists {
		if ver, ok := apiVer.(float64); ok {
			apiVersion = int(ver)
		}
		delete(cmdMap, "api_version")
	}

	// Convert remaining fields to params JSON
	if len(cmdMap) > 0 {
		paramsBytes, _ := json.Marshal(cmdMap)
		cmd.Params = paramsBytes
	}

	clientIP := getWebSocketClientIP(wsConn.conn)
	role := roleForRequest(clientIP, wsConn.portCtx)
	wsLog().Debug("ws request", "cmd", cmd.Command, "remoteAddr", wsConn.conn.RemoteAddr().String(), "clientIP", clientIP, "role", role, "isAdmin", role == types.RoleAdmin)
	rpcCtx := &types.RpcContext{
		Context:    wsConn.ctx,
		Role:       role,
		ApiVersion: apiVersion,
		IsAdmin:    role == types.RoleAdmin,
		ClientIP:   clientIP,
		Services:   ws.services,
	}

	// Handle subscription commands specially
	switch cmd.Command {
	case "subscribe":
		ws.handleSubscribe(wsConn, rpcCtx, cmd)
		return
	case "unsubscribe":
		ws.handleUnsubscribe(wsConn, rpcCtx, cmd)
		return
	case "path_find":
		ws.handlePathFind(wsConn, rpcCtx, cmd)
		return
	}

	// Handle regular RPC methods
	ws.handleRPCMethod(wsConn, rpcCtx, cmd)
}

// handleSubscribe processes subscribe commands
func (ws *WebSocketServer) handleSubscribe(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	var request types.SubscriptionRequest
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &request); err != nil {
			ws.sendError(wsConn, types.RpcErrorInvalidParams("Invalid subscription parameters: "+err.Error()), cmd.ID)
			return
		}
	}

	// Handle subscription through subscription manager
	conn := &types.Connection{
		ID:            wsConn.ID,
		Subscriptions: wsConn.subscriptions,
		SendChannel:   wsConn.sendChannel,
		CloseChannel:  wsConn.closeChannel,
	}
	if err := ws.subscriptionManager.HandleSubscribe(conn, request); err != nil {
		ws.sendError(wsConn, err, cmd.ID)
		return
	}

	// Build response - rippled returns ledger info when subscribing to ledger stream
	result := make(map[string]interface{})

	// Check if subscribing to ledger stream - return current ledger info
	for _, stream := range request.Streams {
		if stream == types.SubLedger {
			if ws.ledgerInfoProvider != nil {
				info := ws.ledgerInfoProvider.GetCurrentLedgerInfo()
				if info != nil {
					result["ledger_index"] = info.LedgerIndex
					result["ledger_hash"] = info.LedgerHash
					result["ledger_time"] = info.LedgerTime
					result["fee_base"] = info.FeeBase
					result["fee_ref"] = info.FeeRef
					result["reserve_base"] = info.ReserveBase
					result["reserve_inc"] = info.ReserveInc
					if info.ValidatedLedgers != "" {
						result["validated_ledgers"] = info.ValidatedLedgers
					}
				}
			}
			break
		}
	}

	response := types.WebSocketResponse{
		Type:       "response",
		ID:         cmd.ID,
		Status:     "success",
		Result:     result,
		ApiVersion: ctx.ApiVersion,
	}
	ws.sendResponse(wsConn, response)
}

// handleUnsubscribe processes unsubscribe commands
func (ws *WebSocketServer) handleUnsubscribe(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	var request types.SubscriptionRequest
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &request); err != nil {
			ws.sendError(wsConn, types.RpcErrorInvalidParams("Invalid unsubscription parameters: "+err.Error()), cmd.ID)
			return
		}
	}

	conn := &types.Connection{
		ID:            wsConn.ID,
		Subscriptions: wsConn.subscriptions,
		SendChannel:   wsConn.sendChannel,
		CloseChannel:  wsConn.closeChannel,
	}
	if err := ws.subscriptionManager.HandleUnsubscribe(conn, request); err != nil {
		ws.sendError(wsConn, err, cmd.ID)
		return
	}

	response := types.WebSocketResponse{
		Type:       "response",
		ID:         cmd.ID,
		Status:     "success",
		Result:     map[string]interface{}{},
		ApiVersion: ctx.ApiVersion,
	}
	ws.sendResponse(wsConn, response)
}

// handlePathFind processes path_find commands (special WebSocket-only method).
// Subcommands: "create" (start session), "close" (stop session), "status" (get current paths).
// Reference: rippled PathFind.cpp
func (ws *WebSocketServer) handlePathFind(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	// Parse subcommand
	var sub struct {
		Subcommand string `json:"subcommand"`
	}
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &sub); err != nil {
			ws.sendError(wsConn, types.RpcErrorInvalidParams("Invalid parameters: "+err.Error()), cmd.ID)
			return
		}
	}

	switch sub.Subcommand {
	case "create":
		ws.handlePathFindCreate(wsConn, ctx, cmd)
	case "close":
		ws.handlePathFindClose(wsConn, ctx, cmd)
	case "status":
		ws.handlePathFindStatus(wsConn, ctx, cmd)
	default:
		ws.sendError(wsConn, types.RpcErrorInvalidParams("Invalid field 'subcommand'."), cmd.ID)
	}
}

// handlePathFindCreate creates a new persistent pathfinding session.
// Any existing session on this connection is replaced (matching rippled).
func (ws *WebSocketServer) handlePathFindCreate(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	// Parse and validate parameters
	session, rpcErr := ParseAndCreateSession(cmd.Params, cmd.ID)
	if rpcErr != nil {
		ws.sendError(wsConn, rpcErr, cmd.ID)
		return
	}

	if ctx.Services == nil || ctx.Services.Ledger == nil {
		ws.sendError(wsConn, types.NewRpcError(types.RpcNO_CURRENT, "noCurrent", "noCurrent",
			"No closed ledger available"), cmd.ID)
		return
	}
	view, err := ctx.Services.Ledger.GetClosedLedgerView()
	if err != nil {
		ws.sendError(wsConn, types.NewRpcError(types.RpcNO_CURRENT, "noCurrent", "noCurrent",
			"No closed ledger available"), cmd.ID)
		return
	}

	// Run initial pathfinding
	event := session.Execute(view)

	// Store session on connection (replaces any existing one, matching rippled)
	wsConn.mutex.Lock()
	wsConn.pathFindSession = session
	wsConn.mutex.Unlock()

	// Send initial result as response
	response := types.WebSocketResponse{
		Type:       "response",
		ID:         cmd.ID,
		Status:     "success",
		Result:     event,
		ApiVersion: ctx.ApiVersion,
	}
	ws.sendResponse(wsConn, response)
}

// handlePathFindClose closes the active pathfinding session on this connection.
func (ws *WebSocketServer) handlePathFindClose(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	wsConn.mutex.Lock()
	session := wsConn.pathFindSession
	wsConn.pathFindSession = nil
	wsConn.mutex.Unlock()

	if session == nil {
		ws.sendError(wsConn, types.RpcErrorNoPathRequest(), cmd.ID)
		return
	}

	response := types.WebSocketResponse{
		Type:       "response",
		ID:         cmd.ID,
		Status:     "success",
		Result:     map[string]interface{}{"closed": true},
		ApiVersion: ctx.ApiVersion,
	}
	ws.sendResponse(wsConn, response)
}

// handlePathFindStatus returns the current status of the active pathfinding session.
func (ws *WebSocketServer) handlePathFindStatus(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	wsConn.mutex.RLock()
	session := wsConn.pathFindSession
	wsConn.mutex.RUnlock()

	if session == nil {
		ws.sendError(wsConn, types.RpcErrorNoPathRequest(), cmd.ID)
		return
	}

	event := session.GetLastResult()

	response := types.WebSocketResponse{
		Type:       "response",
		ID:         cmd.ID,
		Status:     "success",
		Result:     event,
		ApiVersion: ctx.ApiVersion,
	}
	ws.sendResponse(wsConn, response)
}

// UpdatePathFindSessions re-runs pathfinding for all active sessions on ledger close.
// Called from the ledger close callback in server.go.
func (ws *WebSocketServer) UpdatePathFindSessions(getView func() (types.LedgerStateView, error)) {
	ws.connectionsMutex.RLock()
	// Collect connections with active sessions
	var activeSessions []*WebSocketConnection
	for _, conn := range ws.connections {
		conn.mutex.RLock()
		if conn.pathFindSession != nil {
			activeSessions = append(activeSessions, conn)
		}
		conn.mutex.RUnlock()
	}
	ws.connectionsMutex.RUnlock()

	if len(activeSessions) == 0 {
		return
	}

	view, err := getView()
	if err != nil {
		wsLog().Error("Failed to get ledger view for path_find updates", "err", err)
		return
	}

	for _, conn := range activeSessions {
		conn.mutex.RLock()
		session := conn.pathFindSession
		conn.mutex.RUnlock()

		if session == nil {
			continue
		}

		event := session.Execute(view)

		data, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			continue
		}

		select {
		case conn.sendChannel <- data:
		default:
			// Channel full, skip this update
		}
	}
}

// handleRPCMethod processes regular RPC method calls over WebSocket
func (ws *WebSocketServer) handleRPCMethod(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	handler, exists := ws.methodRegistry.Get(cmd.Command)
	if !exists {
		ws.sendError(wsConn, types.RpcErrorMethodNotFound(cmd.Command), cmd.ID)
		return
	}

	if ctx.Role < handler.RequiredRole() {
		ws.sendError(wsConn, types.NewRpcError(types.RpcCOMMAND_UNTRUSTED, "commandUntrusted", "commandUntrusted",
			fmt.Sprintf("Command '%s' requires higher privileges", cmd.Command)), cmd.ID)
		return
	}

	result, rpcErr := handler.Handle(ctx, cmd.Params)
	if rpcErr != nil {
		ws.sendError(wsConn, rpcErr, cmd.ID)
	} else {
		response := types.WebSocketResponse{
			Type:       "response",
			ID:         cmd.ID,
			Status:     "success",
			Result:     result,
			ApiVersion: ctx.ApiVersion,
		}
		ws.sendResponse(wsConn, response)
	}
}

// WebSocketResponseOptions contains optional fields for WebSocket responses
type WebSocketResponseOptions struct {
	Warning   string                // "load" when approaching rate limit
	Warnings  []types.WarningObject // Array of warning objects
	Forwarded bool                  // True if forwarded from Clio to P2P server
}

// sendResponse sends a WebSocket response
func (ws *WebSocketServer) sendResponse(wsConn *WebSocketConnection, response types.WebSocketResponse) {
	ws.sendResponseWithOptions(wsConn, response, nil)
}

// sendResponseWithOptions sends a WebSocket response with optional warning/forwarded fields
func (ws *WebSocketServer) sendResponseWithOptions(wsConn *WebSocketConnection, response types.WebSocketResponse, opts *types.WebSocketResponseOptions) {
	// Apply optional fields if provided
	if opts != nil {
		response.Warning = opts.Warning
		response.Warnings = opts.Warnings
		response.Forwarded = opts.Forwarded
	}

	data, err := json.Marshal(response)
	if err != nil {
		wsLog().Error("Failed to marshal WebSocket response", "err", err)
		return
	}

	select {
	case wsConn.sendChannel <- data:
		// Response sent
	case <-wsConn.ctx.Done():
		// Connection closed
	default:
		// Channel full, close connection
		wsLog().Warn("WebSocket send channel full", "connID", wsConn.ID)
		ws.closeConnection(wsConn)
	}
}

// sendError sends a WebSocket error response with flat error fields (XRPL format)
func (ws *WebSocketServer) sendError(wsConn *WebSocketConnection, rpcErr *types.RpcError, id interface{}) {
	ws.sendErrorWithOptions(wsConn, rpcErr, id, nil)
}

// sendErrorWithOptions sends a WebSocket error response with optional warning/forwarded fields
// Per XRPL WebSocket spec, error fields are at top level (not nested in result)
func (ws *WebSocketServer) sendErrorWithOptions(wsConn *WebSocketConnection, rpcErr *types.RpcError, id interface{}, opts *types.WebSocketResponseOptions) {
	response := types.WebSocketResponse{
		Type:         "response",
		Status:       "error",
		ID:           id,
		Error:        rpcErr.ErrorString,
		ErrorCode:    rpcErr.Code,
		ErrorMessage: rpcErr.Message,
	}

	// Apply optional fields if provided
	if opts != nil {
		response.Warning = opts.Warning
		response.Warnings = opts.Warnings
		response.Forwarded = opts.Forwarded
	}

	data, err := json.Marshal(response)
	if err != nil {
		wsLog().Error("Failed to marshal WebSocket error response", "err", err)
		return
	}

	select {
	case wsConn.sendChannel <- data:
		// Response sent
	case <-wsConn.ctx.Done():
		// Connection closed
	default:
		// Channel full, close connection
		wsLog().Warn("WebSocket send channel full", "connID", wsConn.ID)
		ws.closeConnection(wsConn)
	}
}

// closeConnection closes a WebSocket connection
func (ws *WebSocketServer) closeConnection(wsConn *WebSocketConnection) {
	// Cancel context
	wsConn.cancel()

	// Clear any active path_find session
	wsConn.mutex.Lock()
	wsConn.pathFindSession = nil
	wsConn.mutex.Unlock()

	ws.connectionsMutex.Lock()
	delete(ws.connections, wsConn.ID)
	ws.connectionsMutex.Unlock()

	ws.subscriptionManager.RemoveConnection(wsConn.ID)

	// Release per-port connection limiter slot
	if ws.connLimiter != nil && wsConn.portCtx != nil {
		ws.connLimiter.Release(wsConn.portCtx.PortName)
	}

	// Close WebSocket connection
	wsConn.conn.Close()

	wsLog().Debug("WebSocket connection closed", "connID", wsConn.ID)
}

// BroadcastToSubscribers sends a message to all connections subscribed to a specific stream
func (ws *WebSocketServer) BroadcastToSubscribers(msgType types.SubscriptionType, message interface{}) {
	data, err := json.Marshal(message)
	if err != nil {
		wsLog().Error("Failed to marshal broadcast message", "err", err)
		return
	}

	ws.connectionsMutex.RLock()
	defer ws.connectionsMutex.RUnlock()

	for _, conn := range ws.connections {
		conn.mutex.RLock()
		if _, subscribed := conn.subscriptions[msgType]; subscribed {
			select {
			case conn.sendChannel <- data:
				// Message sent
			default:
				// Channel full, skip this connection
				wsLog().Debug("Skipping slow WebSocket connection", "connID", conn.ID)
			}
		}
		conn.mutex.RUnlock()
	}
}

// Helper functions

func generateConnectionID() string {
	// TODO: Generate a proper unique connection ID
	return fmt.Sprintf("conn_%d", time.Now().UnixNano())
}

func getWebSocketClientIP(conn *websocket.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

// RegisterAllMethods registers all RPC methods for WebSocket use
func (ws *WebSocketServer) RegisterAllMethods() {
	// Use the same method registration as HTTP server
	server := &Server{registry: ws.methodRegistry}
	server.registerAllMethods()
}

// GetSubscriptionManager returns the subscription manager for event publishing
func (ws *WebSocketServer) GetSubscriptionManager() *subscription.Manager {
	return ws.subscriptionManager
}

// Close gracefully closes all active WebSocket connections.
func (ws *WebSocketServer) Close() {
	ws.connectionsMutex.Lock()
	defer ws.connectionsMutex.Unlock()
	for _, conn := range ws.connections {
		conn.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutdown"),
		)
		conn.conn.Close()
	}
}
