package did

import (
	"encoding/hex"
	"fmt"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/did"
)

// DIDSetBuilder provides a fluent interface for building DIDSet transactions.
type DIDSetBuilder struct {
	account     *jtx.Account
	uri         *string
	didDocument *string
	data        *string
	fee         uint64
	sequence    *uint32
	flags       uint32
}

// DIDSet creates a new DIDSetBuilder.
func DIDSet(account *jtx.Account) *DIDSetBuilder {
	return &DIDSetBuilder{
		account: account,
		fee:     10, // Default fee: 10 drops
	}
}

// URI sets the URI field for the DID.
// The URI will be hex-encoded when building the transaction.
// Pass an empty string to explicitly clear the URI field.
func (b *DIDSetBuilder) URI(uri string) *DIDSetBuilder {
	encoded := hex.EncodeToString([]byte(uri))
	b.uri = &encoded
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
	encoded := hex.EncodeToString([]byte(doc))
	b.didDocument = &encoded
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
	encoded := hex.EncodeToString([]byte(data))
	b.data = &encoded
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
func (b *DIDSetBuilder) Build() *did.DIDSet {
	d := did.NewDIDSet(b.account.Address)
	d.Fee = fmt.Sprintf("%d", b.fee)

	if b.uri != nil || b.didDocument != nil || b.data != nil {
		d.Common.PresentFields = make(map[string]bool)
	}
	if b.uri != nil {
		d.URI = *b.uri
		d.Common.PresentFields["URI"] = true
	}
	if b.didDocument != nil {
		d.DIDDocument = *b.didDocument
		d.Common.PresentFields["DIDDocument"] = true
	}
	if b.data != nil {
		d.Data = *b.data
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

// DIDDeleteBuilder provides a fluent interface for building DIDDelete transactions.
type DIDDeleteBuilder struct {
	account  *jtx.Account
	fee      uint64
	sequence *uint32
	flags    uint32
}

// DIDDelete creates a new DIDDeleteBuilder.
func DIDDelete(account *jtx.Account) *DIDDeleteBuilder {
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
func (b *DIDDeleteBuilder) Build() *did.DIDDelete {
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
