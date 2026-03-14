package did

// DIDEntry represents a DID ledger entry
// Reference: rippled ledger_entries.macro ltDID (0x0049)
type DIDEntry struct {
	// Required fields
	Account   [20]byte // The account that owns this DID
	OwnerNode uint64   // Directory page hint

	// Optional fields (stored as hex in the ledger, nil means not present)
	URI         *string // URI for the DID document (hex-encoded)
	DIDDocument *string // The DID document content (hex-encoded)
	Data        *string // Arbitrary data (hex-encoded)

	// Transaction threading
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// HasAnyField returns true if at least one optional field is present and non-empty
func (d *DIDEntry) HasAnyField() bool {
	return (d.URI != nil && *d.URI != "") ||
		(d.DIDDocument != nil && *d.DIDDocument != "") ||
		(d.Data != nil && *d.Data != "")
}
