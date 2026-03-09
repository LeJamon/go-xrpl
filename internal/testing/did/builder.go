package did

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/did"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

// DIDSetBuilder provides a fluent interface for building DIDSet transactions.
type DIDSetBuilder struct {
	account     *testing.Account
	uri         *string
	didDocument *string
	data        *string
	fee         uint64
	sequence    *uint32
	flags       uint32
}

// DIDSet creates a new DIDSetBuilder.
func DIDSet(account *testing.Account) *DIDSetBuilder {
	return &DIDSetBuilder{
		account: account,
		fee:     10, // Default fee: 10 drops
	}
}

// URI sets the URI field for the DID.
// The URI will be hex-encoded when building the transaction.
// Pass an empty string to explicitly clear the URI field.
func (b *DIDSetBuilder) URI(uri string) *DIDSetBuilder {
	b.uri = &uri
	return b
}

// URIHex sets the URI from an already hex-encoded string.
func (b *DIDSetBuilder) URIHex(uriHex string) *DIDSetBuilder {
	b.uri = &uriHex
	return b
}

// Document sets the DIDDocument field.
// The document will be hex-encoded when building the transaction.
// Pass an empty string to explicitly clear the DIDDocument field.
func (b *DIDSetBuilder) Document(doc string) *DIDSetBuilder {
	b.didDocument = &doc
	return b
}

// DocumentHex sets the DIDDocument from an already hex-encoded string.
func (b *DIDSetBuilder) DocumentHex(docHex string) *DIDSetBuilder {
	b.didDocument = &docHex
	return b
}

// Data sets the Data (attestation) field.
// The data will be hex-encoded when building the transaction.
// Pass an empty string to explicitly clear the Data field.
func (b *DIDSetBuilder) Data(data string) *DIDSetBuilder {
	b.data = &data
	return b
}

// DataHex sets the Data from an already hex-encoded string.
func (b *DIDSetBuilder) DataHex(dataHex string) *DIDSetBuilder {
	b.data = &dataHex
	return b
}

// Fee sets the transaction fee in drops.
func (b *DIDSetBuilder) Fee(f uint64) *DIDSetBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *DIDSetBuilder) Sequence(seq uint32) *DIDSetBuilder {
	b.sequence = &seq
	return b
}

// Flags sets transaction flags explicitly.
func (b *DIDSetBuilder) Flags(flags uint32) *DIDSetBuilder {
	b.flags = flags
	return b
}

// Build constructs the DIDSet transaction.
func (b *DIDSetBuilder) Build() tx.Transaction {
	d := did.NewDIDSet(b.account.Address)
	d.Fee = fmt.Sprintf("%d", b.fee)

	// Initialize PresentFields map if needed
	if d.Common.PresentFields == nil {
		d.Common.PresentFields = make(map[string]bool)
	}

	if b.uri != nil {
		if *b.uri != "" {
			// If URI is not already hex-encoded, encode it
			if !isHexEncoded(*b.uri) {
				d.URI = hex.EncodeToString([]byte(*b.uri))
			} else {
				d.URI = *b.uri
			}
		}
		// Mark field as present (even if empty) for clearing
		d.Common.PresentFields["URI"] = true
	}
	if b.didDocument != nil {
		if *b.didDocument != "" {
			if !isHexEncoded(*b.didDocument) {
				d.DIDDocument = hex.EncodeToString([]byte(*b.didDocument))
			} else {
				d.DIDDocument = *b.didDocument
			}
		}
		d.Common.PresentFields["DIDDocument"] = true
	}
	if b.data != nil {
		if *b.data != "" {
			if !isHexEncoded(*b.data) {
				d.Data = hex.EncodeToString([]byte(*b.data))
			} else {
				d.Data = *b.data
			}
		}
		d.Common.PresentFields["Data"] = true
	}
	if b.sequence != nil {
		d.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		d.SetFlags(b.flags)
	}

	return d
}

// BuildDIDSet is a convenience method that returns the concrete *did.DIDSet type.
func (b *DIDSetBuilder) BuildDIDSet() *did.DIDSet {
	return b.Build().(*did.DIDSet)
}

// DIDDeleteBuilder provides a fluent interface for building DIDDelete transactions.
type DIDDeleteBuilder struct {
	account  *testing.Account
	fee      uint64
	sequence *uint32
	flags    uint32
}

// DIDDelete creates a new DIDDeleteBuilder.
func DIDDelete(account *testing.Account) *DIDDeleteBuilder {
	return &DIDDeleteBuilder{
		account: account,
		fee:     10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *DIDDeleteBuilder) Fee(f uint64) *DIDDeleteBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *DIDDeleteBuilder) Sequence(seq uint32) *DIDDeleteBuilder {
	b.sequence = &seq
	return b
}

// Flags sets transaction flags explicitly.
func (b *DIDDeleteBuilder) Flags(flags uint32) *DIDDeleteBuilder {
	b.flags = flags
	return b
}

// Build constructs the DIDDelete transaction.
func (b *DIDDeleteBuilder) Build() tx.Transaction {
	d := did.NewDIDDelete(b.account.Address)
	d.Fee = fmt.Sprintf("%d", b.fee)

	if b.sequence != nil {
		d.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		d.SetFlags(b.flags)
	}

	return d
}

// BuildDIDDelete is a convenience method that returns the concrete *did.DIDDelete type.
func (b *DIDDeleteBuilder) BuildDIDDelete() *did.DIDDelete {
	return b.Build().(*did.DIDDelete)
}

// isHexEncoded checks if a string appears to be hex-encoded.
// Returns true if the string has even length and contains only hex characters.
func isHexEncoded(s string) bool {
	if len(s)%2 != 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}
