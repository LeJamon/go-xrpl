package rpc

import (
	"fmt"
	"net/http"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
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

// defaultSendQueueLimit is the default WebSocket send channel buffer size,
// matching rippled's default ws_queue_limit of 100 (Port.cpp).
const defaultSendQueueLimit = 100

const maxSendQueueLimit = 1<<16 - 1

func resolveSendQueueLimit(portCtx *PortContext) (int, error) {
	if portCtx == nil || portCtx.SendQueue == 0 {
		return defaultSendQueueLimit, nil
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
	services            *types.ServiceGraph
	urlSubscriptions    types.URLSubscriptionService
	urlSubs             *urlSubscriptionRegistry
	peerSourceHolder
	resourceManager   *resource.Manager
	pathFindRefreshMu sync.Mutex
	pathFindRefresh   *pathFindRefreshManager
	// pingInterval is how often pingLoop sends a keepalive ping, selected at
	// construction so tests and node composition can choose their cadence.
	pingInterval time.Duration
	// wg tracks admitted HTTP handlers and per-connection goroutines so Close
	// can join them on shutdown.
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeDone chan struct{}
	forceDone chan struct{}
}

var _ types.MethodDispatcher = (*WebSocketServer)(nil)

// WebSocketServerOptions controls construction of a WebSocket RPC server.
// A nil subscription manager selects a new manager for the standalone server.
type WebSocketServerOptions struct {
	Timeout             time.Duration
	Services            *types.ServiceGraph
	ResourceManager     *resource.Manager
	PeerSource          types.PeerSource
	Registry            *types.MethodRegistry
	PingInterval        time.Duration
	LedgerInfoProvider  types.LedgerInfoProvider
	SubscriptionManager *subscription.Manager
	// URLSubscriptions shares URL subscribers with other RPC transports. A
	// registry returned by NewURLSubscriptionService transfers ownership to
	// the server and is closed with it; other implementations remain caller-owned.
	URLSubscriptions types.URLSubscriptionService
}

// websocketConnection owns the transport-specific state for one logical
// client. Subscription and lifetime state lives in the embedded canonical
// connection shared with the subscription manager.
type websocketConnection struct {
	*subscription.Connection
	registration       *subscription.Registration
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
	forwardedFor     string
	resourceConsumer *resource.Consumer
	closeOnce        sync.Once
}

// NewWebSocketServer creates a new WebSocket RPC server. All method handlers
// are registered before the constructor returns.
func NewWebSocketServer(options WebSocketServerOptions) *WebSocketServer {
	resourceManager := options.ResourceManager
	if resourceManager == nil {
		resourceManager = resource.NewManager(nil, nil)
	}
	if isNilURLSubscriptionService(options.URLSubscriptions) {
		options.URLSubscriptions = nil
	}
	manager := options.SubscriptionManager
	if registry, ok := options.URLSubscriptions.(*urlSubscriptionRegistry); ok && registry.manager != nil {
		manager = registry.manager
	}
	if manager == nil {
		manager = subscription.NewManager()
	}
	pingInterval := options.PingInterval
	if pingInterval <= 0 {
		pingInterval = 30 * time.Second
	}
	ws := &WebSocketServer{
		upgrader: websocket.Upgrader{
			// The shared transport middleware validates Origin before this
			// upgrader runs; direct callers are checked by ServeHTTP.
			CheckOrigin: func(r *http.Request) bool { return true },
			// Don't require specific subprotocol - xrpl.js doesn't use one
		},
		subscriptionManager: manager,
		methodRegistry:      options.Registry,
		connections:         make(map[string]*websocketConnection),
		timeout:             options.Timeout,
		services:            options.Services,
		resourceManager:     resourceManager,
		pingInterval:        pingInterval,
		ledgerInfoProvider:  options.LedgerInfoProvider,
		closeDone:           make(chan struct{}),
		forceDone:           make(chan struct{}),
	}
	if ws.methodRegistry == nil {
		ws.methodRegistry = defaultMethodRegistry()
	}
	ws.pathFindRefresh = newPathFindRefreshManager(ws)
	if registry, ok := options.URLSubscriptions.(*urlSubscriptionRegistry); ok {
		ws.urlSubs = registry
		ws.urlSubscriptions = registry
	} else if options.URLSubscriptions != nil {
		ws.urlSubscriptions = options.URLSubscriptions
	} else {
		ws.urlSubs = newURLSubscriptionRegistry(manager, options.Services, options.LedgerInfoProvider)
		ws.urlSubscriptions = ws.urlSubs
	}
	ws.setPeerSource(options.PeerSource)
	return ws
}

func isNilURLSubscriptionService(service types.URLSubscriptionService) bool {
	if service == nil {
		return true
	}
	value := reflect.ValueOf(service)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
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
	var fwd string
	if f := forwardedForHeader(r); f != "" {
		fwd = f
	} else if xri := r.Header.Get("X-Real-IP"); xri != "" {
		fwd = strings.TrimSpace(xri)
	}
	clientIP := resolveWSClientIP(remoteAddrIP(r.RemoteAddr), fwd, portCtx)
	var resourceConsumer *resource.Consumer
	handshakeRole := roleForRequest(remoteAddrIP(r.RemoteAddr), userHeader(r), nil, portCtx)
	if handshakeRole.IsUnlimited() {
		resourceConsumer = ws.resourceManager.NewUnlimitedEndpoint(remoteAddrIP(r.RemoteAddr))
	} else if clientIP != "" {
		resourceConsumer = ws.resourceManager.NewInboundEndpoint(clientIP)
	}
	if resourceConsumer == nil {
		writePlainHTTPError(w, http.StatusServiceUnavailable, "Server is overloaded")
		return
	}
	consumerTransferred := false
	defer func() {
		if !consumerTransferred {
			resourceConsumer.Release()
		}
	}()

	ws.connectionsMutex.Lock()
	if ws.closing {
		ws.connectionsMutex.Unlock()
		writePlainHTTPError(w, http.StatusServiceUnavailable, "server shutting down")
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

	connection := subscription.NewConnection(generateConnectionID(), make(chan []byte, sendQueueLimit))
	wsConn := &websocketConnection{
		Connection:       connection,
		conn:             conn,
		portCtx:          portCtx,
		user:             userHeader(r),
		forwardedFor:     fwd,
		resourceConsumer: resourceConsumer,
	}

	if !ws.attachConnection(wsConn) {
		wsConn.closeSocket()
		return
	}
	consumerTransferred = true

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

// SubscriptionManager returns the subscription manager for event publishing
func (ws *WebSocketServer) SubscriptionManager() *subscription.Manager {
	return ws.subscriptionManager
}

func (ws *WebSocketServer) URLSubscriptionService() types.URLSubscriptionService {
	return ws.urlSubscriptions
}

// ExecuteMethod lets the json RPC forward through the same immutable registry
// and request context as a direct WebSocket command.
func (ws *WebSocketServer) ExecuteMethod(ctx *types.RpcContext, method string, params []byte) (any, *rpcerrors.RpcError) {
	return dispatchNestedMethod(ws.methodRegistry, ctx, method, params, wsLog())
}
