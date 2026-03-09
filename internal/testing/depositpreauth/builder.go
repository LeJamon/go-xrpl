// Package depositpreauth provides fluent transaction builder helpers for
// DepositPreauth testing, plus integration tests matching rippled's
// DepositAuth_test.cpp and DepositPreauth_test sections.
//
// Reference: rippled/src/test/jtx/deposit.h and deposit.cpp
package depositpreauth

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/depositpreauth"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

// AuthorizeCredentials describes a single credential requirement for
// credential-based deposit preauthorization.
// Reference: rippled jtx::deposit::AuthorizeCredentials
type AuthorizeCredentials struct {
	Issuer   *testing.Account
	CredType string // raw credential type (will be hex-encoded if needed)
}

// -----------------------------------------------------------------------
// AuthBuilder – builds DepositPreauth with Authorize field set.
// Reference: rippled deposit::auth(account, auth)
// -----------------------------------------------------------------------

// AuthBuilder provides a fluent interface for building a DepositPreauth
// transaction that authorizes an account.
type AuthBuilder struct {
	owner      *testing.Account
	authorized *testing.Account
	fee        uint64
	sequence   *uint32
	flags      *uint32
	ticketSeq  *uint32
}

// Auth creates a new AuthBuilder for deposit preauthorization.
func Auth(owner, authorized *testing.Account) *AuthBuilder {
	return &AuthBuilder{
		owner:      owner,
		authorized: authorized,
		fee:        10,
	}
}

func (b *AuthBuilder) Fee(f uint64) *AuthBuilder          { b.fee = f; return b }
func (b *AuthBuilder) Sequence(seq uint32) *AuthBuilder    { b.sequence = &seq; return b }
func (b *AuthBuilder) Flags(flags uint32) *AuthBuilder     { b.flags = &flags; return b }
func (b *AuthBuilder) TicketSeq(seq uint32) *AuthBuilder   { b.ticketSeq = &seq; return b }

// Build constructs the DepositPreauth transaction.
func (b *AuthBuilder) Build() tx.Transaction {
	dp := depositpreauth.NewDepositPreauth(b.owner.Address)
	dp.SetAuthorize(b.authorized.Address)
	dp.Fee = fmt.Sprintf("%d", b.fee)
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

// BuildDepositPreauth returns the concrete *depositpreauth.DepositPreauth type.
func (b *AuthBuilder) BuildDepositPreauth() *depositpreauth.DepositPreauth {
	return b.Build().(*depositpreauth.DepositPreauth)
}

// -----------------------------------------------------------------------
// UnauthBuilder – builds DepositPreauth with Unauthorize field set.
// Reference: rippled deposit::unauth(account, unauth)
// -----------------------------------------------------------------------

// UnauthBuilder provides a fluent interface for building a DepositPreauth
// transaction that removes authorization for an account.
type UnauthBuilder struct {
	owner        *testing.Account
	unauthorized *testing.Account
	fee          uint64
	sequence     *uint32
	flags        *uint32
	ticketSeq    *uint32
}

// Unauth creates a new UnauthBuilder for removing deposit preauthorization.
func Unauth(owner, unauthorized *testing.Account) *UnauthBuilder {
	return &UnauthBuilder{
		owner:        owner,
		unauthorized: unauthorized,
		fee:          10,
	}
}

func (b *UnauthBuilder) Fee(f uint64) *UnauthBuilder        { b.fee = f; return b }
func (b *UnauthBuilder) Sequence(seq uint32) *UnauthBuilder  { b.sequence = &seq; return b }
func (b *UnauthBuilder) Flags(flags uint32) *UnauthBuilder   { b.flags = &flags; return b }
func (b *UnauthBuilder) TicketSeq(seq uint32) *UnauthBuilder { b.ticketSeq = &seq; return b }

// Build constructs the DepositPreauth (unauthorize) transaction.
func (b *UnauthBuilder) Build() tx.Transaction {
	dp := depositpreauth.NewDepositPreauth(b.owner.Address)
	dp.SetUnauthorize(b.unauthorized.Address)
	dp.Fee = fmt.Sprintf("%d", b.fee)
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

// BuildDepositPreauth returns the concrete *depositpreauth.DepositPreauth type.
func (b *UnauthBuilder) BuildDepositPreauth() *depositpreauth.DepositPreauth {
	return b.Build().(*depositpreauth.DepositPreauth)
}

// -----------------------------------------------------------------------
// AuthCredentialsBuilder – builds DepositPreauth with AuthorizeCredentials.
// Reference: rippled deposit::authCredentials(account, credentials)
// -----------------------------------------------------------------------

// AuthCredentialsBuilder provides a fluent interface for building a
// DepositPreauth transaction that authorizes deposits using credentials.
type AuthCredentialsBuilder struct {
	owner       *testing.Account
	credentials []AuthorizeCredentials
	fee         uint64
	sequence    *uint32
	flags       *uint32
}

// AuthCredentials creates a new AuthCredentialsBuilder.
func AuthCredentials(owner *testing.Account, credentials []AuthorizeCredentials) *AuthCredentialsBuilder {
	return &AuthCredentialsBuilder{
		owner:       owner,
		credentials: credentials,
		fee:         10,
	}
}

func (b *AuthCredentialsBuilder) Fee(f uint64) *AuthCredentialsBuilder       { b.fee = f; return b }
func (b *AuthCredentialsBuilder) Sequence(seq uint32) *AuthCredentialsBuilder { b.sequence = &seq; return b }
func (b *AuthCredentialsBuilder) Flags(flags uint32) *AuthCredentialsBuilder  { b.flags = &flags; return b }

// Build constructs the DepositPreauth transaction with AuthorizeCredentials.
func (b *AuthCredentialsBuilder) Build() tx.Transaction {
	dp := depositpreauth.NewDepositPreauth(b.owner.Address)
	dp.Fee = fmt.Sprintf("%d", b.fee)

	wrappers := make([]depositpreauth.CredentialWrapper, len(b.credentials))
	for i, c := range b.credentials {
		credType := c.CredType
		if !isHexEncoded(credType) {
			credType = hex.EncodeToString([]byte(credType))
		}
		wrappers[i] = depositpreauth.CredentialWrapper{
			Credential: depositpreauth.CredentialSpec{
				Issuer:         c.Issuer.Address,
				CredentialType: credType,
			},
		}
	}
	dp.AuthorizeCredentials = wrappers

	if b.sequence != nil {
		dp.SetSequence(*b.sequence)
	}
	if b.flags != nil {
		dp.SetFlags(*b.flags)
	}
	return dp
}

// BuildDepositPreauth returns the concrete *depositpreauth.DepositPreauth type.
func (b *AuthCredentialsBuilder) BuildDepositPreauth() *depositpreauth.DepositPreauth {
	return b.Build().(*depositpreauth.DepositPreauth)
}

// -----------------------------------------------------------------------
// UnauthCredentialsBuilder – builds DepositPreauth with UnauthorizeCredentials.
// Reference: rippled deposit::unauthCredentials(account, credentials)
// -----------------------------------------------------------------------

// UnauthCredentialsBuilder provides a fluent interface for building a
// DepositPreauth transaction that removes credential-based authorization.
type UnauthCredentialsBuilder struct {
	owner       *testing.Account
	credentials []AuthorizeCredentials
	fee         uint64
	sequence    *uint32
	flags       *uint32
}

// UnauthCredentials creates a new UnauthCredentialsBuilder.
func UnauthCredentials(owner *testing.Account, credentials []AuthorizeCredentials) *UnauthCredentialsBuilder {
	return &UnauthCredentialsBuilder{
		owner:       owner,
		credentials: credentials,
		fee:         10,
	}
}

func (b *UnauthCredentialsBuilder) Fee(f uint64) *UnauthCredentialsBuilder       { b.fee = f; return b }
func (b *UnauthCredentialsBuilder) Sequence(seq uint32) *UnauthCredentialsBuilder { b.sequence = &seq; return b }
func (b *UnauthCredentialsBuilder) Flags(flags uint32) *UnauthCredentialsBuilder  { b.flags = &flags; return b }

// Build constructs the DepositPreauth transaction with UnauthorizeCredentials.
func (b *UnauthCredentialsBuilder) Build() tx.Transaction {
	dp := depositpreauth.NewDepositPreauth(b.owner.Address)
	dp.Fee = fmt.Sprintf("%d", b.fee)

	wrappers := make([]depositpreauth.CredentialWrapper, len(b.credentials))
	for i, c := range b.credentials {
		credType := c.CredType
		if !isHexEncoded(credType) {
			credType = hex.EncodeToString([]byte(credType))
		}
		wrappers[i] = depositpreauth.CredentialWrapper{
			Credential: depositpreauth.CredentialSpec{
				Issuer:         c.Issuer.Address,
				CredentialType: credType,
			},
		}
	}
	dp.UnauthorizeCredentials = wrappers

	if b.sequence != nil {
		dp.SetSequence(*b.sequence)
	}
	if b.flags != nil {
		dp.SetFlags(*b.flags)
	}
	return dp
}

// BuildDepositPreauth returns the concrete *depositpreauth.DepositPreauth type.
func (b *UnauthCredentialsBuilder) BuildDepositPreauth() *depositpreauth.DepositPreauth {
	return b.Build().(*depositpreauth.DepositPreauth)
}

// -----------------------------------------------------------------------
// RawDepositPreauthBuilder – builds a raw DepositPreauth transaction
// allowing arbitrary field combinations for negative testing.
// -----------------------------------------------------------------------

// RawBuilder provides direct access to all DepositPreauth fields for
// constructing invalid transactions for negative testing.
type RawBuilder struct {
	dp *depositpreauth.DepositPreauth
}

// Raw creates a new RawBuilder from an account address.
func Raw(account string) *RawBuilder {
	return &RawBuilder{dp: depositpreauth.NewDepositPreauth(account)}
}

func (b *RawBuilder) Authorize(addr string) *RawBuilder                          { b.dp.Authorize = addr; return b }
func (b *RawBuilder) Unauthorize(addr string) *RawBuilder                        { b.dp.Unauthorize = addr; return b }
func (b *RawBuilder) Fee(f string) *RawBuilder                                   { b.dp.Fee = f; return b }
func (b *RawBuilder) Sequence(seq uint32) *RawBuilder                            { b.dp.Sequence = &seq; return b }
func (b *RawBuilder) Flags(flags uint32) *RawBuilder                             { b.dp.SetFlags(flags); return b }
func (b *RawBuilder) AuthorizeCredentials(c []depositpreauth.CredentialWrapper) *RawBuilder {
	b.dp.AuthorizeCredentials = c; return b
}
func (b *RawBuilder) UnauthorizeCredentials(c []depositpreauth.CredentialWrapper) *RawBuilder {
	b.dp.UnauthorizeCredentials = c; return b
}

// Build returns the DepositPreauth transaction.
func (b *RawBuilder) Build() *depositpreauth.DepositPreauth { return b.dp }

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

// CredentialIndex computes the ledger entry hash for a credential.
// This matches rippled's credentials::ledgerEntry index.
func CredentialIndex(subject, issuer *testing.Account, credType string) string {
	rawCredType := credType
	if isHexEncoded(credType) {
		decoded, err := hex.DecodeString(credType)
		if err == nil {
			rawCredType = string(decoded)
		}
	}
	k := keylet.Credential(subject.ID, issuer.ID, []byte(rawCredType))
	return fmt.Sprintf("%X", k.Key)
}

// DepositPreauthKeylet returns the keylet for a basic (account-based) deposit preauth.
func DepositPreauthKeylet(owner, authorized *testing.Account) keylet.Keylet {
	return keylet.DepositPreauth(owner.ID, authorized.ID)
}

// isHexEncoded checks if a string appears to be hex-encoded.
func isHexEncoded(s string) bool {
	if len(s)%2 != 0 || len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
