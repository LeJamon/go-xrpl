package rpc

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
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
	peerSource          atomic.Pointer[types.PeerSource]
	loadTracker         *loadtrack.Tracker
	// pingInterval is how often pingLoop sends a keepalive ping. Settable
	// so concurrency tests can drive the ping path without waiting on the
	// production cadence.
	pingInterval time.Duration
	// wg tracks per-connection goroutines (read loop, send pump, ping loop)
	// so Close can join them on shutdown.
	wg sync.WaitGroup
}

// SetPeerSource registers the source of per-peer entries served by the
// `peers` RPC handler. Passing nil detaches the source so the handler
// returns an empty list. Safe to call concurrently with reads.
func (ws *WebSocketServer) SetPeerSource(src types.PeerSource) {
	if src == nil {
		ws.peerSource.Store(nil)
		return
	}
	ws.peerSource.Store(&src)
}

func (ws *WebSocketServer) loadPeerSource() types.PeerSource {
	if p := ws.peerSource.Load(); p != nil {
		return *p
	}
	return nil
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
	return &WebSocketServer{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// TODO: Implement proper origin checking for security
				// For now, allow all origins (matching rippled behavior)
				return true
			},
			// Don't require specific subprotocol - xrpl.js doesn't use one
		},
		subscriptionManager: &subscription.Manager{
			Connections: make(map[string]*types.Connection),
		},
		methodRegistry: types.NewMethodRegistry(),
		connections:    make(map[string]*WebSocketConnection),
		timeout:        timeout,
		services:       services,
		loadTracker:    loadtrack.New(),
		pingInterval:   30 * time.Second,
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

	command, ok := cmdMap["command"].(string)
	if !ok || command == "" {
		ws.sendError(wsConn, types.NewRpcError(types.RpcMISSING_COMMAND, "missingCommand", "missingCommand", "Missing command field"), nil)
		return
	}

	var id any
	if idVal, exists := cmdMap["id"]; exists {
		id = idVal
	}

	cmd := types.WebSocketCommand{
		Command: command,
		ID:      id,
	}

	delete(cmdMap, "command")
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
	rpcCtx := &types.RpcContext{
		Context:    dispatchCtx,
		Role:       role,
		ApiVersion: apiVersion,
		IsAdmin:    role == types.RoleAdmin,
		Unlimited:  role.IsUnlimited(),
		ClientIP:   clientIP,
		PeerSource: ws.loadPeerSource(),
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

	conn := &types.Connection{
		ID:            wsConn.ID,
		Subscriptions: wsConn.subscriptions,
		SendChannel:   wsConn.sendChannel,
		CloseChannel:  wsConn.closeChannel,
	}
	if err := ws.subscriptionManager.HandleSubscribe(conn, request, ctx.IsAdmin); err != nil {
		ws.sendError(wsConn, err, cmd.ID)
		return
	}

	// rippled returns current ledger info in the subscribe response when
	// the ledger stream is among the requested streams.
	result := make(map[string]any)

	if slices.Contains(request.Streams, types.SubLedger) {
		if ws.ledgerInfoProvider != nil {
			info := ws.ledgerInfoProvider.GetCurrentLedgerInfo()
			if info != nil {
				// Subscribe ack field set mirrors rippled subLedger
				// at NetworkOPs.cpp:4174-4189. Per-ledger pubLedger
				// events (LedgerCloseEvent) carry txn_count separately.
				result["ledger_index"] = info.LedgerIndex
				result["ledger_hash"] = info.LedgerHash
				result["ledger_time"] = info.LedgerTime
				result["fee_base"] = info.FeeBase
				result["fee_ref"] = info.FeeRef
				result["reserve_base"] = info.ReserveBase
				result["reserve_inc"] = info.ReserveInc
				if info.NetworkID > 0 {
					result["network_id"] = info.NetworkID
				}
				if info.ValidatedLedgers != "" {
					result["validated_ledgers"] = info.ValidatedLedgers
				}
			}
		}
	}

	// Synthetic book-offers snapshot for any `snapshot:true` book in the
	// request. Mirrors rippled Subscribe.cpp:339-394: when snapshot is
	// set, the response carries `offers` (or `bids`/`asks` if `both` is
	// set) populated by NetworkOPs::getBookPage. Reuses the ledger
	// service's GetBookOffers — the same code path the book_offers RPC
	// uses — so the snapshot a subscriber gets in the ack is identical
	// to what they would have read with a separate book_offers call.
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

	conn := &types.Connection{
		ID:            wsConn.ID,
		Subscriptions: wsConn.subscriptions,
		SendChannel:   wsConn.sendChannel,
		CloseChannel:  wsConn.closeChannel,
	}
	if err := ws.subscriptionManager.HandleUnsubscribe(conn, request, ctx.IsAdmin); err != nil {
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

		select {
		case conn.sendChannel <- data:
		default:
			// Channel full, skip this update
		}
	}
}

func (ws *WebSocketServer) handleRPCMethod(wsConn *WebSocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) {
	handler, exists := ws.methodRegistry.Get(cmd.Command)
	if !exists {
		ws.sendError(wsConn, types.RpcErrorMethodNotFound(cmd.Command), cmd.ID)
		return
	}

	// Admin gate. Mirrors rippled ServerHandler.cpp:482-486 (WS path):
	// when requestRole returns Role::FORBID for an admin-required command,
	// rippled writes rpcError(rpcFORBIDDEN) before doCommand ever runs.
	// The fallback rpcNO_PERMISSION at RPCHandler.cpp:166-167 is only
	// reached when the outer requestRole gate let the request through.
	if handler.RequiredRole() == types.RoleAdmin && ctx.Role != types.RoleAdmin {
		ws.sendError(wsConn, types.RpcErrorForbidden(cmd.Command), cmd.ID)
		return
	}

	if rpcErr := handlers.RequireNotBusyClient(ctx); rpcErr != nil {
		ws.sendError(wsConn, rpcErr, cmd.ID)
		return
	}

	if rpcErr := gateLoad(ws.loadTracker, ctx, cmd.Command, wsLog()); rpcErr != nil {
		ws.sendError(wsConn, rpcErr, cmd.ID)
		return
	}

	if ws.services != nil && ws.services.ClientLoad != nil {
		ws.services.ClientLoad.Begin()
		defer ws.services.ClientLoad.End()
	}

	result, rpcErr := handler.Handle(ctx, cmd.Params)
	finalizeLoad(ws.loadTracker, ctx, cmd.Command, handler, rpcErr, wsLog())
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

	// Route through the shared TrySend so per-request response delivery
	// and broadcast delivery use the same consecutive-drop counter and
	// the same disconnect-on-N-drops threshold.
	if wsConn.legacy != nil {
		if !wsConn.legacy.TrySend(data) {
			wsLog().Debug("WebSocket send dropped (slow consumer)", "connID", wsConn.ID)
		}
		return
	}
	// Test fixtures may build a wsConn without a legacy peer. Fall back
	// to a non-blocking send so unit tests stay self-contained.
	select {
	case wsConn.sendChannel <- data:
	case <-wsConn.ctx.Done():
	default:
		wsLog().Warn("WebSocket send channel full", "connID", wsConn.ID)
	}
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

	if wsConn.legacy != nil {
		wsConn.legacy.TrySend(data)
		return
	}
	select {
	case wsConn.sendChannel <- data:
	case <-wsConn.ctx.Done():
	default:
		wsLog().Warn("WebSocket send channel full", "connID", wsConn.ID)
	}
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
		// Subscription-manager-driven disconnect routes back through
		// the WS cancel func so a persistently slow subscriber is torn
		// down via the same code path as a normal close.
		Disconnect: wsConn.cancel,
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
// endpoint: the universal HTTP/WS set plus the WebSocket-only commands
// (subscribe / unsubscribe). The HTTP server intentionally omits the
// WebSocket-only set so clients hitting those over HTTP get
// methodNotFound rather than "method exists, returns notSupported"
// (#428 audit, P2).
func (ws *WebSocketServer) RegisterAllMethods() {
	handlers.RegisterAll(ws.methodRegistry)
	handlers.RegisterWebSocketOnly(ws.methodRegistry)
}

// GetSubscriptionManager returns the subscription manager for event publishing
func (ws *WebSocketServer) GetSubscriptionManager() *subscription.Manager {
	return ws.subscriptionManager
}

// Close gracefully closes all active WebSocket connections and waits for
// all per-connection goroutines (read loop, send pump, ping loop) to exit.
// The wait is bounded by ctx so a misbehaving handler cannot stall shutdown
// indefinitely; if ctx expires first, Close returns ctx.Err().
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
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
