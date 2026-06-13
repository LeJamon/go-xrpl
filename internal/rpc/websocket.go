package rpc

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/loadtrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/gorilla/websocket"
)

func recoverPanic(where string, connID string) {
	if rec := recover(); rec != nil {
		wsLog().Error("ws goroutine panic", "where", where, "conn", connID, "err", rec, "stack", string(debug.Stack()))
	}
}

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
	urlSubs             *URLSubscriptionRegistry
	peerSourceHolder
	loadTracker *loadtrack.Tracker
	// pingInterval is how often pingLoop sends a keepalive ping. Settable
	// so concurrency tests can drive the ping path without waiting on the
	// production cadence.
	pingInterval time.Duration
	// wg tracks per-connection goroutines (read loop, send pump, ping loop)
	// so Close can join them on shutdown.
	wg sync.WaitGroup
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
	// user is the X-User header captured at upgrade time. Used by
	// roleForRequest for RoleIdentified promotion when the connection
	// came in through a secure_gateway peer.
	user string
	// forwardedFor is the originating client IP carried in the upgrade
	// request's Forwarded / X-Forwarded-For / X-Real-IP header. Used by
	// resolveWSClientIP when the upgrade socket peer is in the per-port
	// SecureGatewayNets allowlist. Mirrors rippled's
	// WSInfoSub::forwarded_for (ServerHandler.cpp:497-501, :580).
	forwardedFor string
	// legacy is the same logical connection viewed through the
	// subscription-manager data model. Created at AddConnection and
	// torn down at closeConnection — kept on the WS struct so the
	// two-map invariant (subscription.Manager.Connections and
	// WebSocketServer.connections always identify the same set) is
	// enforced by a single attach/detach helper rather than
	// independent map operations.
	legacy *types.Connection
}

// NewWebSocketServer creates a new WebSocket server. The provided
// service container is attached to every RpcContext routed through the
// server so handlers reach the ledger via ctx.Services. May be nil for
// test contexts.
func NewWebSocketServer(timeout time.Duration, services *types.ServiceContainer) *WebSocketServer {
	if services != nil && services.ClientLoad == nil {
		services.ClientLoad = types.NewClientLoadShedder()
	}
	ws := &WebSocketServer{
		upgrader: websocket.Upgrader{
			// Accept any Origin, deliberately matching rippled: its WS
			// server never validates the Origin header — access control
			// is done via admin IP nets / port configuration instead.
			CheckOrigin: func(r *http.Request) bool { return true },
			// Don't require specific subprotocol - xrpl.js doesn't use one
		},
		subscriptionManager: subscription.NewManager(),
		methodRegistry:      types.NewMethodRegistry(),
		connections:         make(map[string]*WebSocketConnection),
		timeout:             timeout,
		services:            services,
		loadTracker:         loadtrack.New(),
		pingInterval:        30 * time.Second,
	}
	// The url (RPCSub) registry lives on the WebSocket server because url
	// subscribers share its subscription manager's broadcast fan-out.
	// Exposing it through the service container lets the plain JSON-RPC
	// subscribe/unsubscribe handlers reach it.
	ws.urlSubs = newURLSubscriptionRegistry(ws)
	if services != nil {
		services.URLSubscriptions = ws.urlSubs
	}
	return ws
}

// SetPingInterval overrides the keepalive ping cadence (the operator's
// websocket_ping_frequency key). Non-positive values are ignored. Must
// be called before connections are accepted.
func (ws *WebSocketServer) SetPingInterval(d time.Duration) {
	if d > 0 {
		ws.pingInterval = d
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

func (ws *WebSocketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	portCtx := GetPortContext(r.Context())

	conn, err := ws.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// PortMiddleware acquired a slot for this WS request and delegated its
		// release to closeConnection, which never runs when the upgrade fails.
		// Release here so a malformed upgrade can't permanently leak the slot.
		if ws.connLimiter != nil && portCtx != nil {
			ws.connLimiter.Release(portCtx.PortName)
		}
		wsLog().Error("WebSocket upgrade failed", "err", err)
		return
	}

	sendQueueLimit := DefaultSendQueueLimit
	if portCtx != nil && portCtx.SendQueue > 0 {
		sendQueueLimit = portCtx.SendQueue
	}

	// Use Background() not r.Context() because the WebSocket connection
	// lives beyond the HTTP request lifecycle.
	ctx, cancel := context.WithCancel(context.Background())

	// Capture proxy-attribution headers at upgrade time. They are only
	// consulted when the upgrade socket peer is in the per-port
	// SecureGatewayNets set — see resolveWSClientIP.
	var fwd string
	if f := forwardedForHeader(r); f != "" {
		fwd = f
	} else if xri := r.Header.Get("X-Real-IP"); xri != "" {
		fwd = strings.TrimSpace(xri)
	}

	wsConn := &WebSocketConnection{
		ID:            generateConnectionID(),
		conn:          conn,
		subscriptions: make(map[types.SubscriptionType]types.SubscriptionConfig),
		sendChannel:   make(chan []byte, sendQueueLimit),
		closeChannel:  make(chan struct{}),
		ctx:           ctx,
		cancel:        cancel,
		portCtx:       portCtx,
		user:          userHeader(r),
		forwardedFor:  fwd,
	}

	ws.attachConnection(wsConn)

	ws.wg.Add(2)
	go func() {
		defer ws.wg.Done()
		ws.handleConnection(wsConn)
	}()
	go func() {
		defer ws.wg.Done()
		ws.handleSend(wsConn)
	}()
}

func (ws *WebSocketServer) handleConnection(wsConn *WebSocketConnection) {
	defer ws.closeConnection(wsConn)
	defer recoverPanic("handleConnection", wsConn.ID)

	// Match the HTTP body cap. rippled enforces RPC::Tuning::maxRequestSize
	// (1 MB) on both onWSMessage and processRequest (ServerHandler.cpp:343
	// and :625), so the WS path uses the same byte ceiling as POST.
	wsConn.conn.SetReadLimit(int64(MaxRequestBytes))

	wsConn.conn.SetPongHandler(func(string) error {
		wsConn.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	ws.wg.Add(1)
	go func() {
		defer ws.wg.Done()
		ws.pingLoop(wsConn)
	}()

	for {
		wsConn.conn.SetReadDeadline(time.Now().Add(90 * time.Second))

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

		ws.handleMessage(wsConn, message)
	}
}

func (ws *WebSocketServer) pingLoop(wsConn *WebSocketConnection) {
	defer recoverPanic("pingLoop", wsConn.ID)
	// Fall back to the default when constructed via struct literal: a zero
	// pingInterval would panic NewTicker. Read into a local rather than
	// mutating the shared field from this per-connection goroutine.
	interval := ws.pingInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-wsConn.ctx.Done():
			return
		case <-ticker.C:
			// WriteControl carries its own deadline and serializes against
			// the message-frame writer (handleSend) through gorilla's
			// control-write lock. WriteMessage+SetWriteDeadline here would
			// instead touch the unguarded single-writer state shared with
			// handleSend, racing it (#746).
			if err := wsConn.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				wsLog().Debug("WebSocket ping failed", "err", err)
				return
			}
		}
	}
}

func (ws *WebSocketServer) handleSend(wsConn *WebSocketConnection) {
	defer recoverPanic("handleSend", wsConn.ID)
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
				// Close the socket so the read loop unblocks and tears the
				// connection down now, not at the 90 s read deadline.
				wsConn.closeSocket()
				return
			}
		}
	}
}

func (ws *WebSocketServer) handleMessage(wsConn *WebSocketConnection, message []byte) {
	// Per-message recover so one bad command can't tear down the read
	// loop and drop the connection's pending subscriptions.
	defer func() {
		if rec := recover(); rec != nil {
			wsLog().Error("ws message panic", "conn", wsConn.ID, "err", rec, "stack", string(debug.Stack()))
			ws.sendError(wsConn, types.NewRpcError(types.RpcINTERNAL, "internal", "internal", "Internal server error"), nil)
		}
	}()

	// XRPL WebSocket format: command and id at top level, all other fields are params.
	var cmdMap map[string]any
	if err := json.Unmarshal(message, &cmdMap); err != nil {
		ws.sendError(wsConn, types.RpcErrorInvalidParams("Invalid JSON: "+err.Error()), nil)
		return
	}

	var id any
	if idVal, exists := cmdMap["id"]; exists {
		id = idVal
	}

	// rippled accepts `method` as an alias for `command`, rejecting only when
	// neither is present (or both are present strings that disagree) with a
	// bare missingCommand token that echoes the original request
	// (ServerHandler.cpp:446-468).
	command, ok := resolveWSCommand(cmdMap)
	if !ok {
		ws.sendMissingCommand(wsConn, cmdMap, id)
		return
	}

	cmd := types.WebSocketCommand{
		Command: command,
		ID:      id,
	}

	delete(cmdMap, "command")
	delete(cmdMap, "method")
	delete(cmdMap, "id")

	var apiVersion int = types.DefaultApiVersion
	if apiVer, exists := cmdMap["api_version"]; exists {
		if ver, ok := apiVer.(float64); ok {
			apiVersion = int(ver)
		}
		delete(cmdMap, "api_version")
	}

	if len(cmdMap) > 0 {
		paramsBytes, _ := json.Marshal(cmdMap)
		cmd.Params = paramsBytes
	}

	// Role is always derived from the socket-level peer, never from
	// header-supplied IPs. ClientIP is the peer too, unless the peer is
	// in this port's secure_gateway set — then we substitute the value
	// captured at upgrade time (matches rippled WSInfoSub::forwarded_for,
	// ServerHandler.cpp:497-501).
	peerIP := getWebSocketClientIP(wsConn.conn)
	clientIP := resolveWSClientIP(peerIP, wsConn.forwardedFor, wsConn.portCtx)
	role := roleForRequest(peerIP, wsConn.user, wsConn.portCtx)
	wsLog().Debug("ws request", "cmd", cmd.Command, "remoteAddr", wsConn.conn.RemoteAddr().String(), "clientIP", clientIP, "role", role, "isAdmin", role == types.RoleAdmin)
	dispatchCtx := wsConn.ctx
	var cancel context.CancelFunc
	if ws.timeout > 0 {
		dispatchCtx, cancel = context.WithTimeout(wsConn.ctx, ws.timeout)
		defer cancel()
	}
	rpcCtx := newRpcContext(dispatchCtx, role, apiVersion, clientIP, ws.loadPeerSource(), ws.services)

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

	ws.handleRPCMethod(wsConn, rpcCtx, cmd)
}

func (ws *WebSocketServer) handleSubscribe(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	var request types.SubscriptionRequest
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &request); err != nil {
			ws.sendError(wsConn, types.RpcErrorInvalidParams("Invalid subscription parameters: "+err.Error()), cmd.ID)
			return
		}
	}

	// url requests are server-to-server (RPCSub) subscriptions: events go
	// to the url's subscriber, not to this WebSocket connection.
	if request.HasURL() {
		if !ctx.IsAdmin {
			ws.sendError(wsConn, types.RpcErrorNoPermission("subscribe"), cmd.ID)
			return
		}
		result, rpcErr := ws.urlSubs.Subscribe(ctx, request)
		if rpcErr != nil {
			ws.sendError(wsConn, rpcErr, cmd.ID)
			return
		}
		ws.sendResponse(wsConn, types.WebSocketResponse{
			Type:       "response",
			ID:         cmd.ID,
			Status:     "success",
			Result:     result,
			ApiVersion: ctx.ApiVersion,
		})
		return
	}

	// wsConn.legacy is the same connection the subscription manager already
	// tracks (created in attachConnection, before any message can arrive); it
	// shares the subscriptions map and carries the Disconnect callback a
	// freshly-built copy would lack.
	if err := ws.subscriptionManager.HandleSubscribe(wsConn.legacy, request, ctx.IsAdmin); err != nil {
		ws.sendError(wsConn, err, cmd.ID)
		return
	}

	result := ws.buildSubscribeAck(ctx, request)

	response := types.WebSocketResponse{
		Type:       "response",
		ID:         cmd.ID,
		Status:     "success",
		Result:     result,
		ApiVersion: ctx.ApiVersion,
	}
	ws.sendResponse(wsConn, response)
}

func (ws *WebSocketServer) handleUnsubscribe(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	var request types.SubscriptionRequest
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &request); err != nil {
			ws.sendError(wsConn, types.RpcErrorInvalidParams("Invalid unsubscription parameters: "+err.Error()), cmd.ID)
			return
		}
	}

	// See handleSubscribe: url requests target the RPCSub registry.
	if request.HasURL() {
		if !ctx.IsAdmin {
			ws.sendError(wsConn, types.RpcErrorNoPermission("unsubscribe"), cmd.ID)
			return
		}
		result, rpcErr := ws.urlSubs.Unsubscribe(ctx, request)
		if rpcErr != nil {
			ws.sendError(wsConn, rpcErr, cmd.ID)
			return
		}
		ws.sendResponse(wsConn, types.WebSocketResponse{
			Type:       "response",
			ID:         cmd.ID,
			Status:     "success",
			Result:     result,
			ApiVersion: ctx.ApiVersion,
		})
		return
	}

	if err := ws.subscriptionManager.HandleUnsubscribe(wsConn.legacy, request, ctx.IsAdmin); err != nil {
		ws.sendError(wsConn, err, cmd.ID)
		return
	}

	response := types.WebSocketResponse{
		Type:       "response",
		ID:         cmd.ID,
		Status:     "success",
		Result:     map[string]any{},
		ApiVersion: ctx.ApiVersion,
	}
	ws.sendResponse(wsConn, response)
}

// handlePathFind processes path_find commands (special WebSocket-only method).
// Subcommands: "create" (start session), "close" (stop session), "status" (get current paths).
// Reference: rippled PathFind.cpp
func (ws *WebSocketServer) handlePathFind(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
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
	release, rpcErr := handlers.AcquirePathfind(ctx)
	if rpcErr != nil {
		ws.sendError(wsConn, rpcErr, cmd.ID)
		return
	}
	defer release()

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

	event := session.Execute(view)

	// Replace any existing session on this connection (matches rippled).
	wsConn.mutex.Lock()
	wsConn.pathFindSession = session
	wsConn.mutex.Unlock()

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
		Result:     map[string]any{"closed": true},
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

		// Deliver through the shared TrySend so a persistently slow path-find
		// subscriber accrues drops and is disconnected like any other outbound
		// path, rather than silently skipping forever on a bare select/default.
		conn.legacy.TrySend(data)
	}
}

func (ws *WebSocketServer) handleRPCMethod(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	// Shared dispatch core (registry → admin gate → conditionMet →
	// api-version → busy/load gates → handle → finalize), identical to the
	// HTTP path. The WS-specific admin gate returns rpcFORBIDDEN instead of
	// rpcNO_PERMISSION (ServerHandler.cpp:482-486): when requestRole returns
	// Role::FORBID for an admin-required command, rippled writes
	// rpcError(rpcFORBIDDEN) before doCommand ever runs.
	result, rpcErr := dispatchMethod(ws.methodRegistry, ws.loadTracker, ws.services, ctx, cmd.Command, cmd.Params, types.RpcErrorForbidden, wsLog())
	opts := wsLoadWarningOpts(ctx)
	if rpcErr != nil {
		ws.sendErrorWithOptions(wsConn, rpcErr, cmd.ID, opts)
		return
	}
	ws.sendResponseWithOptions(wsConn, types.WebSocketResponse{
		Type:       "response",
		ID:         cmd.ID,
		Status:     "success",
		Result:     result,
		ApiVersion: ctx.ApiVersion,
	}, opts)
}

// wsLoadWarningOpts surfaces rippled's warning:"load" on a WS reply when the
// dispatch crossed the resource warn threshold (recorded on ctx by
// finalizeLoad), and returns nil otherwise.
func wsLoadWarningOpts(ctx *types.RpcContext) *types.WebSocketResponseOptions {
	if ctx != nil && ctx.LoadWarning {
		return &types.WebSocketResponseOptions{Warning: "load"}
	}
	return nil
}

func (ws *WebSocketServer) sendResponse(wsConn *WebSocketConnection, response types.WebSocketResponse) {
	ws.sendResponseWithOptions(wsConn, response, nil)
}

func (ws *WebSocketServer) sendResponseWithOptions(wsConn *WebSocketConnection, response types.WebSocketResponse, opts *types.WebSocketResponseOptions) {
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
	ws.deliver(wsConn, data)
}

// deliver queues an already-marshalled WS frame through the shared TrySend so
// per-request response delivery and broadcast delivery use the same
// consecutive-drop counter and the same disconnect-on-N-drops threshold. Test
// fixtures may build a wsConn without a legacy peer; those fall back to a
// non-blocking channel send so unit tests stay self-contained.
func (ws *WebSocketServer) deliver(wsConn *WebSocketConnection, data []byte) {
	if wsConn.legacy != nil {
		if !wsConn.legacy.TrySend(data) {
			wsLog().Debug("WebSocket send dropped (slow consumer)", "connID", wsConn.ID)
		}
		return
	}
	select {
	case wsConn.sendChannel <- data:
	case <-wsConn.ctx.Done():
	default:
		wsLog().Warn("WebSocket send channel full", "connID", wsConn.ID)
	}
}

// resolveWSCommand resolves the WS command name from the incoming JSON,
// accepting `method` as an alias for `command` (ServerHandler.cpp:446-475).
// ok is false — meaning the caller emits missingCommand — when neither is a
// non-empty string, or both are present strings that disagree.
func resolveWSCommand(m map[string]any) (string, bool) {
	cmd, cmdOK := m["command"].(string)
	method, methodOK := m["method"].(string)
	switch {
	case cmdOK && methodOK:
		if cmd != method {
			return "", false
		}
		return cmd, cmd != ""
	case cmdOK:
		return cmd, cmd != ""
	case methodOK:
		return method, method != ""
	default:
		return "", false
	}
}

// sendMissingCommand emits rippled's bare missingCommand reply: a lone
// `error` token (no error_code/error_message) plus the echoed request and id
// (ServerHandler.cpp:452-468). Credentials in the echo are redacted — a
// deliberate goxrpl superset of rippled's raw echo.
func (ws *WebSocketServer) sendMissingCommand(wsConn *WebSocketConnection, request map[string]any, id any) {
	echo := make(map[string]any, len(request))
	maps.Copy(echo, request)
	redactCredentials(echo)
	data, err := json.Marshal(types.WebSocketResponse{
		Type:    "response",
		Status:  "error",
		Error:   "missingCommand",
		Request: echo,
		ID:      id,
	})
	if err != nil {
		wsLog().Error("Failed to marshal missingCommand response", "err", err)
		return
	}
	ws.deliver(wsConn, data)
}

func (ws *WebSocketServer) sendError(wsConn *WebSocketConnection, rpcErr *types.RpcError, id any) {
	ws.sendErrorWithOptions(wsConn, rpcErr, id, nil)
}

// sendErrorWithOptions writes an XRPL-format error: error fields are at top
// level (not nested in result) per the WebSocket spec.
func (ws *WebSocketServer) sendErrorWithOptions(wsConn *WebSocketConnection, rpcErr *types.RpcError, id any, opts *types.WebSocketResponseOptions) {
	response := types.WebSocketResponse{
		Type:   "response",
		Status: "error",
		ID:     id,
		Error:  rpcErr.ErrorString,
	}
	// Bare-token errors carry only `error` on the wire (rippled's direct
	// jvResult[jss::error] path); leave error_code/error_message zero so
	// omitempty drops them.
	if !rpcErr.IsBareToken() {
		response.ErrorCode = rpcErr.Code
		response.ErrorMessage = rpcErr.Message
	}

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
	ws.deliver(wsConn, data)
}

// attachConnection is the single point at which a new WS connection
// becomes visible to both the per-server connection map and the
// subscription manager. Pairing this with detachConnection makes it
// impossible for the two maps to drift on Add/Remove ordering — the
// "duplicated connection state" concern flagged in the #428 audit.
func (ws *WebSocketServer) attachConnection(wsConn *WebSocketConnection) {
	legacy := &types.Connection{
		ID:            wsConn.ID,
		Subscriptions: wsConn.subscriptions,
		SendChannel:   wsConn.sendChannel,
		CloseChannel:  wsConn.closeChannel,
		// Subscription-manager-driven disconnect closes the socket (not just
		// cancels the ctx) so a persistently slow subscriber is torn down
		// immediately — cancel alone leaves the read loop blocked in
		// ReadMessage until the 90 s deadline.
		Disconnect: wsConn.closeSocket,
	}
	wsConn.legacy = legacy
	ws.connectionsMutex.Lock()
	ws.connections[wsConn.ID] = wsConn
	ws.connectionsMutex.Unlock()
	ws.subscriptionManager.AddConnection(legacy)
}

// detachConnection is the inverse of attachConnection.
func (ws *WebSocketServer) detachConnection(wsConn *WebSocketConnection) {
	ws.connectionsMutex.Lock()
	delete(ws.connections, wsConn.ID)
	ws.connectionsMutex.Unlock()
	ws.subscriptionManager.RemoveConnection(wsConn.ID)
}

// closeSocket cancels the connection context and closes the underlying
// socket. Closing the socket unblocks a read loop parked in ReadMessage
// immediately, so closeConnection (and the conn-limit slot release) run
// without waiting out the 90 s read deadline. Used by the slow-consumer
// Disconnect callback and the send-error path; idempotent — closeConnection
// closes again and gorilla tolerates the double close.
func (wsConn *WebSocketConnection) closeSocket() {
	wsConn.cancel()
	wsConn.conn.Close()
}

func (ws *WebSocketServer) closeConnection(wsConn *WebSocketConnection) {
	wsConn.cancel()

	wsConn.mutex.Lock()
	wsConn.pathFindSession = nil
	wsConn.mutex.Unlock()

	ws.detachConnection(wsConn)

	if ws.connLimiter != nil && wsConn.portCtx != nil {
		ws.connLimiter.Release(wsConn.portCtx.PortName)
	}

	wsConn.conn.Close()

	wsLog().Debug("WebSocket connection closed", "connID", wsConn.ID)
}

// buildSubscribeAck assembles the subscribe response payload shared by the
// WebSocket and url (RPCSub) subscribe paths: current ledger info when the
// ledger stream is among the requested streams, and a synthetic book-offers
// snapshot for any `snapshot:true` book.
//
// The ledger ack field set mirrors rippled subLedger: fee_ref only while
// XRPFees is disabled, network_id always present; per-ledger pubLedger
// events (LedgerCloseEvent) carry txn_count separately. The snapshot block
// mirrors rippled
// Subscribe.cpp:339-394: when snapshot is set, the response carries `offers`
// (or `bids`/`asks` if `both` is set) populated by NetworkOPs::getBookPage.
// It reuses the ledger service's GetBookOffers — the same code path the
// book_offers RPC uses — so the snapshot a subscriber gets in the ack is
// identical to what they would have read with a separate book_offers call.
func (ws *WebSocketServer) buildSubscribeAck(ctx *types.RpcContext, request types.SubscriptionRequest) map[string]any {
	result := make(map[string]any)

	if slices.Contains(request.Streams, types.SubLedger) {
		if ws.ledgerInfoProvider != nil {
			info := ws.ledgerInfoProvider.GetCurrentLedgerInfo()
			if info != nil {
				result["ledger_index"] = info.LedgerIndex
				result["ledger_hash"] = info.LedgerHash
				result["ledger_time"] = info.LedgerTime
				result["fee_base"] = info.FeeBase
				// rippled emits the deprecated fee_ref only while XRPFees
				// is disabled; network_id is always present.
				if !info.XRPFeesEnabled {
					result["fee_ref"] = info.FeeRef
				}
				result["reserve_base"] = info.ReserveBase
				result["reserve_inc"] = info.ReserveInc
				result["network_id"] = info.NetworkID
				if info.ValidatedLedgers != "" {
					result["validated_ledgers"] = info.ValidatedLedgers
				}
			}
		}
	}

	for _, book := range request.Books {
		if !book.Snapshot || ctx.Services == nil || ctx.Services.Ledger == nil {
			continue
		}
		var takerGets, takerPays types.CurrencySpec
		if err := json.Unmarshal(book.TakerGets, &takerGets); err != nil {
			continue
		}
		if err := json.Unmarshal(book.TakerPays, &takerPays); err != nil {
			continue
		}
		gets := types.Amount{Currency: takerGets.Currency, Issuer: takerGets.Issuer}
		pays := types.Amount{Currency: takerPays.Currency, Issuer: takerPays.Issuer}
		if book.Both {
			bids, _ := ws.snapshotBook(ctx, gets, pays, book.Taker)
			asks, _ := ws.snapshotBook(ctx, pays, gets, book.Taker)
			if bids != nil {
				result["bids"] = appendOffers(result["bids"], bids)
			}
			if asks != nil {
				result["asks"] = appendOffers(result["asks"], asks)
			}
			continue
		}
		offers, _ := ws.snapshotBook(ctx, gets, pays, book.Taker)
		if offers != nil {
			result["offers"] = appendOffers(result["offers"], offers)
		}
	}

	return result
}

// snapshotBook is the WS-side shim around the LedgerService's
// GetBookOffers. Returns the offers slice ready to embed in the
// subscribe ack. Errors are squashed — a snapshot failure mustn't
// reject the entire subscribe (rippled Subscribe.cpp:339-394 ignores
// the snapshot block on lookup failure too).
func (ws *WebSocketServer) snapshotBook(ctx *types.RpcContext, takerGets, takerPays types.Amount, taker string) ([]types.BookOffer, error) {
	if ctx == nil || ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, nil
	}
	res, err := ctx.Services.Ledger.GetBookOffers(ctx.Context, takerGets, takerPays, taker, "", "current", DefaultBookSnapshotLimit, "", false)
	if err != nil || res == nil {
		return nil, err
	}
	return res.Offers, nil
}

// DefaultBookSnapshotLimit caps the synthetic snapshot returned in the
// subscribe ack — keeps a noisy market from blowing the response size
// past the WebSocket frame limit. Matches rippled's
// RPC::Tuning::bookOffers.rdefault used in Subscribe.cpp:349-356.
const DefaultBookSnapshotLimit uint32 = 60

func appendOffers(prev any, more []types.BookOffer) []types.BookOffer {
	if prev == nil {
		return more
	}
	if existing, ok := prev.([]types.BookOffer); ok {
		return append(existing, more...)
	}
	return more
}

// BroadcastToSubscribers sends a message to all connections subscribed to
// a specific stream. Iteration runs through the subscription Manager so
// the per-connection subscription map is read under the same mutex
// HandleSubscribe / HandleUnsubscribe write under (#428 race fix).
func (ws *WebSocketServer) BroadcastToSubscribers(msgType types.SubscriptionType, message any) {
	data, err := json.Marshal(message)
	if err != nil {
		wsLog().Error("Failed to marshal broadcast message", "err", err)
		return
	}
	ws.subscriptionManager.BroadcastToStream(msgType, data, nil)
}

var connectionIDSeq atomic.Uint64

// generateConnectionID returns `conn_<seq>_<random>`. The atomic seq
// avoids collisions under same-nanosecond accept bursts; the random
// suffix keeps IDs unguessable so they can't be used as cross-connection
// references.
func generateConnectionID() string {
	seq := connectionIDSeq.Add(1)
	var rnd [6]byte
	if _, err := cryptorand.Read(rnd[:]); err != nil {
		return fmt.Sprintf("conn_%d_%x", seq, time.Now().UnixNano())
	}
	return fmt.Sprintf("conn_%d_%x", seq, rnd)
}

func getWebSocketClientIP(conn *websocket.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

// resolveWSClientIP returns the attributed client IP for a WebSocket
// dispatch. If the peer is in this port's SecureGatewayNets allowlist
// and the upgrade captured a Forwarded / X-Forwarded-For / X-Real-IP
// value, that value is returned; otherwise the socket peer is returned.
// Role decisions never consult this — see roleForRequest.
func resolveWSClientIP(peerIP, upgradeForwardedFor string, portCtx *PortContext) string {
	if upgradeForwardedFor == "" || portCtx == nil || len(portCtx.SecureGatewayNets) == 0 {
		return peerIP
	}
	parsed := net.ParseIP(peerIP)
	if parsed == nil || !config.IPInNets(parsed, portCtx.SecureGatewayNets) {
		return peerIP
	}
	return upgradeForwardedFor
}

// RegisterAllMethods registers every RPC method available on the WebSocket
// endpoint. subscribe/unsubscribe are part of the common table (as in
// rippled); the WebSocket dispatch intercepts both before registry lookup
// and runs the real subscription implementation.
func (ws *WebSocketServer) RegisterAllMethods() {
	handlers.RegisterAll(ws.methodRegistry)
}

// GetSubscriptionManager returns the subscription manager for event publishing
func (ws *WebSocketServer) GetSubscriptionManager() *subscription.Manager {
	return ws.subscriptionManager
}

// Close gracefully closes all active WebSocket connections and url (RPCSub)
// subscriptions, waiting for all per-connection goroutines (read loop, send
// pump, ping loop) and url delivery loops to exit. The wait is bounded by
// ctx so a misbehaving handler cannot stall shutdown indefinitely; if ctx
// expires first, Close returns ctx.Err().
func (ws *WebSocketServer) Close(ctx context.Context) error {
	ws.connectionsMutex.Lock()
	for _, conn := range ws.connections {
		// WriteControl (not WriteMessage) so the shutdown close frame
		// serializes against a possibly-still-running handleSend instead of
		// racing its message-frame write (#746).
		conn.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutdown"),
			time.Now().Add(10*time.Second),
		)
		conn.cancel()
		conn.conn.Close()
	}
	ws.connectionsMutex.Unlock()

	done := make(chan struct{})
	go func() {
		ws.wg.Wait()
		ws.urlSubs.Close()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
