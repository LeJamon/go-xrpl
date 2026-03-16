package rpc

import (
	"net/http"
	"strings"
)

// PortMiddleware returns an http.Handler that enforces per-port connection
// limits and injects the PortContext into the request context.
//
// For WebSocket upgrade requests the connection slot is NOT released when the
// middleware returns — WebSocketServer.closeConnection handles that instead.
// For regular HTTP requests the slot is released when the handler returns.
func PortMiddleware(pc *PortContext, limiter *ConnLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Enforce connection limit
		if limiter != nil && !limiter.TryAcquire(pc.PortName, pc.Limit) {
			http.Error(w, "Too many connections", http.StatusServiceUnavailable)
			return
		}

		isWS := isWebSocketUpgrade(r)

		// For non-WS requests, release the slot when the handler returns.
		// WS connections are long-lived; their slot is released in closeConnection.
		if limiter != nil && !isWS {
			defer limiter.Release(pc.PortName)
		}

		// Inject PortContext into request context
		ctx := WithPortContext(r.Context(), pc)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isWebSocketUpgrade returns true if the request is a WebSocket upgrade.
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
