package entry

import (
	"errors"

	"github.com/LeJamon/goXRPLd/ledger/entry"
)

// DID represents a Decentralized Identifier ledger entry
// Reference: rippled/include/xrpl/protocol/detail/ledger_entries.macro ltDID
type DID struct {
	BaseEntry

	// Required fields
	Account   [20]byte // The account that owns this DID
	OwnerNode uint64   // Directory node hint

	// Optional fields
	DIDDocument *[]byte // The DID document (W3C DID Core format)
	URI         *string // URI for the DID document
	Data        *[]byte // Arbitrary data associated with the DID
}

func (d *DID) Type() entry.Type {
	return entry.TypeDID
}

func (d *DID) Validate() error {
	if d.Account == [20]byte{} {
		return errors.New("account is required")
	}
	// At least one of DIDDocument, URI, or Data should be present
	if d.DIDDocument == nil && d.URI == nil && d.Data == nil {
		return errors.New("at least one of DIDDocument, URI, or Data is required")
	}
	return nil
}

func (d *DID) Hash() ([32]byte, error) {
	hash := d.BaseEntry.Hash()
	for i := 0; i < 20; i++ {
		hash[i] ^= d.Account[i]
	}
	return hash, nil
}
