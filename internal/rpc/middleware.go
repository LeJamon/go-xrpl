package rpc

import (
	"context"
	"net/http"
	"strings"

	"github.com/LeJamon/go-xrpl/config"
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
// Auth policy before a request can reach a protocol handler.
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
		w.Header().Set("WWW-Authenticate", `Basic realm="goxrpl"`)
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

// PortMiddleware applies per-port transport policy and injects the immutable
// port configuration. Connection admission is owned by the listener for the
// full TCP lifetime, including keep-alive requests and hijacked WebSockets.
func PortMiddleware(pc *PortContext, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authorizeTransport(w, r, pc) {
			return
		}
		ctx := markTransportAuthorized(WithPortContext(r.Context(), pc))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
