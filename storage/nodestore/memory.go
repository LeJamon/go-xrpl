package nodestore

// MemoryBudget is the narrow admission contract used by optional managed
// caches. Implementations own the accounting policy; the NodeStore only
// reserves a conservative residency charge before retaining a value and
// releases that charge when the cache no longer owns it.
type MemoryBudget interface {
	TryReserve(uint64) bool
	Release(uint64)
}
