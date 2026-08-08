// Package depositpreauth provides fluent transaction builder helpers for
// DepositPreauth testing, plus integration tests matching rippled's
// DepositAuth_test.cpp and DepositPreauth_test sections.
//
// Reference: rippled/src/test/jtx/deposit.h and deposit.cpp
package depositpreauth

import (
	"encoding/hex"
	"fmt"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/depositpreauth"
	"github.com/LeJamon/go-xrpl/keylet"
)

type AuthorizeCredentials struct {
	Issuer       *jtx.Account
	CredTypeText string
	CredTypeHex  *string
}

func AuthorizeCredentialText(issuer *jtx.Account, credentialType string) AuthorizeCredentials {
	return AuthorizeCredentials{Issuer: issuer, CredTypeText: credentialType}
}

func AuthorizeCredentialBytes(issuer *jtx.Account, credentialType []byte) AuthorizeCredentials {
	return AuthorizeCredentialText(issuer, string(append([]byte(nil), credentialType...)))
}

func AuthorizeCredentialHex(issuer *jtx.Account, credentialTypeHex string) (AuthorizeCredentials, error) {
	if _, err := hex.DecodeString(credentialTypeHex); err != nil {
		return AuthorizeCredentials{}, fmt.Errorf("invalid credential type hex: %w", err)
	}
	encoded := credentialTypeHex
	return AuthorizeCredentials{Issuer: issuer, CredTypeHex: &encoded}, nil
}

type builderKind uint8

const (
	authorizeAccount builderKind = iota
	unauthorizeAccount
	authorizeCredentials
	unauthorizeCredentials
)

type Builder struct {
	kind        builderKind
	owner       *jtx.Account
	account     *jtx.Account
	credentials []AuthorizeCredentials
	fee         uint64
	sequence    *uint32
	flags       *uint32
	ticketSeq   *uint32
}

func Auth(owner, authorized *jtx.Account) *Builder {
	return &Builder{kind: authorizeAccount, owner: owner, account: authorized, fee: 10}
}

func Unauth(owner, unauthorized *jtx.Account) *Builder {
	return &Builder{kind: unauthorizeAccount, owner: owner, account: unauthorized, fee: 10}
}

func AuthCredentials(owner *jtx.Account, credentials []AuthorizeCredentials) *Builder {
	return &Builder{kind: authorizeCredentials, owner: owner, credentials: credentials, fee: 10}
}

func UnauthCredentials(owner *jtx.Account, credentials []AuthorizeCredentials) *Builder {
	return &Builder{kind: unauthorizeCredentials, owner: owner, credentials: credentials, fee: 10}
}

func (b *Builder) Fee(f uint64) *Builder {
	b.fee = f
	return b
}

func (b *Builder) Sequence(sequence uint32) *Builder {
	b.sequence = &sequence
	return b
}

func (b *Builder) Flags(flags uint32) *Builder {
	b.flags = &flags
	return b
}

func (b *Builder) TicketSeq(ticketSequence uint32) *Builder {
	b.ticketSeq = &ticketSequence
	return b
}

func (b *Builder) Build() *depositpreauth.DepositPreauth {
	dp := depositpreauth.NewDepositPreauth(b.owner.Address)
	dp.Fee = fmt.Sprintf("%d", b.fee)

	switch b.kind {
	case authorizeAccount:
		dp.SetAuthorize(b.account.Address)
	case unauthorizeAccount:
		dp.SetUnauthorize(b.account.Address)
	case authorizeCredentials:
		dp.AuthorizeCredentials = credentialWrappers(b.credentials)
	case unauthorizeCredentials:
		dp.UnauthorizeCredentials = credentialWrappers(b.credentials)
	}

	if b.sequence != nil {
		dp.SetSequence(*b.sequence)
	}
	if b.flags != nil {
		dp.SetFlags(*b.flags)
	}
	if b.ticketSeq != nil {
		zero := uint32(0)
		dp.Sequence = &zero
		dp.TicketSequence = b.ticketSeq
	}
	return dp
}

func credentialWrappers(credentials []AuthorizeCredentials) []depositpreauth.CredentialWrapper {
	wrapper := make([]depositpreauth.CredentialWrapper, len(credentials))
	for i, credential := range credentials {
		credentialType := hex.EncodeToString([]byte(credential.CredTypeText))
		if credential.CredTypeHex != nil {
			credentialType = *credential.CredTypeHex
		}
		wrapper[i] = depositpreauth.CredentialWrapper{
			Credential: depositpreauth.CredentialSpec{
				Issuer:         credential.Issuer.Address,
				CredentialType: credentialType,
			},
		}
	}
	return wrapper
}

// RawBuilder provides direct access to all DepositPreauth fields for
// constructing invalid transactions for negative testing.
type RawBuilder struct {
	dp *depositpreauth.DepositPreauth
}

// Raw creates a new RawBuilder from an account address.
func Raw(account string) *RawBuilder {
	return &RawBuilder{dp: depositpreauth.NewDepositPreauth(account)}
}

func (b *RawBuilder) Authorize(addr string) *RawBuilder   { b.dp.Authorize = addr; return b }
func (b *RawBuilder) Unauthorize(addr string) *RawBuilder { b.dp.Unauthorize = addr; return b }
func (b *RawBuilder) Fee(f string) *RawBuilder            { b.dp.Fee = f; return b }
func (b *RawBuilder) Sequence(seq uint32) *RawBuilder     { b.dp.Sequence = &seq; return b }
func (b *RawBuilder) Flags(flags uint32) *RawBuilder      { b.dp.SetFlags(flags); return b }
func (b *RawBuilder) AuthorizeCredentials(c []depositpreauth.CredentialWrapper) *RawBuilder {
	b.dp.AuthorizeCredentials = c
	return b
}
func (b *RawBuilder) UnauthorizeCredentials(c []depositpreauth.CredentialWrapper) *RawBuilder {
	b.dp.UnauthorizeCredentials = c
	return b
}

// Build returns the DepositPreauth transaction.
func (b *RawBuilder) Build() *depositpreauth.DepositPreauth { return b.dp }

func CredentialIndexHex(subject, issuer *jtx.Account, credentialType string) string {
	return CredentialBytesIndexHex(subject, issuer, []byte(credentialType))
}

func CredentialBytesIndexHex(subject, issuer *jtx.Account, credentialType []byte) string {
	k := keylet.Credential(subject.ID, issuer.ID, credentialType)
	return fmt.Sprintf("%X", k.Key)
}
