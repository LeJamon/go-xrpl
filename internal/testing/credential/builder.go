package credential

import (
	"encoding/hex"
	"strconv"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
)

const defaultCredentialFee uint64 = 10

type credentialBuilderOptions struct {
	credentialType string
	fee            uint64
	flags          uint32
}

func newCredentialBuilderOptions(credentialType string) credentialBuilderOptions {
	return credentialBuilderOptions{
		credentialType: credentialType,
		fee:            defaultCredentialFee,
	}
}

func (o *credentialBuilderOptions) apply(common *tx.Common) {
	common.Fee = strconv.FormatUint(o.fee, 10)
	if o.flags != 0 {
		common.SetFlags(o.flags)
	}
}

// CredentialCreateBuilder provides a fluent interface for building CredentialCreate transactions.
type CredentialCreateBuilder struct {
	credentialBuilderOptions
	account    *jtx.Account
	subject    *jtx.Account
	uri        string
	uriIsHex   bool
	uriSet     bool
	expiration *uint32
}

func CredentialCreateText(account, subject *jtx.Account, credentialType string) *CredentialCreateBuilder {
	return CredentialCreateBytes(account, subject, []byte(credentialType))
}

func CredentialCreateBytes(account, subject *jtx.Account, credentialType []byte) *CredentialCreateBuilder {
	return CredentialCreateHex(account, subject, hex.EncodeToString(credentialType))
}

func CredentialCreateHex(account, subject *jtx.Account, credentialTypeHex string) *CredentialCreateBuilder {
	return &CredentialCreateBuilder{
		credentialBuilderOptions: newCredentialBuilderOptions(credentialTypeHex),
		account:                  account,
		subject:                  subject,
	}
}

// URI sets the URI for the credential.
// The URI will be hex-encoded when building the transaction.
func (b *CredentialCreateBuilder) URI(uri string) *CredentialCreateBuilder {
	b.uri = uri
	b.uriIsHex = false
	b.uriSet = true
	return b
}

// URIHex sets the URI from an already hex-encoded string.
func (b *CredentialCreateBuilder) URIHex(uriHex string) *CredentialCreateBuilder {
	b.uri = uriHex
	b.uriIsHex = true
	b.uriSet = true
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

// Flags sets transaction flags explicitly.
func (b *CredentialCreateBuilder) Flags(flags uint32) *CredentialCreateBuilder {
	b.flags = flags
	return b
}

// Build constructs the CredentialCreate transaction.
func (b *CredentialCreateBuilder) Build() *credential.CredentialCreate {
	c := credential.NewCredentialCreate(b.account.Address, b.subject.Address, b.credentialType)
	b.credentialBuilderOptions.apply(c.GetCommon())

	if b.uriSet {
		c.URI = b.uri
		if !b.uriIsHex {
			c.URI = hex.EncodeToString([]byte(b.uri))
		}
		c.Common.SetPresentFields(map[string]bool{"URI": true})
	}
	if b.expiration != nil {
		c.Expiration = b.expiration
	}
	return c
}

// CredentialAcceptBuilder provides a fluent interface for building CredentialAccept transactions.
type CredentialAcceptBuilder struct {
	credentialBuilderOptions
	account *jtx.Account
	issuer  *jtx.Account
}

func CredentialAcceptText(account, issuer *jtx.Account, credentialType string) *CredentialAcceptBuilder {
	return CredentialAcceptBytes(account, issuer, []byte(credentialType))
}

func CredentialAcceptBytes(account, issuer *jtx.Account, credentialType []byte) *CredentialAcceptBuilder {
	return CredentialAcceptHex(account, issuer, hex.EncodeToString(credentialType))
}

func CredentialAcceptHex(account, issuer *jtx.Account, credentialTypeHex string) *CredentialAcceptBuilder {
	return &CredentialAcceptBuilder{
		credentialBuilderOptions: newCredentialBuilderOptions(credentialTypeHex),
		account:                  account,
		issuer:                   issuer,
	}
}

// Fee sets the transaction fee in drops.
func (b *CredentialAcceptBuilder) Fee(f uint64) *CredentialAcceptBuilder {
	b.fee = f
	return b
}

// Flags sets transaction flags explicitly.
func (b *CredentialAcceptBuilder) Flags(flags uint32) *CredentialAcceptBuilder {
	b.flags = flags
	return b
}

// Build constructs the CredentialAccept transaction.
func (b *CredentialAcceptBuilder) Build() *credential.CredentialAccept {
	c := credential.NewCredentialAccept(b.account.Address, b.issuer.Address, b.credentialType)
	b.credentialBuilderOptions.apply(c.GetCommon())

	return c
}

// CredentialDeleteBuilder provides a fluent interface for building CredentialDelete transactions.
type CredentialDeleteBuilder struct {
	credentialBuilderOptions
	account *jtx.Account
	subject *jtx.Account
	issuer  *jtx.Account
}

func CredentialDeleteText(account, subject, issuer *jtx.Account, credentialType string) *CredentialDeleteBuilder {
	return CredentialDeleteBytes(account, subject, issuer, []byte(credentialType))
}

func CredentialDeleteBytes(account, subject, issuer *jtx.Account, credentialType []byte) *CredentialDeleteBuilder {
	return CredentialDeleteHex(account, subject, issuer, hex.EncodeToString(credentialType))
}

func CredentialDeleteHex(account, subject, issuer *jtx.Account, credentialTypeHex string) *CredentialDeleteBuilder {
	return &CredentialDeleteBuilder{
		credentialBuilderOptions: newCredentialBuilderOptions(credentialTypeHex),
		account:                  account,
		subject:                  subject,
		issuer:                   issuer,
	}
}

// Flags sets transaction flags explicitly.
func (b *CredentialDeleteBuilder) Flags(flags uint32) *CredentialDeleteBuilder {
	b.flags = flags
	return b
}

// Build constructs the CredentialDelete transaction.
func (b *CredentialDeleteBuilder) Build() *credential.CredentialDelete {
	c := credential.NewCredentialDelete(b.account.Address, b.credentialType)
	b.credentialBuilderOptions.apply(c.GetCommon())

	if b.subject != nil {
		c.Subject = b.subject.Address
	}
	if b.issuer != nil {
		c.Issuer = b.issuer.Address
	}
	return c
}
