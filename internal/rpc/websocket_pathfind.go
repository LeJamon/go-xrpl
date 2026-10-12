package rpc

import (
	"context"
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type pathFindUpdateTarget struct {
	connection *websocketConnection
	session    *PathFindSession
	generation uint64
}

func (ws *WebSocketServer) executePathFind(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (any, *rpcerrors.RpcError) {
	if rpcErr := handlers.RequirePathSearch(ctx); rpcErr != nil {
		return nil, rpcErr
	}
	var params map[string]json.RawMessage
	if len(cmd.Params) == 0 || json.Unmarshal(cmd.Params, &params) != nil {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	rawSubcommand, exists := params["subcommand"]
	if !exists {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	var subcommand *string
	if err := json.Unmarshal(rawSubcommand, &subcommand); err != nil || subcommand == nil {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	wsConn.SetAPIVersion(ctx.ApiVersion)

	switch *subcommand {
	case "create":
		ctx.LoadCost = uint32(resource.FeeHeavyBurdenRPC().Cost())
		return ws.executePathFindCreate(wsConn, ctx, cmd)
	case "close":
		return ws.executePathFindClose(wsConn, ctx, cmd)
	case "status":
		return ws.executePathFindStatus(wsConn, ctx, cmd)
	default:
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
}

// executePathFindCreate creates a new persistent pathfinding session.
// Any existing session on this connection is replaced (matching rippled).
func (ws *WebSocketServer) executePathFindCreate(wsConn *websocketConnection, ctx *types.RpcContext, cmd types.WebSocketCommand) (any, *rpcerrors.RpcError) {
	ws.bindPathFindRefreshManager(wsConn)
	wsConn.clearPathFindSession()

	session, rpcErr := ParseAndCreateSession(cmd.Params, cmd.ID)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if ctx.Services == nil || ctx.Services.Ledger() == nil {
		return nil, rpcerrors.NewRpcError(rpcerrors.RpcNO_CURRENT, "noCurrent", "noCurrent",
			"No closed ledger available")
	}
	session.setSearchLevelMax(ctx.Services.Capabilities().PathSearchMax)
	var view types.LedgerStateView
	var err error
	if source, ok := ctx.Services.LedgerViews().(types.ContextClosedLedgerViewSource); ok {
		view, err = source.GetClosedLedgerViewContext(ctx.Context)
	} else {
		view, err = ctx.Services.LedgerViews().GetClosedLedgerView()
	}
	if err != nil {
		closePathFindView(view)
		return nil, rpcerrors.NewRpcError(rpcerrors.RpcNO_CURRENT, "noCurrent", "noCurrent",
			"No closed ledger available")
	}
	defer closePathFindView(view)

	event := session.Execute(view, false)

	wsConn.installPathFindSession(session)
	ws.queuePathFindSessionsContext(currentPathFindViewContext(ctx.Services), wsConn)

	return event, nil
}

// executePathFindClose closes the active pathfinding session on this connection.
func (ws *WebSocketServer) executePathFindClose(wsConn *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *rpcerrors.RpcError) {
	session := wsConn.clearPathFindSession()

	if session == nil {
		return nil, rpcerrors.RpcErrorNoPathRequest()
	}

	return session.Close(), nil
}

// executePathFindStatus returns the current status of the active pathfinding session.
func (ws *WebSocketServer) executePathFindStatus(wsConn *websocketConnection, _ *types.RpcContext, _ types.WebSocketCommand) (any, *rpcerrors.RpcError) {
	wsConn.mutex.RLock()
	session := wsConn.pathFindSession
	wsConn.mutex.RUnlock()

	if session == nil {
		return nil, rpcerrors.RpcErrorNoPathRequest()
	}

	return session.Status(), nil
}

// UpdatePathFindSessions snapshots active sessions and queues one bounded,
// asynchronous refresh generation. The ledger-close callback must not perform
// ledger-view acquisition or path computation itself.
func (ws *WebSocketServer) UpdatePathFindSessions(getView func() (types.LedgerStateView, error)) {
	ws.queuePathFindSessionsContext(func(ctx context.Context) (types.LedgerStateView, error) {
		if getView == nil {
			return nil, nil
		}
		view, err := getView()
		if err != nil {
			return view, err
		}
		if _, leased := view.(types.LedgerReadCloser); leased || ws.services == nil {
			return view, nil
		}
		if source, ok := ws.services.LedgerViews().(types.ContextClosedLedgerViewSource); ok {
			return source.GetClosedLedgerViewContext(ctx)
		}
		return view, nil
	}, nil)
}

// UpdatePathFindSessionsContext is the lease-aware variant used by runtime
// callers that can provide a context-aware ledger view source.
func (ws *WebSocketServer) UpdatePathFindSessionsContext(getView func(context.Context) (types.LedgerStateView, error)) {
	ws.queuePathFindSessionsContext(getView, nil)
}

func (ws *WebSocketServer) queuePathFindSessions(getView func() (types.LedgerStateView, error), additional *websocketConnection) {
	ws.queuePathFindSessionsContext(func(context.Context) (types.LedgerStateView, error) {
		if getView == nil {
			return nil, nil
		}
		return getView()
	}, additional)
}

func (ws *WebSocketServer) queuePathFindSessionsContext(getView func(context.Context) (types.LedgerStateView, error), additional *websocketConnection) {
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

	manager.enqueueContext(getView, targets)
}

func currentPathFindViewContext(services *types.ServiceGraph) func(context.Context) (types.LedgerStateView, error) {
	return func(ctx context.Context) (types.LedgerStateView, error) {
		ledger := services.Ledger()
		if source, ok := ledger.(types.ContextLedgerViewSource); ok {
			view, _, err := source.GetLedgerViewBySeqContext(ctx, ledger.GetCurrentLedgerIndex())
			return view, err
		}
		if source, ok := ledger.(types.LedgerViewSource); ok {
			view, _, err := source.GetLedgerViewBySeq(ledger.GetCurrentLedgerIndex())
			return view, err
		}
		if source, ok := services.LedgerViews().(types.ContextClosedLedgerViewSource); ok {
			return source.GetClosedLedgerViewContext(ctx)
		}
		return services.LedgerViews().GetClosedLedgerView()
	}
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
