package tx

import (
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// ApplyResult contains the result of applying a transaction
type ApplyResult struct {
	// Result is the transaction result code
	Result Result

	// Applied indicates if the transaction was applied to the ledger
	Applied bool

	// Fee is the fee charged (in drops)
	Fee uint64

	// Metadata contains the changes made by the transaction
	Metadata *Metadata

	// Message is a human-readable result message
	Message string
}

// Metadata tracks changes made by a transaction
type Metadata struct {
	// AffectedNodes lists all nodes that were created, modified, or deleted
	AffectedNodes []AffectedNode

	// TransactionIndex is the index in the ledger
	TransactionIndex uint32

	// TransactionResult is the result code
	TransactionResult Result

	// DeliveredAmount is the actual amount delivered (for partial payments)
	DeliveredAmount *Amount

	// ParentBatchID is the hash of the parent batch transaction.
	// Set only for inner transactions within a batch.
	// Reference: rippled TxMeta.h mParentBatchId
	ParentBatchID *[32]byte
}

// AffectedNode is an alias for state.AffectedNode
type AffectedNode = state.AffectedNode

// MarshalJSON implements custom JSON marshaling for Metadata to match rippled format
func (m Metadata) MarshalJSON() ([]byte, error) {
	// Build the output structure matching rippled's format
	output := make(map[string]any)

	// Sort AffectedNodes by LedgerIndex (ascending) to match rippled's ordering
	sortedNodes := make([]AffectedNode, len(m.AffectedNodes))
	copy(sortedNodes, m.AffectedNodes)
	sort.Slice(sortedNodes, func(i, j int) bool {
		return sortedNodes[i].LedgerIndex < sortedNodes[j].LedgerIndex
	})

	// AffectedNodes with nested structure
	affectedNodes := make([]map[string]any, 0, len(sortedNodes))
	for _, node := range sortedNodes {
		nodeJSON, err := affectedNodeToRippledFormat(node)
		if err != nil {
			return nil, err
		}
		affectedNodes = append(affectedNodes, nodeJSON)
	}
	output["AffectedNodes"] = affectedNodes

	// TransactionIndex
	output["TransactionIndex"] = m.TransactionIndex

	// TransactionResult as string
	output["TransactionResult"] = m.TransactionResult.String()

	// delivered_amount (snake_case per rippled format)
	// Use "unavailable" for legacy compatibility when not explicitly set
	if m.DeliveredAmount != nil {
		output["delivered_amount"] = m.DeliveredAmount
	}

	// ParentBatchID for inner batch transactions
	// Reference: rippled TxMeta.cpp getAsObject() lines 257-258
	if m.ParentBatchID != nil {
		output["ParentBatchID"] = strings.ToUpper(hex.EncodeToString(m.ParentBatchID[:]))
	}

	return json.Marshal(output)
}

// affectedNodeToRippledFormat converts an AffectedNode to rippled's nested format
func affectedNodeToRippledFormat(n AffectedNode) (map[string]any, error) {
	// Build the inner node content
	inner := make(map[string]any)

	// FinalFields (for ModifiedNode and DeletedNode)
	if n.FinalFields != nil {
		inner["FinalFields"] = n.FinalFields
	}

	// LedgerEntryType
	inner["LedgerEntryType"] = n.LedgerEntryType

	// LedgerIndex
	inner["LedgerIndex"] = n.LedgerIndex

	// PreviousFields (for ModifiedNode only, omit if nil/empty)
	if n.PreviousFields != nil && len(n.PreviousFields) > 0 {
		inner["PreviousFields"] = n.PreviousFields
	}

	// PreviousTxnID + PreviousTxnLgrSeq — present-or-absent as a
	// PAIR. Rippled writes them together inside one
	// `if (!prevTxID.isZero())` guard at ApplyStateTable.cpp:560-572,
	// so independent gates here would let one of the two leak when
	// the other is correctly omitted. Couple the predicates: emit
	// both when PreviousTxnID is a real (non-zero) hash, omit both
	// otherwise. Genesis case: master has no prior tx → PreviousTxnID
	// stays default zero → both fields stay off the wire (33+5 bytes
	// saved per meta blob touching genesis).
	prevTxnIDPresent := n.PreviousTxnID != "" && !isZeroHashHex(n.PreviousTxnID)
	if prevTxnIDPresent {
		inner["PreviousTxnID"] = n.PreviousTxnID
		inner["PreviousTxnLgrSeq"] = n.PreviousTxnLgrSeq
	}

	// NewFields (for CreatedNode only, omit if nil)
	if n.NewFields != nil {
		inner["NewFields"] = n.NewFields
	}

	// Wrap in NodeType (e.g., "ModifiedNode": {...})
	return map[string]any{
		n.NodeType: inner,
	}, nil
}

// isZeroHashHex reports whether s is the canonical 64-character
// hex string for the all-zero 256-bit hash. Accepts both upper and
// lower case; tolerates the empty string. Used by metadata
// serialization to mirror rippled's "omit defaulted optional fields"
// behavior — see affectedNodeToRippledFormat.
func isZeroHashHex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < 64; i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}
