package credential

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/credential"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

// CredentialCreateBuilder provides a fluent interface for building CredentialCreate transactions.
type CredentialCreateBuilder struct {
	account        *testing.Account
	subject        *testing.Account
	credentialType string
	uri            string
	expiration     *uint32
	fee            uint64
	sequence       *uint32
	flags          uint32
}

// CredentialCreate creates a new CredentialCreateBuilder.
// The account (issuer) creates a credential for the subject.
func CredentialCreate(account, subject *testing.Account, credentialType string) *CredentialCreateBuilder {
	return &CredentialCreateBuilder{
		account:        account,
		subject:        subject,
		credentialType: credentialType,
		fee:            10, // Default fee: 10 drops
	}
}

// URI sets the URI for the credential.
// The URI will be hex-encoded when building the transaction.
func (b *CredentialCreateBuilder) URI(uri string) *CredentialCreateBuilder {
	b.uri = uri
	return b
}

// URIHex sets the URI from an already hex-encoded string.
func (b *CredentialCreateBuilder) URIHex(uriHex string) *CredentialCreateBuilder {
	b.uri = uriHex
	return b
}

// Expiration sets when the credential expires (in Ripple epoch seconds).
func (b *CredentialCreateBuilder) Expiration(exp uint32) *CredentialCreateBuilder {
	b.expiration = &exp
	return b
}

// Fee sets the transaction fee in drops.
func (b *CredentialCreateBuilder) Fee(f uint64) *CredentialCreateBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *CredentialCreateBuilder) Sequence(seq uint32) *CredentialCreateBuilder {
	b.sequence = &seq
	return b
}

// Flags sets transaction flags explicitly.
func (b *CredentialCreateBuilder) Flags(flags uint32) *CredentialCreateBuilder {
	b.flags = flags
	return b
}

// Build constructs the CredentialCreate transaction.
func (b *CredentialCreateBuilder) Build() tx.Transaction {
	// Hex-encode the credential type if not already hex
	credType := b.credentialType
	if !isHexEncoded(credType) {
		credType = hex.EncodeToString([]byte(credType))
	}

	c := credential.NewCredentialCreate(b.account.Address, b.subject.Address, credType)
	c.Fee = fmt.Sprintf("%d", b.fee)

	if b.uri != "" {
		if !isHexEncoded(b.uri) {
			c.URI = hex.EncodeToString([]byte(b.uri))
		} else {
			c.URI = b.uri
		}
	}
	if b.expiration != nil {
		c.Expiration = b.expiration
	}
	if b.sequence != nil {
		c.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		c.SetFlags(b.flags)
	}

	return c
}

// BuildCredentialCreate is a convenience method that returns the concrete *credential.CredentialCreate type.
func (b *CredentialCreateBuilder) BuildCredentialCreate() *credential.CredentialCreate {
	return b.Build().(*credential.CredentialCreate)
}

// CredentialAcceptBuilder provides a fluent interface for building CredentialAccept transactions.
type CredentialAcceptBuilder struct {
	account        *testing.Account
	issuer         *testing.Account
	credentialType string
	fee            uint64
	sequence       *uint32
	flags          uint32
}

// CredentialAccept creates a new CredentialAcceptBuilder.
// The account (subject) accepts a credential issued by the issuer.
func CredentialAccept(account, issuer *testing.Account, credentialType string) *CredentialAcceptBuilder {
	return &CredentialAcceptBuilder{
		account:        account,
		issuer:         issuer,
		credentialType: credentialType,
		fee:            10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *CredentialAcceptBuilder) Fee(f uint64) *CredentialAcceptBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *CredentialAcceptBuilder) Sequence(seq uint32) *CredentialAcceptBuilder {
	b.sequence = &seq
	return b
}

// Flags sets transaction flags explicitly.
func (b *CredentialAcceptBuilder) Flags(flags uint32) *CredentialAcceptBuilder {
	b.flags = flags
	return b
}

// Build constructs the CredentialAccept transaction.
func (b *CredentialAcceptBuilder) Build() tx.Transaction {
	// Hex-encode the credential type if not already hex
	credType := b.credentialType
	if !isHexEncoded(credType) {
		credType = hex.EncodeToString([]byte(credType))
	}

	c := credential.NewCredentialAccept(b.account.Address, b.issuer.Address, credType)
	c.Fee = fmt.Sprintf("%d", b.fee)

	if b.sequence != nil {
		c.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		c.SetFlags(b.flags)
	}

	return c
}

// BuildCredentialAccept is a convenience method that returns the concrete *credential.CredentialAccept type.
func (b *CredentialAcceptBuilder) BuildCredentialAccept() *credential.CredentialAccept {
	return b.Build().(*credential.CredentialAccept)
}

// CredentialDeleteBuilder provides a fluent interface for building CredentialDelete transactions.
type CredentialDeleteBuilder struct {
	account        *testing.Account
	subject        *testing.Account
	issuer         *testing.Account
	credentialType string
	fee            uint64
	sequence       *uint32
	flags          uint32
}

// CredentialDelete creates a new CredentialDeleteBuilder.
// The account submits the delete. Subject and issuer identify the credential.
func CredentialDelete(account, subject, issuer *testing.Account, credentialType string) *CredentialDeleteBuilder {
	return &CredentialDeleteBuilder{
		account:        account,
		subject:        subject,
		issuer:         issuer,
		credentialType: credentialType,
		fee:            10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *CredentialDeleteBuilder) Fee(f uint64) *CredentialDeleteBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *CredentialDeleteBuilder) Sequence(seq uint32) *CredentialDeleteBuilder {
	b.sequence = &seq
	return b
}

// Flags sets transaction flags explicitly.
func (b *CredentialDeleteBuilder) Flags(flags uint32) *CredentialDeleteBuilder {
	b.flags = flags
	return b
}

// Build constructs the CredentialDelete transaction.
func (b *CredentialDeleteBuilder) Build() tx.Transaction {
	// Hex-encode the credential type if not already hex
	credType := b.credentialType
	if !isHexEncoded(credType) {
		credType = hex.EncodeToString([]byte(credType))
	}

	c := credential.NewCredentialDelete(b.account.Address, credType)
	c.Fee = fmt.Sprintf("%d", b.fee)

	if b.subject != nil {
		c.Subject = b.subject.Address
	}
	if b.issuer != nil {
		c.Issuer = b.issuer.Address
	}
	if b.sequence != nil {
		c.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		c.SetFlags(b.flags)
	}

	return c
}

// BuildCredentialDelete is a convenience method that returns the concrete *credential.CredentialDelete type.
func (b *CredentialDeleteBuilder) BuildCredentialDelete() *credential.CredentialDelete {
	return b.Build().(*credential.CredentialDelete)
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
