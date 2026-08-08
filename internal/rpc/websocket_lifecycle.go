package rpc

import (
	"context"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// attachConnection is the single point at which a new WS connection becomes
// visible to both the per-server connection map and the subscription manager.
// Pairing this with detachConnection keeps the two registries synchronized.
func (ws *WebSocketServer) attachConnection(wsConn *websocketConnection) bool {
	ws.bindPathFindRefreshManager(wsConn)
	wsConn.SetDisconnect(wsConn.closeSocket)

	ws.connectionsMutex.Lock()
	if ws.closing {
		ws.connectionsMutex.Unlock()
		return false
	}
	if _, exists := ws.connections[wsConn.ID()]; exists {
		ws.connectionsMutex.Unlock()
		return false
	}
	registration, attached := ws.subscriptionManager.Attach(wsConn.Connection)
	if !attached {
		ws.connectionsMutex.Unlock()
		return false
	}
	wsConn.registration = registration
	ws.wg.Add(3)
	ws.connections[wsConn.ID()] = wsConn
	ws.connectionsMutex.Unlock()
	return true
}

// detachConnection is the inverse of attachConnection.
func (ws *WebSocketServer) detachConnection(wsConn *websocketConnection) {
	ws.connectionsMutex.Lock()
	if current := ws.connections[wsConn.ID()]; current == wsConn {
		delete(ws.connections, wsConn.ID())
	}
	ws.connectionsMutex.Unlock()
	ws.subscriptionManager.Detach(wsConn.registration)
	removeAccountHistoryConnection(ws.services, wsConn.Connection)
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
	wsConn.resourceConsumer.Release()

	wsConn.clearPathFindSession()

	ws.detachConnection(wsConn)

	wsConn.closeSocket()

	wsLog().Debug("WebSocket connection closed", "connID", wsConn.ID())
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
