package peermanagement

import (
	"sync"
	"sync/atomic"
)

// TrafficCategory represents a traffic category for counting.
type TrafficCategory int

const (
	CategoryBase TrafficCategory = iota
	CategoryCluster
	CategoryOverlay
	CategoryManifests
	CategoryTransaction
	CategoryProposal
	CategoryValidation
	CategoryValidatorList
	CategorySquelch
	CategoryLedgerData
	CategoryTotal
	CategoryUnknown
)

// String returns the string representation of a category.
func (c TrafficCategory) String() string {
	names := map[TrafficCategory]string{
		CategoryBase:          "overhead",
		CategoryCluster:       "overhead_cluster",
		CategoryOverlay:       "overhead_overlay",
		CategoryManifests:     "overhead_manifest",
		CategoryTransaction:   "transactions",
		CategoryProposal:      "proposals",
		CategoryValidation:    "validations",
		CategoryValidatorList: "validator_lists",
		CategorySquelch:       "squelch",
		CategoryLedgerData:    "ledger_data",
		CategoryTotal:         "total",
		CategoryUnknown:       "unknown",
	}
	if name, ok := names[c]; ok {
		return name
	}
	return "unknown"
}

// TrafficStats holds traffic statistics.
type TrafficStats struct {
	Name        string
	BytesIn     uint64
	BytesOut    uint64
	MessagesIn  uint64
	MessagesOut uint64
}

type atomicStats struct {
	bytesIn     atomic.Uint64
	bytesOut    atomic.Uint64
	messagesIn  atomic.Uint64
	messagesOut atomic.Uint64
}

// TrafficCounter tracks ingress and egress traffic by category.
type TrafficCounter struct {
	mu     sync.RWMutex
	counts map[TrafficCategory]*atomicStats
}

// NewTrafficCounter creates a new TrafficCounter.
func NewTrafficCounter() *TrafficCounter {
	tc := &TrafficCounter{
		counts: make(map[TrafficCategory]*atomicStats),
	}

	categories := []TrafficCategory{
		CategoryBase, CategoryCluster, CategoryOverlay, CategoryManifests,
		CategoryTransaction, CategoryProposal, CategoryValidation,
		CategoryValidatorList, CategorySquelch, CategoryLedgerData,
		CategoryTotal, CategoryUnknown,
	}

	for _, cat := range categories {
		tc.counts[cat] = &atomicStats{}
	}

	return tc
}

// AddCount records traffic for a category.
func (tc *TrafficCounter) AddCount(cat TrafficCategory, inbound bool, bytes int) {
	tc.mu.RLock()
	stats, exists := tc.counts[cat]
	tc.mu.RUnlock()

	if !exists {
		return
	}

	if inbound {
		stats.bytesIn.Add(uint64(bytes))
		stats.messagesIn.Add(1)
	} else {
		stats.bytesOut.Add(uint64(bytes))
		stats.messagesOut.Add(1)
	}

	// Also update total
	tc.mu.RLock()
	total := tc.counts[CategoryTotal]
	tc.mu.RUnlock()

	if total != nil {
		if inbound {
			total.bytesIn.Add(uint64(bytes))
			total.messagesIn.Add(1)
		} else {
			total.bytesOut.Add(uint64(bytes))
			total.messagesOut.Add(1)
		}
	}
}

// CategorizeMessage determines the traffic category for a message type.
func CategorizeMessage(msgType uint16) TrafficCategory {
	switch msgType {
	case 3: // TypePing
		return CategoryBase
	case 5: // TypeCluster
		return CategoryCluster
	case 15: // TypeEndpoints
		return CategoryOverlay
	case 2: // TypeManifests
		return CategoryManifests
	case 30, 64: // TypeTransaction, TypeTransactions
		return CategoryTransaction
	case 33: // TypeProposeLedger
		return CategoryProposal
	case 41: // TypeValidation
		return CategoryValidation
	case 54, 56: // TypeValidatorList, TypeValidatorListCollection
		return CategoryValidatorList
	case 55: // TypeSquelch
		return CategorySquelch
	case 31, 32: // TypeGetLedger, TypeLedgerData
		return CategoryLedgerData
	default:
		return CategoryUnknown
	}
}

// GetStats returns statistics for a category.
func (tc *TrafficCounter) GetStats(cat TrafficCategory) *TrafficStats {
	tc.mu.RLock()
	stats, exists := tc.counts[cat]
	tc.mu.RUnlock()

	if !exists {
		return nil
	}

	return &TrafficStats{
		Name:        cat.String(),
		BytesIn:     stats.bytesIn.Load(),
		BytesOut:    stats.bytesOut.Load(),
		MessagesIn:  stats.messagesIn.Load(),
		MessagesOut: stats.messagesOut.Load(),
	}
}

// GetTotalStats returns the total traffic statistics.
func (tc *TrafficCounter) GetTotalStats() *TrafficStats {
	return tc.GetStats(CategoryTotal)
}

// Reset resets all counters.
func (tc *TrafficCounter) Reset() {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	for _, stats := range tc.counts {
		stats.bytesIn.Store(0)
		stats.bytesOut.Store(0)
		stats.messagesIn.Store(0)
		stats.messagesOut.Store(0)
	}
}
