package peermanagement

// MemoryBudget accounts for retained payloads and wakes readers when capacity returns.
type MemoryBudget interface {
	TryReserve(uint64) bool
	Release(uint64)
	Changed() <-chan struct{}
}
