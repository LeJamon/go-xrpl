package rpc

import (
	"context"
	"net"
)

type portContextKey struct{}

// PortContext holds per-port configuration attached to each request.
// It is injected by PortMiddleware and consumed by roleForRequest
// and WebSocketServer to enforce per-port access control and limits.
type PortContext struct {
	PortName  string
	AdminNets []net.IPNet
	// SecureGatewayNets is the per-port secure_gateway allowlist. A
	// peer in this set may carry X-Forwarded-For / X-User for
	// downstream client-IP attribution and (with X-User) is promoted
	// to RoleIdentified for resource-limit purposes. Admin promotion
	// always requires AdminNets membership.
	SecureGatewayNets []net.IPNet
	Limit             int // max concurrent connections; 0 = unlimited
	SendQueue         int // WS send channel buffer size; 0 = use default (100)
}

// WithPortContext returns a new context carrying the given PortContext.
func WithPortContext(ctx context.Context, pc *PortContext) context.Context {
	return context.WithValue(ctx, portContextKey{}, pc)
}

// GetPortContext extracts the PortContext from a context, or nil if absent.
func GetPortContext(ctx context.Context) *PortContext {
	pc, _ := ctx.Value(portContextKey{}).(*PortContext)
	return pc
}
