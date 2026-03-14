// Package metadata provides test helpers for validating transaction metadata.
// Ported from rippled test helpers (Discrepancy_test.cpp, Freeze_test.cpp, etc.).
package metadata

import (
	"strconv"
	"testing"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/stretchr/testify/require"
)

// CheckXRPConservation verifies that XRP is conserved across a transaction's metadata.
// For every AccountRoot in AffectedNodes, sums the previous and final Balance values.
// Verifies: sumPrev - sumFinal == fee (XRP destroyed equals the fee).
// Ported from rippled's Discrepancy_test.cpp testXRPDiscrepancy().
func CheckXRPConservation(t *testing.T, result jtx.TxResult, fee uint64) {
	t.Helper()

	meta := result.Metadata
	require.NotNil(t, meta, "Metadata should not be nil")

	var sumPrev, sumFinal uint64

	for _, node := range meta.AffectedNodes {
		if node.LedgerEntryType != "AccountRoot" {
			continue
		}

		switch node.NodeType {
		case "ModifiedNode":
			// FinalFields always has the current Balance
			finalBal := extractBalance(node.FinalFields)
			sumFinal += finalBal

			// PreviousFields has the old Balance if it changed
			if node.PreviousFields != nil {
				if _, hasPrevBal := node.PreviousFields["Balance"]; hasPrevBal {
					sumPrev += extractBalance(node.PreviousFields)
				} else {
					// Balance didn't change, prev == final
					sumPrev += finalBal
				}
			} else {
				sumPrev += finalBal
			}

		case "CreatedNode":
			// New account — previous balance was 0
			finalBal := extractBalance(node.NewFields)
			sumFinal += finalBal
			// sumPrev += 0 (account didn't exist before)

		case "DeletedNode":
			// Deleted account — final balance goes to 0
			prevBal := extractBalance(node.FinalFields)
			sumPrev += prevBal
			// sumFinal += 0 (account no longer exists)
		}
	}

	// XRP conservation: what was destroyed equals the fee
	require.Equal(t, fee, sumPrev-sumFinal,
		"XRP conservation: sumPrev(%d) - sumFinal(%d) should equal fee(%d)",
		sumPrev, sumFinal, fee)
}

// CheckNodeCount verifies the number of AffectedNodes in metadata.
func CheckNodeCount(t *testing.T, result jtx.TxResult, expected int) {
	t.Helper()
	meta := result.Metadata
	require.NotNil(t, meta, "Metadata should not be nil")
	require.Equal(t, expected, len(meta.AffectedNodes),
		"Expected %d AffectedNodes, got %d", expected, len(meta.AffectedNodes))
}

// FindNode finds the first AffectedNode matching the given type and entry type.
func FindNode(meta *tx.Metadata, nodeType, entryType string) *tx.AffectedNode {
	for i := range meta.AffectedNodes {
		n := &meta.AffectedNodes[i]
		if n.NodeType == nodeType && n.LedgerEntryType == entryType {
			return n
		}
	}
	return nil
}

// FindNodes finds all AffectedNodes matching the given type and entry type.
func FindNodes(meta *tx.Metadata, nodeType, entryType string) []*tx.AffectedNode {
	var result []*tx.AffectedNode
	for i := range meta.AffectedNodes {
		n := &meta.AffectedNodes[i]
		if n.NodeType == nodeType && n.LedgerEntryType == entryType {
			result = append(result, n)
		}
	}
	return result
}

// FindNodeByAccount finds the first ModifiedNode AccountRoot for a specific account.
func FindNodeByAccount(meta *tx.Metadata, account string) *tx.AffectedNode {
	for i := range meta.AffectedNodes {
		n := &meta.AffectedNodes[i]
		if n.LedgerEntryType != "AccountRoot" {
			continue
		}
		var fields map[string]any
		switch n.NodeType {
		case "ModifiedNode":
			fields = n.FinalFields
		case "CreatedNode":
			fields = n.NewFields
		case "DeletedNode":
			fields = n.FinalFields
		}
		if fields == nil {
			continue
		}
		if acct, ok := fields["Account"].(string); ok && acct == account {
			return n
		}
	}
	return nil
}

// GetFinalField extracts a field from FinalFields of an AffectedNode.
func GetFinalField(node *tx.AffectedNode, field string) any {
	if node == nil || node.FinalFields == nil {
		return nil
	}
	return node.FinalFields[field]
}

// GetPreviousField extracts a field from PreviousFields of an AffectedNode.
func GetPreviousField(node *tx.AffectedNode, field string) any {
	if node == nil || node.PreviousFields == nil {
		return nil
	}
	return node.PreviousFields[field]
}

// GetNewField extracts a field from NewFields of an AffectedNode.
func GetNewField(node *tx.AffectedNode, field string) any {
	if node == nil || node.NewFields == nil {
		return nil
	}
	return node.NewFields[field]
}

// extractBalance extracts the Balance field from a fields map.
// Balance is stored as a string of drops.
func extractBalance(fields map[string]any) uint64 {
	if fields == nil {
		return 0
	}
	bal, ok := fields["Balance"]
	if !ok {
		return 0
	}
	switch v := bal.(type) {
	case string:
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0
		}
		return n
	case float64:
		return uint64(v)
	case uint64:
		return v
	case int:
		return uint64(v)
	default:
		return 0
	}
}

// ToUint32 converts various numeric types to uint32.
func ToUint32(v any) uint32 {
	switch val := v.(type) {
	case uint32:
		return val
	case float64:
		return uint32(val)
	case int:
		return uint32(val)
	case int64:
		return uint32(val)
	case uint64:
		return uint32(val)
	default:
		return 0
	}
}
