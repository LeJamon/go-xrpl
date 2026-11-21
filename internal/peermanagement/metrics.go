package peermanagement

import (
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"google.golang.org/protobuf/encoding/protowire"
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
	CategoryProofPathRequest
	CategoryProofPathResponse
	CategoryReplayDeltaRequest
	CategoryReplayDeltaResponse
	CategoryHaveTransactions
	CategoryRequestedTransactions
	CategoryGetSet
	CategoryShareSet
	CategoryLedgerDataTxSetGet
	CategoryLedgerDataTxSetShare
	CategoryLedgerDataTxNodeGet
	CategoryLedgerDataTxNodeShare
	CategoryLedgerDataStateNodeGet
	CategoryLedgerDataStateNodeShare
	CategoryLedgerDataGet
	CategoryLedgerDataShare
	CategoryGetLedgerTxSetShare
	CategoryGetLedgerTxSetGet
	CategoryGetLedgerTxNodeShare
	CategoryGetLedgerTxNodeGet
	CategoryGetLedgerStateNodeShare
	CategoryGetLedgerStateNodeGet
	CategoryGetLedgerShare
	CategoryGetLedgerGet
	CategoryShareHashLedger
	CategoryGetHashLedger
	CategoryShareHashTx
	CategoryGetHashTx
	CategoryShareHashTxNode
	CategoryGetHashTxNode
	CategoryShareHashStateNode
	CategoryGetHashStateNode
	CategoryShareCasObject
	CategoryGetCasObject
	CategoryShareFetchPack
	CategoryGetFetchPack
	CategoryGetTransactions
	CategoryShareHash
	CategoryGetHash
	CategoryTotal
	CategoryUnknown
)

// String returns the string representation of a category.
func (c TrafficCategory) String() string {
	if c >= 0 && int(c) < len(trafficCategoryNames) {
		return trafficCategoryNames[c]
	}
	return "unknown"
}

var trafficCategoryNames = [...]string{
	CategoryBase:                     "overhead",
	CategoryCluster:                  "overhead_cluster",
	CategoryOverlay:                  "overhead_overlay",
	CategoryManifests:                "overhead_manifest",
	CategoryTransaction:              "transactions",
	CategoryProposal:                 "proposals",
	CategoryValidation:               "validations",
	CategoryValidatorList:            "validator_lists",
	CategorySquelch:                  "squelch",
	CategoryLedgerData:               "ledger_data",
	CategoryProofPathRequest:         "proof_path_request",
	CategoryProofPathResponse:        "proof_path_response",
	CategoryReplayDeltaRequest:       "replay_delta_request",
	CategoryReplayDeltaResponse:      "replay_delta_response",
	CategoryHaveTransactions:         "have_transactions",
	CategoryRequestedTransactions:    "requested_transactions",
	CategoryGetSet:                   "set_get",
	CategoryShareSet:                 "set_share",
	CategoryLedgerDataTxSetGet:       "ledger_data_Transaction_Set_candidate_get",
	CategoryLedgerDataTxSetShare:     "ledger_data_Transaction_Set_candidate_share",
	CategoryLedgerDataTxNodeGet:      "ledger_data_Transaction_Node_get",
	CategoryLedgerDataTxNodeShare:    "ledger_data_Transaction_Node_share",
	CategoryLedgerDataStateNodeGet:   "ledger_data_Account_State_Node_get",
	CategoryLedgerDataStateNodeShare: "ledger_data_Account_State_Node_share",
	CategoryLedgerDataGet:            "ledger_data_get",
	CategoryLedgerDataShare:          "ledger_data_share",
	CategoryGetLedgerTxSetShare:      "ledger_Transaction_Set_candidate_share",
	CategoryGetLedgerTxSetGet:        "ledger_Transaction_Set_candidate_get",
	CategoryGetLedgerTxNodeShare:     "ledger_Transaction_Node_share",
	CategoryGetLedgerTxNodeGet:       "ledger_Transaction_Node_get",
	CategoryGetLedgerStateNodeShare:  "ledger_Account_State_Node_share",
	CategoryGetLedgerStateNodeGet:    "ledger_Account_State_Node_get",
	CategoryGetLedgerShare:           "ledger_share",
	CategoryGetLedgerGet:             "ledger_get",
	CategoryShareHashLedger:          "getobject_Ledger_share",
	CategoryGetHashLedger:            "getobject_Ledger_get",
	CategoryShareHashTx:              "getobject_Transaction_share",
	CategoryGetHashTx:                "getobject_Transaction_get",
	CategoryShareHashTxNode:          "getobject_Transaction_node_share",
	CategoryGetHashTxNode:            "getobject_Transaction_node_get",
	CategoryShareHashStateNode:       "getobject_Account_State_node_share",
	CategoryGetHashStateNode:         "getobject_Account_State_node_get",
	CategoryShareCasObject:           "getobject_CAS_share",
	CategoryGetCasObject:             "getobject_CAS_get",
	CategoryShareFetchPack:           "getobject_Fetch_Pack_share",
	CategoryGetFetchPack:             "getobject_Fetch Pack_get",
	CategoryGetTransactions:          "getobject_Transactions_get",
	CategoryShareHash:                "getobject_share",
	CategoryGetHash:                  "getobject_get",
	CategoryTotal:                    "total",
	CategoryUnknown:                  "unknown",
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
// total. inbound=false counts egress.
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
func CategorizeMessage(msgType message.MessageType) TrafficCategory {
	return describeMessage(msgType).traffic
}

type trafficPair struct {
	get, share TrafficCategory
}

func (p trafficPair) category(sharing bool) TrafficCategory {
	if sharing {
		return p.share
	}
	return p.get
}

var getObjectTraffic = [...]trafficPair{
	message.ObjectTypeUnknown:         {CategoryGetHash, CategoryShareHash},
	message.ObjectTypeLedger:          {CategoryGetHashLedger, CategoryShareHashLedger},
	message.ObjectTypeTransaction:     {CategoryGetHashTx, CategoryShareHashTx},
	message.ObjectTypeTransactionNode: {CategoryGetHashTxNode, CategoryShareHashTxNode},
	message.ObjectTypeStateNode:       {CategoryGetHashStateNode, CategoryShareHashStateNode},
	message.ObjectTypeCasObject:       {CategoryGetCasObject, CategoryShareCasObject},
	message.ObjectTypeFetchPack:       {CategoryGetFetchPack, CategoryShareFetchPack},
	message.ObjectTypeTransactions:    {CategoryGetTransactions, CategoryGetTransactions},
}

var getLedgerTraffic = [...]trafficPair{
	message.LedgerInfoBase:        {CategoryGetLedgerGet, CategoryGetLedgerShare},
	message.LedgerInfoTxNode:      {CategoryGetLedgerTxNodeGet, CategoryGetLedgerTxNodeShare},
	message.LedgerInfoAsNode:      {CategoryGetLedgerStateNodeGet, CategoryGetLedgerStateNodeShare},
	message.LedgerInfoTsCandidate: {CategoryGetLedgerTxSetGet, CategoryGetLedgerTxSetShare},
}

var ledgerDataTraffic = [...]trafficPair{
	message.LedgerInfoBase:        {CategoryLedgerDataGet, CategoryLedgerDataShare},
	message.LedgerInfoTxNode:      {CategoryLedgerDataTxNodeGet, CategoryLedgerDataTxNodeShare},
	message.LedgerInfoAsNode:      {CategoryLedgerDataStateNodeGet, CategoryLedgerDataStateNodeShare},
	message.LedgerInfoTsCandidate: {CategoryLedgerDataTxSetGet, CategoryLedgerDataTxSetShare},
}

func categorizeMessagePayload(msgType message.MessageType, payload []byte, inbound bool) TrafficCategory {
	switch msgType {
	case message.TypeHaveSet:
		return trafficPair{CategoryGetSet, CategoryShareSet}.category(!inbound)
	case message.TypeGetObjects:
		fields, ok := readTrafficFields(payload, 1, 2, getObjectTraffic[:])
		if ok && fields.hasFlag {
			return fields.pair.category(fields.flag == inbound)
		}
	case message.TypeGetLedger:
		fields, ok := readTrafficFields(payload, 1, 6, getLedgerTraffic[:])
		if ok {
			return fields.pair.category(inbound || fields.hasFlag)
		}
	case message.TypeLedgerData:
		fields, ok := readTrafficFields(payload, 3, 5, ledgerDataTraffic[:])
		if ok {
			return fields.pair.category(!inbound || fields.hasFlag)
		}
	default:
		return CategorizeMessage(msgType)
	}
	return CategoryUnknown
}

func outboundTrafficCategory(header *message.Header, frame []byte) TrafficCategory {
	payload := frame[header.HeaderSize():]
	if header.Compressed {
		switch header.MessageType {
		case message.TypeGetObjects, message.TypeGetLedger, message.TypeLedgerData:
			var err error
			payload, err = message.DecompressLZ4(payload, int(header.UncompressedSize))
			if err != nil {
				return CategoryUnknown
			}
		}
	}
	return categorizeMessagePayload(header.MessageType, payload, false)
}

type trafficFields struct {
	pair    trafficPair
	hasFlag bool
	flag    bool
}

// Read only the root scalars used by TrafficCount. A ledger-data frame may
// contain thousands of nodes whose contents do not affect its category.
func readTrafficFields(payload []byte, subtype, flag protowire.Number, categories []trafficPair) (fields trafficFields, found bool) {
	for len(payload) > 0 {
		number, wireType, n := protowire.ConsumeTag(payload)
		if n < 0 {
			return trafficFields{}, false
		}
		payload = payload[n:]
		if wireType != protowire.VarintType {
			n = protowire.ConsumeFieldValue(number, wireType, payload)
			if n < 0 {
				return trafficFields{}, false
			}
			payload = payload[n:]
			continue
		}
		value, n := protowire.ConsumeVarint(payload)
		if n < 0 {
			return trafficFields{}, false
		}
		switch number {
		case subtype:
			// Proto2 keeps the last recognized enum value.
			enum := int32(value)
			if enum >= 0 && int(enum) < len(categories) {
				fields.pair = categories[enum]
				found = true
			}
		case flag:
			fields.hasFlag = true
			fields.flag = value != 0
		}
		payload = payload[n:]
	}
	return fields, found
}

// Stats returns statistics for a category.
func (tc *TrafficCounter) Stats(cat TrafficCategory) *TrafficStats {
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

// TotalStats returns the total traffic statistics.
func (tc *TrafficCounter) TotalStats() *TrafficStats {
	return tc.Stats(CategoryTotal)
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
