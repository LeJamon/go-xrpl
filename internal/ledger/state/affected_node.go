package state

// AffectedNode represents a ledger entry affected by a transaction
type AffectedNode struct {
	// NodeType is "CreatedNode", "ModifiedNode", or "DeletedNode"
	NodeType string

	// LedgerEntryType is the type of ledger entry
	LedgerEntryType string

	// LedgerIndex is the key of the entry
	LedgerIndex string

	// PreviousTxnLgrSeq is the ledger sequence of the previous transaction that modified this entry
	PreviousTxnLgrSeq uint32

	// PreviousTxnID is the hash of the previous transaction that modified this entry
	PreviousTxnID string

	// FinalFields contains the final state (for Modified/Deleted)
	FinalFields map[string]any

	// PreviousFields contains the previous state (for Modified)
	PreviousFields map[string]any

	// NewFields contains the new state (for Created)
	NewFields map[string]any
}
