package peermanagement

import (
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

// numTrafficCategories is the count of contiguous TrafficCategory enum
// values; the counter is a fixed array indexed by category.
const numTrafficCategories = int(CategoryUnknown) + 1

// TrafficCounter tracks ingress and egress traffic by category. The
// per-category atomics live in a fixed array indexed by category, so no
// map and no mutex are needed — the set of categories is fixed at compile
// time and each slot is independently atomic.
type TrafficCounter struct {
	counts [numTrafficCategories]atomicStats
}

// NewTrafficCounter creates a new TrafficCounter.
func NewTrafficCounter() *TrafficCounter {
	return &TrafficCounter{}
}

// AddCount records traffic for a category and mirrors it into the running
// total. inbound=false counts egress; the per-peer writeLoop now records
// outbound frames so MessagesOut/BytesOut are populated.
func (tc *TrafficCounter) AddCount(cat TrafficCategory, inbound bool, bytes int) {
	if cat < 0 || int(cat) >= numTrafficCategories {
		return
	}
	add := func(stats *atomicStats) {
		if inbound {
			stats.bytesIn.Add(uint64(bytes))
			stats.messagesIn.Add(1)
		} else {
			stats.bytesOut.Add(uint64(bytes))
			stats.messagesOut.Add(1)
		}
	}
	add(&tc.counts[cat])
	if cat != CategoryTotal {
		add(&tc.counts[CategoryTotal])
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
	if cat < 0 || int(cat) >= numTrafficCategories {
		return nil
	}
	stats := &tc.counts[cat]
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
	for i := range tc.counts {
		stats := &tc.counts[i]
		stats.bytesIn.Store(0)
		stats.bytesOut.Store(0)
		stats.messagesIn.Store(0)
		stats.messagesOut.Store(0)
	}
}
