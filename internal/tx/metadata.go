package tx

import (
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
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

// affectedNodeToRippledFormat converts an AffectedNode to rippled's nested
// format for JSON output. It wraps the shared inner-node assembly so the
// RPC/JSON metadata and the binary/consensus metadata (MetadataToMap) stay in
// lockstep — including the empty `PreviousFields: {}` (E6 E1) marker and the
// DeletedNode PreviousTxnID placement.
func affectedNodeToRippledFormat(n AffectedNode) (map[string]any, error) {
	return map[string]any{
		n.NodeType: buildAffectedNodeInner(n),
	}, nil
}

// buildAffectedNodeInner assembles the inner content of a single AffectedNode
// (the object under "CreatedNode"/"ModifiedNode"/"DeletedNode"). It is the
// single source of truth shared by the JSON serializer
// (affectedNodeToRippledFormat) and the binary serializer (MetadataToMap), so
// the two encodings can never drift.
//
// The emission rules mirror rippled's ApplyStateTable:
//   - LedgerEntryType / LedgerIndex are always present.
//   - PreviousTxnID + PreviousTxnLgrSeq are emitted as a pair, both or neither
//     (one `if (!prevTxID.isZero())` guard, ApplyStateTable.cpp:560-572), and
//     only for ModifiedNode. On a DeletedNode rippled carries the prior-txn
//     info inside FinalFields via sMD_DeleteFinal, not at the node level; on a
//     CreatedNode there is no prior txn.
//   - FinalFields / PreviousFields / NewFields are omitted when empty, except
//     that an EmitEmptyPreviousFields signal forces an empty `PreviousFields:
//     {}` marker (the E6 E1 wire case) even when no orig value was recorded.
func buildAffectedNodeInner(n AffectedNode) map[string]any {
	inner := make(map[string]any)

	inner["LedgerEntryType"] = n.LedgerEntryType
	inner["LedgerIndex"] = n.LedgerIndex

	if n.NodeType == "ModifiedNode" && n.PreviousTxnID != "" && !isZeroHashHex(n.PreviousTxnID) {
		inner["PreviousTxnID"] = n.PreviousTxnID
		inner["PreviousTxnLgrSeq"] = n.PreviousTxnLgrSeq
	}

	if len(n.FinalFields) > 0 {
		inner["FinalFields"] = n.FinalFields
	}

	if len(n.PreviousFields) > 0 {
		inner["PreviousFields"] = n.PreviousFields
	} else if n.EmitEmptyPreviousFields {
		inner["PreviousFields"] = map[string]any{}
	}

	if len(n.NewFields) > 0 {
		inner["NewFields"] = n.NewFields
	}

	return inner
}

func isZeroHashHex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range 64 {
		if s[i] != '0' {
			return false
		}
	}
	return true
}
