package rpc

import "sync"

// DefaultMaxTotalConnections bounds the process-wide count of concurrent
// HTTP + WebSocket connections when the operator does not configure an explicit
// ceiling. Without it, a WS port left at the default (per-port limit unset =
// unlimited) has no backpressure on connection count, and every connection
// allocates a send buffer, goroutines, and subscription state — an inbound
// memory-DoS surface. An operator can raise or disable it via [server]
// max_connections (0 keeps this bounded default; a negative value disables the
// global cap entirely).
const DefaultMaxTotalConnections = 8192

// ConnLimiter tracks concurrent connections per port name and enforces both a
// per-port connection limit and a process-wide ceiling. Matches rippled's
// ServerHandler onAccept/onClose counter pattern, plus the global budget that
// rippled derives from its io_service thread pool.
type ConnLimiter struct {
	mu          sync.Mutex
	counts      map[string]int
	total       int
	globalLimit int
}

// NewConnLimiter creates a ConnLimiter seeded with the bounded default global
// ceiling. Override it with SetGlobalLimit before serving.
func NewConnLimiter() *ConnLimiter {
	return &ConnLimiter{counts: make(map[string]int), globalLimit: DefaultMaxTotalConnections}
}

// SetGlobalLimit overrides the process-wide connection ceiling. A value <= 0
// disables the global cap (per-port limits still apply). Call before serving.
func (cl *ConnLimiter) SetGlobalLimit(n int) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.globalLimit = n
}

// TryAcquire attempts to reserve a connection slot for the given port. It fails
// when the process-wide ceiling is reached, or when limit > 0 and the port is
// already at capacity.
func (cl *ConnLimiter) TryAcquire(portName string, limit int) bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.globalLimit > 0 && cl.total >= cl.globalLimit {
		return false
	}
	if limit > 0 && cl.counts[portName] >= limit {
		return false
	}
	cl.counts[portName]++
	cl.total++
	return true
}

// Release frees a connection slot for the given port.
func (cl *ConnLimiter) Release(portName string) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.counts[portName] > 0 {
		cl.counts[portName]--
		cl.total--
	}
}

// Count returns the current connection count for a port (for testing).
func (cl *ConnLimiter) Count(portName string) int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.counts[portName]
}

// Total returns the current process-wide connection count (for testing).
func (cl *ConnLimiter) Total() int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.total
}
