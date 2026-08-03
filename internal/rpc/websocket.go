package rpc

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
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

const maxSendQueueLimit = 1<<16 - 1

func resolveSendQueueLimit(portCtx *PortContext) (int, error) {
	if portCtx == nil || portCtx.SendQueue == 0 {
		return DefaultSendQueueLimit, nil
	}
	if portCtx.SendQueue < 1 || portCtx.SendQueue > maxSendQueueLimit {
		return 0, fmt.Errorf("send_queue_limit must be 0 (default) or between 1 and %d, got %d", maxSendQueueLimit, portCtx.SendQueue)
	}
	return portCtx.SendQueue, nil
}

// WebSocketServer handles WebSocket connections for real-time subscriptions
type WebSocketServer struct {
	upgrader            websocket.Upgrader
	subscriptionManager *subscription.Manager
	methodRegistry      *types.MethodRegistry
	connections         map[string]*websocketConnection
	connectionsMutex    sync.RWMutex
	closing             bool
	timeout             time.Duration
	ledgerInfoProvider  types.LedgerInfoProvider
	services            *types.ServiceContainer
	urlSubs             *URLSubscriptionRegistry
	peerSourceHolder
	loadTracker       *loadtrack.Tracker
	pathFindRefreshMu sync.Mutex
	pathFindRefresh   *pathFindRefreshManager
	// pingInterval is how often pingLoop sends a keepalive ping. Settable
	// so concurrency tests can drive the ping path without waiting on the
	// production cadence.
	pingInterval time.Duration
	// wg tracks admitted HTTP handlers and per-connection goroutines so Close
	// can join them on shutdown.
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeDone chan struct{}
	forceDone chan struct{}
}

// websocketConnection owns the transport-specific state for one logical
// client. Subscription and lifetime state lives in the embedded canonical
// connection shared with the subscription manager.
type websocketConnection struct {
	*types.Connection
	conn               *websocket.Conn
	mutex              sync.RWMutex
	pathFindSession    *PathFindSession // At most one active path_find session per connection
	pathFindGeneration uint64
	pathFindRefresh    *pathFindRefreshManager
	portCtx            *PortContext // per-port config for role determination
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
	closeOnce    sync.Once
}

// NewWebSocketServer creates a new WebSocket server. The provided
// service container is attached to every RpcContext routed through the
// server so handlers reach the ledger via ctx.Services. May be nil for
// test contexts.
func NewWebSocketServer(timeout time.Duration, services *types.ServiceContainer) *WebSocketServer {
	return NewWebSocketServerWithLoadTracker(timeout, services, nil)
}

// NewWebSocketServerWithLoadTracker creates a WebSocket server using tracker
// for transport-level admission and charging. A nil tracker preserves
// NewWebSocketServer's standalone default.
func NewWebSocketServerWithLoadTracker(timeout time.Duration, services *types.ServiceContainer, tracker *loadtrack.Tracker) *WebSocketServer {
	if tracker == nil {
		tracker = loadtrack.New()
	}
	if services != nil && services.ClientLoad == nil {
		services.ClientLoad = types.NewClientLoadShedder()
	}
	ws := &WebSocketServer{
		upgrader: websocket.Upgrader{
			// The shared transport middleware validates Origin before this
			// upgrader runs; direct callers are checked by ServeHTTP.
			CheckOrigin: func(r *http.Request) bool { return true },
			// Don't require specific subprotocol - xrpl.js doesn't use one
		},
		subscriptionManager: subscription.NewManager(),
		methodRegistry:      types.NewMethodRegistry(),
		connections:         make(map[string]*websocketConnection),
		timeout:             timeout,
		services:            services,
		loadTracker:         tracker,
		pingInterval:        30 * time.Second,
		closeDone:           make(chan struct{}),
		forceDone:           make(chan struct{}),
	}
	ws.pathFindRefresh = newPathFindRefreshManager(ws)
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

func (ws *WebSocketServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	portCtx := GetPortContext(r.Context())
	if !transportAuthorized(r.Context()) && !authorizeTransport(w, r, portCtx) {
		return
	}

	sendQueueLimit, err := resolveSendQueueLimit(portCtx)
	if err != nil {
		r.Close = true
		w.Header().Set("Connection", "close")
		writePlainHTTPError(w, http.StatusInternalServerError, "invalid WebSocket configuration: "+err.Error())
		wsLog().Error("invalid WebSocket send queue limit", "err", err)
		return
	}

	ws.connectionsMutex.Lock()
	if ws.closing {
		ws.connectionsMutex.Unlock()
		http.Error(w, "server shutting down", http.StatusServiceUnavailable)
		return
	}
	ws.wg.Add(1)
	ws.connectionsMutex.Unlock()
	defer ws.wg.Done()

	// If the handshake fails, let net/http close the underlying connection so
	// the listener-owned admission slot is released. Successful upgrades
	// hijack the connection and own its lifetime independently.
	r.Close = true
	conn, err := ws.upgrader.Upgrade(w, r, nil)
	if err != nil {
		wsLog().Error("WebSocket upgrade failed", "err", err)
		return
	}

	// Use Background() not r.Context() because the WebSocket connection
	// lives beyond the HTTP request lifecycle.
	// Capture proxy-attribution headers at upgrade time. They are only
	// consulted when the upgrade socket peer is in the per-port
	// SecureGatewayNets set — see resolveWSClientIP.
	var fwd string
	if f := forwardedForHeader(r); f != "" {
		fwd = f
	} else if xri := r.Header.Get("X-Real-IP"); xri != "" {
		fwd = strings.TrimSpace(xri)
	}

	connection := types.NewConnection(generateConnectionID(), make(chan []byte, sendQueueLimit))
	wsConn := &websocketConnection{
		Connection:   connection,
		conn:         conn,
		portCtx:      portCtx,
		user:         userHeader(r),
		forwardedFor: fwd,
	}

	if !ws.attachConnection(wsConn) {
		wsConn.closeSocket()
		return
	}

	go func() {
		defer ws.wg.Done()
		ws.handleConnection(wsConn)
	}()
	go func() {
		defer ws.wg.Done()
		ws.handleSend(wsConn)
	}()
	go func() {
		defer ws.wg.Done()
		ws.pingLoop(wsConn)
	}()
}

func (ws *WebSocketServer) handleConnection(wsConn *websocketConnection) {
	defer ws.closeConnection(wsConn)
	defer recoverPanic("handleConnection", wsConn.ID)

	wsConn.conn.SetPongHandler(func(string) error {
		wsConn.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	for {
		wsConn.conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		_, reader, err := wsConn.conn.NextReader()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure, websocket.CloseNormalClosure, websocket.CloseNoStatusReceived) {
				wsLog().Debug("WebSocket read error", "err", err)
			}
			return
		}
		message, err := io.ReadAll(io.LimitReader(reader, MaxRequestBytes+1))
		if err != nil {
			wsLog().Debug("WebSocket read error", "err", err)
			return
		}
		if len(message) > MaxRequestBytes {
			ws.sendJSONInvalid(wsConn, nil, false)
			continue
		}

		select {
		case <-wsConn.Done():
			return
		default:
		}

		ws.handleMessage(wsConn, message)
	}
}

func (ws *WebSocketServer) pingLoop(wsConn *websocketConnection) {
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
		case <-wsConn.Done():
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

func (ws *WebSocketServer) handleSend(wsConn *websocketConnection) {
	defer recoverPanic("handleSend", wsConn.ID)
	for {
		select {
		case <-wsConn.Done():
			return
		case message := <-wsConn.Outbound():
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

func (ws *WebSocketServer) handleMessage(wsConn *websocketConnection, message []byte) {
	// Per-message recover so one bad command can't tear down the read
	// loop and drop the connection's pending subscriptions.
	defer func() {
		if rec := recover(); rec != nil {
			wsLog().Error("ws message panic", "conn", wsConn.ID, "err", rec, "stack", string(debug.Stack()))
			ws.sendErrorResponse(wsConn, rpcInternalError(), nil, nil, buildWSRequestEcho(message))
		}
	}()

	var requestValue any
	if err := decodeJSONUseNumber(message, &requestValue); err != nil {
		ws.sendJSONInvalid(wsConn, nil, false)
		return
	}
	cmdMap, ok := requestValue.(map[string]any)
	if !ok || cmdMap == nil {
		ws.sendJSONInvalid(wsConn, requestValue, true)
		return
	}

	requestEcho := redactedRequestMap(cmdMap)
	var id any
	if idVal, exists := requestEcho["id"]; exists {
		id = idVal
		cmdMap["id"] = idVal
	}

	// Role is always derived from the socket-level peer, never from
	// header-supplied IPs. ClientIP is the peer too, unless the peer is in this
	// port's secure_gateway set — then we substitute the value captured at
	// upgrade time (matches rippled WSInfoSub::forwarded_for,
	// ServerHandler.cpp:497-501). Derived before command resolution so a
	// malformed request can be load-charged like rippled charges it.
	peerIP := getWebSocketClientIP(wsConn.conn)
	clientIP := resolveWSClientIP(peerIP, wsConn.forwardedFor, wsConn.portCtx)
	role := roleForRequest(peerIP, wsConn.user, cmdMap, wsConn.portCtx)
	loadCtx := newRpcContext(wsConn.Context(), role, types.DefaultApiVersion, clientIP, ws.loadPeerSource(), ws.services)
	if rpcErr := gateLoad(ws.loadTracker, loadCtx, "", wsLog()); rpcErr != nil {
		wsConn.closeWithPolicyViolation("threshold exceeded")
		return
	}

	apiVersion := types.DefaultApiVersion
	if version, present := apiVersionFromObject(message); present {
		apiVersion = version
	}
	versionCtx := newRpcContext(wsConn.Context(), role, apiVersion, clientIP, ws.loadPeerSource(), ws.services)
	if rpcErr := validateApiVersion(versionCtx); rpcErr != nil {
		chargeLoad(ws.loadTracker, versionCtx, "", loadtrack.LoadMalformed, wsLog())
		ws.sendErrorResponse(wsConn, rpcErr, id, nil, requestEcho)
		return
	}

	// rippled accepts `method` as an alias for `command`, rejecting only when
	// neither is present (or both are present strings that disagree) with a
	// bare missingCommand token that echoes the original request, and charges
	// feeMalformedRPC (ServerHandler.cpp:446-468).
	command, ok := resolveWSCommand(cmdMap)
	if !ok {
		chargeLoad(ws.loadTracker, versionCtx, "", loadtrack.LoadMalformed, wsLog())
		ws.sendMissingCommand(wsConn, cmdMap, id)
		return
	}

	cmd := types.WebSocketCommand{
		Command: command,
		ID:      id,
		Request: requestEcho,
	}

	delete(cmdMap, "command")
	delete(cmdMap, "method")
	delete(cmdMap, "api_version")

	if len(cmdMap) > 0 {
		paramsBytes, _ := json.Marshal(cmdMap)
		cmd.Params = paramsBytes
	}

	wsLog().Debug("ws request", "cmd", cmd.Command, "remoteAddr", wsConn.conn.RemoteAddr().String(), "clientIP", clientIP, "role", role, "isAdmin", role == types.RoleAdmin)
	dispatchCtx := wsConn.Context()
	var cancel context.CancelFunc
	if ws.timeout > 0 {
		dispatchCtx, cancel = context.WithTimeout(wsConn.Context(), ws.timeout)
		defer cancel()
	}
	rpcCtx := newRpcContext(dispatchCtx, role, apiVersion, clientIP, ws.loadPeerSource(), ws.services)

	switch cmd.Command {
	case "subscribe":
		ws.handleSpecialCommand(wsConn, rpcCtx, cmd, ws.executeSubscribe)
		return
	case "unsubscribe":
		ws.handleSpecialCommand(wsConn, rpcCtx, cmd, ws.executeUnsubscribe)
		return
	case "path_find":
		ws.handleSpecialCommand(wsConn, rpcCtx, cmd, ws.executePathFind)
		return
	}

	ws.handleRPCMethod(wsConn, rpcCtx, cmd)
}

type wsSpecialHandler func(*websocketConnection, *types.RpcContext, types.WebSocketCommand) (any, *types.RpcError)

func (ws *WebSocketServer) handleSpecialCommand(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand, handler wsSpecialHandler) {
	ctx.LoadCost = uint32(loadtrack.LoadReference)
	if rpcErr := handlers.RequireNotBusyClient(ctx); rpcErr != nil {
		finalizeLoad(ws.loadTracker, ctx, cmd.Command, loadtrack.LoadReference, wsLog())
		ws.sendCommandError(wsConn, rpcErr, cmd)
		return
	}

	resolution := resolveMethod(ws.methodRegistry, cmd.Command, ctx.ApiVersion)
	if !resolution.resolved {
		finalizeLoad(ws.loadTracker, ctx, cmd.Command, loadtrack.LoadReference, wsLog())
		ws.sendCommandError(wsConn, types.RpcErrorMethodNotFound(), cmd)
		return
	}
	if rpcErr := conditionMet(resolution.handler.RequiredCondition(), ctx); rpcErr != nil {
		finalizeLoad(ws.loadTracker, ctx, cmd.Command, loadtrack.LoadReference, wsLog())
		ws.sendCommandError(wsConn, rpcErr, cmd)
		return
	}

	result, rpcErr, recovered := func() (any, *types.RpcError, bool) {
		if ws.services != nil && ws.services.ClientLoad != nil {
			ws.services.ClientLoad.Begin()
			defer ws.services.ClientLoad.End()
		}
		return invokeWSSpecial(handler, wsConn, ctx, cmd)
	}()
	kind := loadtrack.LoadKind(ctx.LoadCost)
	if recovered && kind == loadtrack.LoadReference {
		kind = loadtrack.LoadException
	}
	finalizeLoad(ws.loadTracker, ctx, cmd.Command, kind, wsLog())
	if rpcErr != nil {
		// Error responses deliberately omit warnings produced by the final charge.
		ws.sendCommandError(wsConn, rpcErr, cmd)
		return
	}
	ws.sendCommandResponse(wsConn, result, cmd, wsLoadWarningOpts(ctx))
}

func invokeWSSpecial(handler wsSpecialHandler, wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (result any, rpcErr *types.RpcError, recovered bool) {
	defer func() {
		if rec := recover(); rec != nil {
			wsLog().Error("rpc handler panic", "err", rec, "stack", string(debug.Stack()), "method", cmd.Command, "client", ctx.ClientIP)
			result = nil
			rpcErr = rpcInternalError()
			recovered = true
		}
	}()
	result, rpcErr = handler(wsConn, ctx, cmd)
	return result, rpcErr, false
}

func (ws *WebSocketServer) executeSubscribe(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (any, *types.RpcError) {
	var request types.SubscriptionRequest
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
	}
	// url requests are server-to-server (RPCSub) subscriptions: events go
	// to the url's subscriber, not to this WebSocket connection.
	if request.HasURL() {
		if !ctx.IsAdmin {
			return nil, types.RpcErrorNoPermission("subscribe")
		}
		result, rpcErr := ws.urlSubs.Subscribe(ctx, request)
		if rpcErr != nil {
			return nil, rpcErr
		}
		setSubscriptionLoadCost(ctx, request)
		return result, nil
	}

	// The embedded canonical connection is the same object the subscription
	// manager already tracks and carries the bounded queue and disconnect
	// callback.
	prefix, err := subscriptionRequestExcluding(cmd.Params, "books")
	if err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid subscription parameters.")
	}
	prefix.ApiVersion = ctx.ApiVersion
	if rpcErr := ws.subscriptionManager.HandleSubscribe(wsConn.Connection, prefix, ctx.IsAdmin); rpcErr != nil {
		return nil, rpcErr
	}
	if rpcErr := applySubscriptionBooks(request.WireArrays().Books, func(bookRequest types.SubscriptionRequest) *types.RpcError {
		bookRequest.ApiVersion = ctx.ApiVersion
		if rpcErr := ws.subscriptionManager.HandleSubscribe(wsConn.Connection, bookRequest, ctx.IsAdmin); rpcErr != nil {
			return rpcErr
		}
		setSubscriptionLoadCost(ctx, bookRequest)
		return nil
	}); rpcErr != nil {
		return nil, rpcErr
	}

	result := ws.buildSubscribeAck(ctx, request)
	return result, nil
}

func subscriptionRequestExcluding(params json.RawMessage, fields ...string) (types.SubscriptionRequest, error) {
	var request types.SubscriptionRequest
	if len(params) == 0 {
		return request, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil {
		return request, err
	}
	for _, field := range fields {
		delete(raw, field)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return request, err
	}
	err = json.Unmarshal(data, &request)
	return request, err
}

func applySubscriptionBooks(raw json.RawMessage, apply func(types.SubscriptionRequest) *types.RpcError) *types.RpcError {
	if raw == nil {
		return nil
	}
	if rawJSONNull(raw) {
		request, err := subscriptionRequestForBooks(raw)
		if err != nil {
			return types.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
		return apply(request)
	}
	var books []json.RawMessage
	if err := json.Unmarshal(raw, &books); err != nil {
		request, decodeErr := subscriptionRequestForBooks(raw)
		if decodeErr != nil {
			return types.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
		return apply(request)
	}
	for _, book := range books {
		request, err := subscriptionRequestForBooks(json.RawMessage("[" + string(book) + "]"))
		if err != nil {
			return types.RpcErrorInvalidParams("Invalid subscription parameters.")
		}
		if rpcErr := apply(request); rpcErr != nil {
			return rpcErr
		}
	}
	return nil
}

func subscriptionRequestForBooks(books json.RawMessage) (types.SubscriptionRequest, error) {
	data, err := json.Marshal(map[string]json.RawMessage{"books": books})
	if err != nil {
		return types.SubscriptionRequest{}, err
	}
	var request types.SubscriptionRequest
	err = json.Unmarshal(data, &request)
	return request, err
}

func (ws *WebSocketServer) finishUnsubscribe(wsConn *websocketConnection, request types.SubscriptionRequest, params json.RawMessage, isAdmin bool) *types.RpcError {
	prefix, err := subscriptionRequestExcluding(params, "books")
	if err != nil {
		return types.RpcErrorInvalidParams("Invalid unsubscription parameters.")
	}
	if rpcErr := ws.subscriptionManager.HandleUnsubscribe(wsConn.Connection, prefix, isAdmin); rpcErr != nil {
		return rpcErr
	}
	return applySubscriptionBooks(request.WireArrays().Books, func(bookRequest types.SubscriptionRequest) *types.RpcError {
		return ws.subscriptionManager.HandleUnsubscribe(wsConn.Connection, bookRequest, isAdmin)
	})
}

func setSubscriptionLoadCost(ctx *types.RpcContext, request types.SubscriptionRequest) {
	for _, book := range request.Books {
		if book.Snapshot || book.StateNow {
			ctx.LoadCost = uint32(loadtrack.LoadMedium)
			return
		}
	}
}

func (ws *WebSocketServer) executeUnsubscribe(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (any, *types.RpcError) {
	var request types.SubscriptionRequest
	if len(cmd.Params) > 0 {
		if err := json.Unmarshal(cmd.Params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid unsubscription parameters.")
		}
	}
	// See handleSubscribe: url requests target the RPCSub registry.
	if request.HasURL() {
		if !ctx.IsAdmin {
			return nil, types.RpcErrorNoPermission("unsubscribe")
		}
		result, rpcErr := ws.urlSubs.Unsubscribe(ctx, request)
		if rpcErr != nil {
			return nil, rpcErr
		}
		return result, nil
	}

	if rpcErr := ws.finishUnsubscribe(wsConn, request, cmd.Params, ctx.IsAdmin); rpcErr != nil {
		return nil, rpcErr
	}

	return map[string]any{}, nil
}

func (ws *WebSocketServer) executePathFind(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (any, *types.RpcError) {
	var params map[string]json.RawMessage
	if len(cmd.Params) == 0 || json.Unmarshal(cmd.Params, &params) != nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	rawSubcommand, exists := params["subcommand"]
	if !exists {
		return nil, types.RpcErrorInvalidParams("Invalid field 'subcommand'.")
	}
	var subcommand *string
	if err := json.Unmarshal(rawSubcommand, &subcommand); err != nil || subcommand == nil {
		return nil, types.RpcErrorInvalidParams("Invalid field 'subcommand'.")
	}
	wsConn.SetAPIVersion(ctx.ApiVersion)

	switch *subcommand {
	case "create":
		ctx.LoadCost = uint32(loadtrack.LoadHeavy)
		return ws.executePathFindCreate(wsConn, ctx, cmd)
	case "close":
		return ws.executePathFindClose(wsConn, ctx, cmd)
	case "status":
		return ws.executePathFindStatus(wsConn, ctx, cmd)
	default:
		return nil, types.RpcErrorInvalidParams("Invalid field 'subcommand'.")
	}
}

// executePathFindCreate creates a new persistent pathfinding session.
// Any existing session on this connection is replaced (matching rippled).
func (ws *WebSocketServer) executePathFindCreate(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (any, *types.RpcError) {
	ws.bindPathFindRefreshManager(wsConn)
	wsConn.clearPathFindSession()

	release, rpcErr := handlers.AcquirePathfind(ctx)
	if rpcErr != nil {
		return nil, rpcErr
	}
	defer release()

	session, rpcErr := ParseAndCreateSession(cmd.Params, cmd.ID)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, types.NewRpcError(types.RpcNO_CURRENT, "noCurrent", "noCurrent",
			"No closed ledger available")
	}
	view, err := ctx.Services.Ledger.GetClosedLedgerView()
	if err != nil {
		return nil, types.NewRpcError(types.RpcNO_CURRENT, "noCurrent", "noCurrent",
			"No closed ledger available")
	}

	event := session.Execute(view, false)

	wsConn.installPathFindSession(session)
	ws.queuePathFindSessions(currentPathFindView(ctx.Services.Ledger), wsConn)

	return event, nil
}

// executePathFindClose closes the active pathfinding session on this connection.
func (ws *WebSocketServer) executePathFindClose(wsConn *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *types.RpcError) {
	session := wsConn.clearPathFindSession()

	if session == nil {
		return nil, types.RpcErrorNoPathRequest()
	}

	return session.Close(), nil
}

// executePathFindStatus returns the current status of the active pathfinding session.
func (ws *WebSocketServer) executePathFindStatus(wsConn *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *types.RpcError) {
	wsConn.mutex.RLock()
	session := wsConn.pathFindSession
	wsConn.mutex.RUnlock()

	if session == nil {
		return nil, types.RpcErrorNoPathRequest()
	}

	return session.Status(), nil
}

// UpdatePathFindSessions snapshots active sessions and queues one bounded,
// asynchronous refresh generation. The ledger-close callback must not perform
// ledger-view acquisition or path computation itself.
func (ws *WebSocketServer) UpdatePathFindSessions(getView func() (types.LedgerStateView, error)) {
	ws.queuePathFindSessions(getView, nil)
}

func (ws *WebSocketServer) queuePathFindSessions(getView func() (types.LedgerStateView, error), additional *websocketConnection) {
	manager := ws.ensurePathFindRefreshManager()
	ws.connectionsMutex.RLock()
	var targets []pathFindUpdateTarget
	seen := make(map[*websocketConnection]struct{}, len(ws.connections)+1)
	for _, conn := range ws.connections {
		seen[conn] = struct{}{}
		ws.bindPathFindRefreshManager(conn)
		if target, ok := conn.snapshotPathFindUpdate(); ok {
			targets = append(targets, target)
		}
	}
	ws.connectionsMutex.RUnlock()
	if _, exists := seen[additional]; additional != nil && !exists {
		ws.bindPathFindRefreshManager(additional)
		if target, ok := additional.snapshotPathFindUpdate(); ok {
			targets = append(targets, target)
		}
	}

	manager.enqueue(getView, targets)
}

func currentPathFindView(ledger types.LedgerService) func() (types.LedgerStateView, error) {
	return func() (types.LedgerStateView, error) {
		if source, ok := ledger.(types.LedgerViewSource); ok {
			view, _, err := source.GetLedgerViewBySeq(ledger.GetCurrentLedgerIndex())
			return view, err
		}
		return ledger.GetClosedLedgerView()
	}
}

type pathFindUpdateTarget struct {
	connection *websocketConnection
	session    *PathFindSession
	generation uint64
}

func (c *websocketConnection) clearPathFindSession() *PathFindSession {
	c.mutex.Lock()
	session := c.pathFindSession
	generation := c.pathFindGeneration
	manager := c.pathFindRefresh
	c.pathFindSession = nil
	c.pathFindGeneration++
	c.mutex.Unlock()
	if manager != nil && session != nil {
		manager.cancel(c, session, generation)
	}
	return session
}

func (c *websocketConnection) installPathFindSession(session *PathFindSession) {
	c.mutex.Lock()
	previous := c.pathFindSession
	previousGeneration := c.pathFindGeneration
	manager := c.pathFindRefresh
	c.pathFindSession = session
	c.pathFindGeneration++
	c.mutex.Unlock()
	if manager != nil && previous != nil {
		manager.cancel(c, previous, previousGeneration)
	}
}

func (ws *WebSocketServer) ensurePathFindRefreshManager() *pathFindRefreshManager {
	ws.pathFindRefreshMu.Lock()
	defer ws.pathFindRefreshMu.Unlock()
	if ws.pathFindRefresh == nil {
		ws.pathFindRefresh = newPathFindRefreshManager(ws)
	}
	return ws.pathFindRefresh
}

func (ws *WebSocketServer) bindPathFindRefreshManager(conn *websocketConnection) {
	manager := ws.ensurePathFindRefreshManager()
	conn.mutex.Lock()
	if conn.pathFindRefresh == nil {
		conn.pathFindRefresh = manager
	}
	conn.mutex.Unlock()
}

func (c *websocketConnection) snapshotPathFindUpdate() (pathFindUpdateTarget, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	if c.pathFindSession == nil {
		return pathFindUpdateTarget{}, false
	}
	return pathFindUpdateTarget{
		connection: c,
		session:    c.pathFindSession,
		generation: c.pathFindGeneration,
	}, true
}

func (target pathFindUpdateTarget) current() bool {
	target.connection.mutex.RLock()
	defer target.connection.mutex.RUnlock()
	return target.connection.pathFindSession == target.session &&
		target.connection.pathFindGeneration == target.generation
}

func (target pathFindUpdateTarget) trySend(data []byte) bool {
	target.connection.mutex.RLock()
	defer target.connection.mutex.RUnlock()

	if target.connection.pathFindSession != target.session ||
		target.connection.pathFindGeneration != target.generation {
		return false
	}
	return target.connection.TrySend(data)
}

func (ws *WebSocketServer) handleRPCMethod(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	// Shared dispatch core (registry → admin gate → conditionMet →
	// api-version → busy/load gates → handle → finalize), identical to the
	// HTTP path. The WS-specific admin gate returns rpcFORBIDDEN instead of
	// rpcNO_PERMISSION (ServerHandler.cpp:482-486): when requestRole returns
	// Role::FORBID for an admin-required command, rippled writes
	// rpcError(rpcFORBIDDEN) before doCommand ever runs.
	resolution := resolveMethod(ws.methodRegistry, cmd.Command, ctx.ApiVersion)
	if rpcErr := admitMethod(ws.loadTracker, ctx, cmd.Command, resolution, types.RpcErrorForbidden, false, wsLog()); rpcErr != nil {
		warnLoad(ws.loadTracker, ctx, cmd.Command, wsLog())
		ws.sendErrorResponse(wsConn, rpcErr, cmd.ID, nil, cmd.Request)
		return
	}
	result, rpcErr := dispatchResolvedMethod(ws.loadTracker, ws.services, ctx, cmd.Command, cmd.Params, resolution, wsLog())
	if rpcErr != nil {
		ws.sendErrorResponse(wsConn, rpcErr, cmd.ID, nil, cmd.Request)
		return
	}
	ws.sendCommandResponse(wsConn, result, cmd, wsLoadWarningOpts(ctx))
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

func (ws *WebSocketServer) sendCommandResponse(wsConn *websocketConnection, result any, cmd types.WebSocketCommand, opts *types.WebSocketResponseOptions) {
	payload := map[string]any{
		"type":   "response",
		"status": "success",
	}
	if result != nil {
		payload["result"] = result
	}
	copyWSMetadata(payload, cmd.Request, cmd.ID)
	if opts != nil {
		if opts.Warning != "" {
			payload["warning"] = opts.Warning
		}
		if len(opts.Warnings) > 0 {
			payload["warnings"] = opts.Warnings
		}
		if opts.Forwarded {
			payload["forwarded"] = true
		}
	}

	data, err := marshalWebSocketJSON(payload)
	if err != nil {
		wsLog().Error("Failed to marshal WebSocket response", "err", err)
		return
	}
	ws.deliver(wsConn, data)
}

// deliver queues an already-marshalled WS frame through the canonical
// connection so per-request response delivery and broadcast delivery use the
// same drop policy.
func (ws *WebSocketServer) deliver(wsConn *websocketConnection, data []byte) {
	if !wsConn.TrySend(data) {
		wsLog().Debug("WebSocket send dropped (slow consumer)", "connID", wsConn.ID)
	}
}

// resolveWSCommand resolves the WS command name from the incoming JSON,
// accepting `method` as an alias for `command` (ServerHandler.cpp:446-475).
// ok is false when either supplied field is not a string, neither is supplied,
// or both strings disagree. Empty strings are valid at this layer and resolve
// to unknownCmd during dispatch.
func resolveWSCommand(m map[string]any) (string, bool) {
	cmdValue, cmdPresent := m["command"]
	methodValue, methodPresent := m["method"]
	cmd, cmdOK := cmdValue.(string)
	method, methodOK := methodValue.(string)
	if (cmdPresent && !cmdOK) || (methodPresent && !methodOK) {
		return "", false
	}
	switch {
	case cmdPresent && methodPresent:
		if cmd != method {
			return "", false
		}
		return cmd, true
	case cmdPresent:
		return cmd, true
	case methodPresent:
		return method, true
	default:
		return "", false
	}
}

// sendMissingCommand emits rippled's bare missingCommand reply: a lone
// `error` token (no error_code/error_message) plus the echoed request and id
// (ServerHandler.cpp:452-468). Credentials in the echo are redacted — a
// deliberate goxrpl superset of rippled's raw echo.
func (ws *WebSocketServer) sendMissingCommand(wsConn *websocketConnection, request map[string]any, id any) {
	echo := redactedRequestMap(request)
	resp := map[string]any{
		"type":    "response",
		"status":  "error",
		"error":   "missingCommand",
		"request": echo,
	}
	copyWSMetadata(resp, echo, id)
	data, err := marshalWebSocketJSON(resp)
	if err != nil {
		wsLog().Error("Failed to marshal malformed WebSocket response", "err", err)
		return
	}
	ws.deliver(wsConn, data)
}

func (ws *WebSocketServer) sendJSONInvalid(wsConn *websocketConnection, value any, parsed bool) {
	rawValue := "<redacted>"
	if parsed {
		if data, err := marshalWebSocketJSON(redactJSONValue(value)); err == nil {
			rawValue = string(data)
		}
	}
	data, err := marshalWebSocketJSON(map[string]any{
		"type":  "error",
		"error": "jsonInvalid",
		"value": rawValue,
	})
	if err != nil {
		wsLog().Error("Failed to marshal invalid JSON response", "err", err)
		return
	}
	ws.deliver(wsConn, data)
}

func (ws *WebSocketServer) sendCommandError(wsConn *websocketConnection, rpcErr *types.RpcError, cmd types.WebSocketCommand) {
	ws.sendErrorResponse(wsConn, rpcErr, cmd.ID, nil, cmd.Request)
}

func (ws *WebSocketServer) sendErrorResponse(wsConn *websocketConnection, rpcErr *types.RpcError, id any, opts *types.WebSocketResponseOptions, request map[string]any) {
	response := map[string]any{
		"type":   "response",
		"status": "error",
		"error":  rpcErr.ErrorString,
	}
	if id != nil {
		response["id"] = id
	}
	if rpcErr.ErrorException != "" {
		response["error_exception"] = rpcErr.ErrorException
	} else if !rpcErr.IsBareToken() {
		response["error_code"] = rpcErr.Code
		response["error_message"] = rpcErr.Message
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

	if request != nil {
		response["request"] = request
	}
	copyWSMetadata(response, request, id)

	data, err := marshalWebSocketJSON(response)
	if err != nil {
		wsLog().Error("Failed to marshal WebSocket error response", "err", err)
		return
	}
	ws.deliver(wsConn, data)
}

func buildWSRequestEcho(message []byte) map[string]any {
	var request map[string]any
	if err := decodeJSONUseNumber(message, &request); err != nil || request == nil {
		return nil
	}
	return redactedRequestMap(request)
}

func copyWSMetadata(response map[string]any, request map[string]any, fallbackID any) {
	if fallbackID != nil {
		response["id"] = fallbackID
	}
	for _, key := range []string{"id", "jsonrpc", "ripplerpc", "api_version"} {
		if value, ok := request[key]; ok {
			response[key] = value
		}
	}
}

func marshalWebSocketJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

// attachConnection is the single point at which a new WS connection
// becomes visible to both the per-server connection map and the
// subscription manager. Pairing this with detachConnection makes it
// impossible for the two maps to drift on Add/Remove ordering — the
// "duplicated connection state" concern flagged in the #428 audit.
func (ws *WebSocketServer) attachConnection(wsConn *websocketConnection) bool {
	ws.bindPathFindRefreshManager(wsConn)
	wsConn.Disconnect = wsConn.closeSocket

	ws.connectionsMutex.Lock()
	if ws.closing {
		ws.connectionsMutex.Unlock()
		return false
	}
	ws.wg.Add(3)
	ws.connections[wsConn.ID] = wsConn
	ws.connectionsMutex.Unlock()
	ws.subscriptionManager.AddConnection(wsConn.Connection)
	return true
}

// detachConnection is the inverse of attachConnection.
func (ws *WebSocketServer) detachConnection(wsConn *websocketConnection) {
	ws.connectionsMutex.Lock()
	delete(ws.connections, wsConn.ID)
	ws.connectionsMutex.Unlock()
	ws.subscriptionManager.RemoveConnection(wsConn.ID)
}

// closeSocket cancels the connection context and closes the underlying
// socket. Closing the socket releases its listener-owned connection slot and
// unblocks a read loop parked in ReadMessage without waiting out the 90 s read
// deadline. Used by the slow-consumer
// Disconnect callback and the send-error path; idempotent — closeConnection
// closes again and gorilla tolerates the double close.
func (c *websocketConnection) closeSocket() {
	c.Cancel()
	c.closeOnce.Do(func() {
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

func (c *websocketConnection) closeWithPolicyViolation(reason string) {
	c.Cancel()
	_ = c.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason),
		time.Now().Add(time.Second),
	)
	c.closeSocket()
}

func (ws *WebSocketServer) closeConnection(wsConn *websocketConnection) {
	wsConn.Cancel()

	wsConn.clearPathFindSession()

	ws.detachConnection(wsConn)

	wsConn.closeSocket()

	wsLog().Debug("WebSocket connection closed", "connID", wsConn.ID)
}

// buildSubscribeAck assembles the subscribe response payload shared by the
// WebSocket and url (RPCSub) subscribe paths: current ledger info when the
// ledger stream is among the requested streams, and a synthetic book-offers
// snapshot for any `snapshot:true` book.
//
// The ledger ack field set mirrors rippled subLedger: fee_ref only while
// XRPFees is disabled, network_id is present when a validated ledger exists; per-ledger pubLedger
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
				if info.LedgerAvailable {
					result["ledger_index"] = info.LedgerIndex
					result["ledger_hash"] = info.LedgerHash
					result["ledger_time"] = info.LedgerTime
					result["fee_base"] = info.FeeBase
					// rippled emits the deprecated fee_ref only while XRPFees
					// is disabled; network_id is present with ledger fields.
					if !info.XRPFeesEnabled {
						result["fee_ref"] = info.FeeRef
					}
					result["reserve_base"] = info.ReserveBase
					result["reserve_inc"] = info.ReserveInc
					result["network_id"] = info.NetworkID
				}
				if info.ValidatedLedgersPresent {
					result["validated_ledgers"] = info.ValidatedLedgers
				}
			}
		}
	}

	for _, book := range request.Books {
		if (!book.Snapshot && !book.StateNow) || ctx.Services == nil || ctx.Services.Ledger == nil {
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
		if book.Both || book.BothSides {
			bids, _ := ws.snapshotBook(ctx, gets, pays, book.Taker, book.Domain)
			asks, _ := ws.snapshotBook(ctx, pays, gets, book.Taker, book.Domain)
			if bids != nil {
				result["bids"] = appendOffers(result["bids"], bids)
			}
			if asks != nil {
				result["asks"] = appendOffers(result["asks"], asks)
			}
			continue
		}
		offers, _ := ws.snapshotBook(ctx, gets, pays, book.Taker, book.Domain)
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
func (ws *WebSocketServer) snapshotBook(ctx *types.RpcContext, takerGets, takerPays types.Amount, taker, domain string) ([]types.BookOffer, error) {
	if ctx == nil || ctx.Services == nil || ctx.Services.Ledger == nil {
		return nil, nil
	}
	res, err := ctx.Services.Ledger.GetBookOffers(ctx.Context, takerGets, takerPays, taker, domain, "validated", DefaultBookSnapshotLimit, "", false)
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

// SubscriptionManager returns the subscription manager for event publishing
func (ws *WebSocketServer) SubscriptionManager() *subscription.Manager {
	return ws.subscriptionManager
}

// Close gracefully closes all active WebSocket connections and url (RPCSub)
// subscriptions, waiting for admitted HTTP handlers, per-connection
// goroutines, and url delivery loops to exit. The wait is bounded by ctx so a
// misbehaving handler cannot stall shutdown indefinitely; if ctx expires first,
// Close returns ctx.Err().
func (ws *WebSocketServer) Close(ctx context.Context) error {
	ws.closeOnce.Do(func() {
		go ws.shutdown(ctx)
	})
	select {
	case <-ws.closeDone:
		return nil
	case <-ctx.Done():
		<-ws.forceDone
		return ctx.Err()
	}
}

func (ws *WebSocketServer) shutdown(ctx context.Context) {
	defer close(ws.closeDone)

	ws.connectionsMutex.Lock()
	ws.closing = true
	connections := make([]*websocketConnection, 0, len(ws.connections))
	for _, conn := range ws.connections {
		connections = append(connections, conn)
	}
	ws.connectionsMutex.Unlock()

	closeDeadline := time.Now().Add(10 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(closeDeadline) {
		closeDeadline = deadline
	}

	var closeFrames sync.WaitGroup
	closeFrames.Add(len(connections))
	for _, conn := range connections {
		conn.Cancel()
		conn.clearPathFindSession()
		go func() {
			defer closeFrames.Done()
			_ = conn.conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutdown"),
				closeDeadline,
			)
		}()
	}

	closeFramesDone := make(chan struct{})
	go func() {
		closeFrames.Wait()
		close(closeFramesDone)
	}()

	select {
	case <-closeFramesDone:
	case <-ctx.Done():
	}

	for _, conn := range connections {
		conn.closeSocket()
	}
	refreshManager := ws.ensurePathFindRefreshManager()
	refreshErr := refreshManager.wait(ctx)
	close(ws.forceDone)
	if refreshErr != nil {
		// The close caller has already been released at its deadline. Keep this
		// shutdown goroutine joining the fixed refresh worker set so eventual
		// release of a non-cooperative pathfinder leaves no worker behind.
		_ = refreshManager.wait(context.Background())
	}

	closeFrames.Wait()
	ws.wg.Wait()
	ws.urlSubs.Close()
}
