package rpc

import (
	"context"
	"net/http"
	"strings"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/gorilla/websocket"
)

type transportAuthorizedKey struct{}

func transportAuthorized(ctx context.Context) bool {
	value, _ := ctx.Value(transportAuthorizedKey{}).(bool)
	return value
}

func markTransportAuthorized(ctx context.Context) context.Context {
	return context.WithValue(ctx, transportAuthorizedKey{}, true)
}

func rejectTransport(w http.ResponseWriter, r *http.Request, status int, message string) {
	r.Close = true
	w.Header().Set("Connection", "close")
	writePlainHTTPError(w, status, message)
}

// authorizeTransport applies the immutable per-port browser-origin and Basic
// Auth policy before a request can consume a connection slot or reach a
// protocol handler.
func authorizeTransport(w http.ResponseWriter, r *http.Request, pc *PortContext) bool {
	if pc == nil {
		return true
	}
	if origins := r.Header.Values("Origin"); len(origins) > 1 {
		rejectTransport(w, r, http.StatusForbidden, "Forbidden")
		return false
	}
	rawOrigin := r.Header.Get("Origin")
	origin := strings.TrimSpace(rawOrigin)
	if rawOrigin != origin {
		rejectTransport(w, r, http.StatusForbidden, "Forbidden")
		return false
	}
	if origin != "" {
		canonical, err := config.CanonicalOrigin(origin)
		if err != nil || !allowedOrigin(pc.AllowedOrigins, canonical) {
			rejectTransport(w, r, http.StatusForbidden, "Forbidden")
			return false
		}
		w.Header().Set("Access-Control-Allow-Origin", canonical)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Add("Vary", "Origin")
	}
	// Browsers intentionally omit credentials from CORS preflight requests.
	// The actual POST/upgrade remains Basic-Auth protected; an allowed
	// preflight only exposes the negotiated transport policy.
	if r.Method == http.MethodOptions && origin != "" {
		return true
	}
	if !basicAuthMatches(r, pc) {
		w.Header().Set("WWW-Authenticate", `Basic realm="xrpld"`)
		rejectTransport(w, r, http.StatusUnauthorized, "Unauthorized")
		return false
	}
	return true
}

func basicAuthMatches(r *http.Request, pc *PortContext) bool {
	if pc == nil || (pc.User == "" && pc.Password == "") {
		return true
	}
	user, password, present := r.BasicAuth()
	match := constantTimeCredentialsMatch(user, password, pc.User, pc.Password)
	if !present {
		return false
	}
	return match
}

func allowedOrigin(origins []string, canonical string) bool {
	for _, origin := range origins {
		if origin == canonical {
			return true
		}
	}
	return false
}

// PortMiddleware returns an http.Handler that enforces per-port connection
// limits and injects the PortContext into the request context.
//
// For WebSocket upgrade requests the connection slot is NOT released when the
// middleware returns — WebSocketServer.closeConnection handles that instead.
// For regular HTTP requests the slot is released when the handler returns.
func PortMiddleware(pc *PortContext, limiter *ConnLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorizeTransport(w, r, pc) {
			return
		}

		// Enforce connection limit
		if limiter != nil && pc != nil && !limiter.TryAcquire(pc.PortName, pc.Limit) {
			http.Error(w, "Too many connections", http.StatusServiceUnavailable)
			return
		}

		isWS := isWebSocketUpgrade(r)

		// For non-WS requests, release the slot when the handler returns.
		// WS connections are long-lived; their slot is released in closeConnection.
		if limiter != nil && pc != nil && !isWS {
			defer limiter.Release(pc.PortName)
		}

		// Inject PortContext into request context
		ctx := markTransportAuthorized(WithPortContext(r.Context(), pc))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isWebSocketUpgrade returns true if the request is a WebSocket upgrade.
func isWebSocketUpgrade(r *http.Request) bool {
	return websocket.IsWebSocketUpgrade(r)
}
