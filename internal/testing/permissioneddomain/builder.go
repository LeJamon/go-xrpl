package permissioneddomain

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/permissioneddomain"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
)

// DomainSetBuilder provides a fluent interface for building PermissionedDomainSet transactions.
type DomainSetBuilder struct {
	account  *jtx.Account
	domainID string
	creds    []permissioneddomain.AcceptedCredential
	fee      uint64
	feeStr   string
	flags    uint32
}

// DomainSet creates a new DomainSetBuilder.
func DomainSet(account *jtx.Account) *DomainSetBuilder {
	return &DomainSetBuilder{
		account: account,
		fee:     10,
	}
}

// DomainID sets the DomainID field (for update; omit for creation).
func (b *DomainSetBuilder) DomainID(domainID string) *DomainSetBuilder {
	b.domainID = domainID
	return b
}

// Credential adds an accepted credential (issuer XRPL address, credentialType hex string).
func (b *DomainSetBuilder) Credential(issuer *jtx.Account, credentialType string) *DomainSetBuilder {
	b.creds = append(b.creds, permissioneddomain.AcceptedCredential{
		Credential: permissioneddomain.AcceptedCredentialData{
			Issuer:         issuer.Address,
			CredentialType: credentialType,
		},
	})
	return b
}

// Fee sets the transaction fee in drops.
func (b *DomainSetBuilder) Fee(f uint64) *DomainSetBuilder {
	b.fee = f
	return b
}

// BadFee sets an invalid fee ("-1") to trigger temBAD_FEE.
func (b *DomainSetBuilder) BadFee() *DomainSetBuilder {
	b.feeStr = "-1"
	return b
}

// Flags sets transaction flags.
func (b *DomainSetBuilder) Flags(flags uint32) *DomainSetBuilder {
	b.flags = flags
	return b
}

// Build constructs the PermissionedDomainSet transaction.
func (b *DomainSetBuilder) Build() tx.Transaction {
	d := permissioneddomain.NewPermissionedDomainSet(b.account.Address)
	if b.feeStr != "" {
		d.Fee = b.feeStr
	} else {
		d.Fee = fmt.Sprintf("%d", b.fee)
	}
	if b.domainID != "" {
		d.DomainID = b.domainID
	}
	d.AcceptedCredentials = b.creds
	if b.flags != 0 {
		d.SetFlags(b.flags)
	}
	return d
}

// DomainDeleteBuilder provides a fluent interface for building PermissionedDomainDelete transactions.
type DomainDeleteBuilder struct {
	account  *jtx.Account
	domainID string
	fee      uint64
	feeStr   string
	flags    uint32
}

// DomainDelete creates a new DomainDeleteBuilder.
func DomainDelete(account *jtx.Account, domainID string) *DomainDeleteBuilder {
	return &DomainDeleteBuilder{
		account:  account,
		domainID: domainID,
		fee:      10,
	}
}

// Fee sets the transaction fee in drops.
func (b *DomainDeleteBuilder) Fee(f uint64) *DomainDeleteBuilder {
	b.fee = f
	return b
}

// BadFee sets an invalid fee ("-1") to trigger temBAD_FEE.
func (b *DomainDeleteBuilder) BadFee() *DomainDeleteBuilder {
	b.feeStr = "-1"
	return b
}

// Flags sets transaction flags.
func (b *DomainDeleteBuilder) Flags(flags uint32) *DomainDeleteBuilder {
	b.flags = flags
	return b
}

// Build constructs the PermissionedDomainDelete transaction.
func (b *DomainDeleteBuilder) Build() tx.Transaction {
	d := permissioneddomain.NewPermissionedDomainDelete(b.account.Address, b.domainID)
	if b.feeStr != "" {
		d.Fee = b.feeStr
	} else {
		d.Fee = fmt.Sprintf("%d", b.fee)
	}
	if b.flags != 0 {
		d.SetFlags(b.flags)
	}
	return d
}
