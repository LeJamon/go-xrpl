package rpc

import (
	"io"
	"time"

	"github.com/gorilla/websocket"
)

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
		message, err := io.ReadAll(io.LimitReader(reader, maxRequestBytes+1))
		if err != nil {
			wsLog().Debug("WebSocket read error", "err", err)
			return
		}
		if len(message) > maxRequestBytes {
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
			// control-write lock. The message-frame writer and control frames
			// therefore use separate gorilla synchronization paths.
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
