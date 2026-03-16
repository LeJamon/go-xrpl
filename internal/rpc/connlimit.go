package rpc

import "sync"

// ConnLimiter tracks concurrent connections per port name and enforces
// per-port connection limits. Matches rippled's ServerHandler onAccept/onClose
// counter pattern.
type ConnLimiter struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewConnLimiter creates a new ConnLimiter.
func NewConnLimiter() *ConnLimiter {
	return &ConnLimiter{counts: make(map[string]int)}
}

// TryAcquire attempts to reserve a connection slot for the given port.
// Returns false if limit > 0 and the port is already at capacity.
func (cl *ConnLimiter) TryAcquire(portName string, limit int) bool {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if limit > 0 && cl.counts[portName] >= limit {
		return false
	}
	cl.counts[portName]++
	return true
}

// Release frees a connection slot for the given port.
func (cl *ConnLimiter) Release(portName string) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.counts[portName] > 0 {
		cl.counts[portName]--
	}
}

// Count returns the current connection count for a port (for testing).
func (cl *ConnLimiter) Count(portName string) int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.counts[portName]
}
