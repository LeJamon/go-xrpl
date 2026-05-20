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

	// EmitEmptyPreviousFields signals that the meta serializer should emit
	// an empty `PreviousFields: {}` object even when PreviousFields is
	// otherwise nil/empty. This mirrors rippled's ApplyStateTable.cpp
	// behavior where the prevs loop iterates origNode's v_ — including
	// STI_NOTPRESENT template entries — and adds any sMD_ChangeOrig-eligible
	// field that's "absent in orig, present in cur" as an STI_NOTPRESENT
	// entry. That entry serializes to zero bytes but makes `prevs.empty()`
	// false, so `if (!prevs.empty()) emplace_back(prevs)` runs and emits
	// the `E6 E1` empty-PreviousFields marker. Without this signal, goxrpl
	// drops the marker and the tx-tree leaf hash diverges from rippled.
	EmitEmptyPreviousFields bool
}
