package accountset

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	accounttx "github.com/LeJamon/goXRPLd/internal/core/tx/account"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

// AccountSetBuilder provides a fluent interface for building AccountSet transactions.
type AccountSetBuilder struct {
	account              *testing.Account
	setFlag              *uint32
	clearFlag            *uint32
	domain               string
	domainPresent        bool
	emailHash            string
	emailHashPresent     bool
	messageKey           string
	messageKeyPresent    bool
	walletLocator        string
	walletLocatorPresent bool
	transferRate         *uint32
	tickSize             *uint8
	nfTokenMinter        string
	fee                  uint64
	sequence             *uint32
	flags                uint32
}

// AccountSet creates a new AccountSetBuilder.
func AccountSet(account *testing.Account) *AccountSetBuilder {
	return &AccountSetBuilder{
		account: account,
		fee:     10, // Default fee: 10 drops
	}
}

// SetFlag sets a flag to enable on the account.
func (b *AccountSetBuilder) SetFlag(flag uint32) *AccountSetBuilder {
	b.setFlag = &flag
	return b
}

// ClearFlag sets a flag to disable on the account.
func (b *AccountSetBuilder) ClearFlag(flag uint32) *AccountSetBuilder {
	b.clearFlag = &flag
	return b
}

// Domain sets the domain for the account (as hex-encoded string).
// Pass empty string to clear the domain field.
func (b *AccountSetBuilder) Domain(domain string) *AccountSetBuilder {
	b.domain = domain
	b.domainPresent = true
	return b
}

// EmailHash sets the email hash (128-bit MD5 hash as hex string).
// Pass empty string to clear the field.
func (b *AccountSetBuilder) EmailHash(hash string) *AccountSetBuilder {
	b.emailHash = hash
	b.emailHashPresent = true
	return b
}

// MessageKey sets the message key (public key as hex string).
// Pass empty string to clear the field.
func (b *AccountSetBuilder) MessageKey(key string) *AccountSetBuilder {
	b.messageKey = key
	b.messageKeyPresent = true
	return b
}

// WalletLocator sets the wallet locator (256-bit hash as hex string).
// Pass empty string to clear the field.
func (b *AccountSetBuilder) WalletLocator(locator string) *AccountSetBuilder {
	b.walletLocator = locator
	b.walletLocatorPresent = true
	return b
}

// TxFlags sets the transaction-level flags (e.g., tfAllowXRP, tfOptionalAuth).
// These are different from SetFlag/ClearFlag which control account-level flags.
func (b *AccountSetBuilder) TxFlags(flags uint32) *AccountSetBuilder {
	b.flags = flags
	return b
}

// TransferRate sets the transfer rate (1e9 = 100%, 1.005e9 = 100.5%).
func (b *AccountSetBuilder) TransferRate(rate uint32) *AccountSetBuilder {
	b.transferRate = &rate
	return b
}

// TickSize sets the tick size for offers (0, 3-15).
func (b *AccountSetBuilder) TickSize(size uint8) *AccountSetBuilder {
	b.tickSize = &size
	return b
}

// Fee sets the transaction fee in drops.
func (b *AccountSetBuilder) Fee(f uint64) *AccountSetBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *AccountSetBuilder) Sequence(seq uint32) *AccountSetBuilder {
	b.sequence = &seq
	return b
}

// RequireDest enables the require destination tag flag.
func (b *AccountSetBuilder) RequireDest() *AccountSetBuilder {
	flag := accounttx.AccountSetFlagRequireDest
	b.setFlag = &flag
	return b
}

// RequireAuth enables the require authorization flag.
func (b *AccountSetBuilder) RequireAuth() *AccountSetBuilder {
	flag := accounttx.AccountSetFlagRequireAuth
	b.setFlag = &flag
	return b
}

// DisallowXRP enables the disallow XRP flag.
func (b *AccountSetBuilder) DisallowXRP() *AccountSetBuilder {
	flag := accounttx.AccountSetFlagDisallowXRP
	b.setFlag = &flag
	return b
}

// DefaultRipple enables the default ripple flag.
func (b *AccountSetBuilder) DefaultRipple() *AccountSetBuilder {
	flag := accounttx.AccountSetFlagDefaultRipple
	b.setFlag = &flag
	return b
}

// DepositAuth enables the deposit authorization flag.
func (b *AccountSetBuilder) DepositAuth() *AccountSetBuilder {
	flag := accounttx.AccountSetFlagDepositAuth
	b.setFlag = &flag
	return b
}

// NoFreeze enables the no freeze flag (cannot be disabled once set).
func (b *AccountSetBuilder) NoFreeze() *AccountSetBuilder {
	flag := accounttx.AccountSetFlagNoFreeze
	b.setFlag = &flag
	return b
}

// GlobalFreeze enables the global freeze flag.
func (b *AccountSetBuilder) GlobalFreeze() *AccountSetBuilder {
	flag := accounttx.AccountSetFlagGlobalFreeze
	b.setFlag = &flag
	return b
}

// AllowClawback enables the clawback flag (cannot be disabled once set).
func (b *AccountSetBuilder) AllowClawback() *AccountSetBuilder {
	flag := accounttx.AccountSetFlagAllowTrustLineClawback
	b.setFlag = &flag
	return b
}

// AuthorizedMinter sets the account as an authorized NFToken minter.
// Reference: rippled's token::setMinter(account, minter).
func (b *AccountSetBuilder) AuthorizedMinter(minter *testing.Account) *AccountSetBuilder {
	flag := accounttx.AccountSetFlagAuthorizedNFTokenMinter
	b.setFlag = &flag
	b.nfTokenMinter = minter.Address
	return b
}

// ClearMinter clears the authorized NFToken minter.
func (b *AccountSetBuilder) ClearMinter() *AccountSetBuilder {
	flag := accounttx.AccountSetFlagAuthorizedNFTokenMinter
	b.clearFlag = &flag
	b.nfTokenMinter = ""
	return b
}

// Build constructs the AccountSet transaction.
func (b *AccountSetBuilder) Build() tx.Transaction {
	as := accounttx.NewAccountSet(b.account.Address)
	as.Fee = fmt.Sprintf("%d", b.fee)

	if b.setFlag != nil {
		as.SetFlag = b.setFlag
	}
	if b.clearFlag != nil {
		as.ClearFlag = b.clearFlag
	}
	if b.domainPresent {
		d := b.domain
		as.Domain = &d
	}
	if b.emailHashPresent {
		if b.emailHash == "" {
			// Clearing: send all-zeros hash (128-bit)
			as.EmailHash = "00000000000000000000000000000000"
		} else {
			as.EmailHash = b.emailHash
		}
	}
	if b.messageKeyPresent {
		mk := b.messageKey
		as.MessageKey = &mk
	}
	if b.walletLocatorPresent {
		if b.walletLocator == "" {
			// Clearing: send all-zeros hash (256-bit)
			as.WalletLocator = "0000000000000000000000000000000000000000000000000000000000000000"
		} else {
			as.WalletLocator = b.walletLocator
		}
	}
	if b.transferRate != nil {
		as.TransferRate = b.transferRate
	}
	if b.tickSize != nil {
		as.TickSize = b.tickSize
	}
	if b.nfTokenMinter != "" {
		as.NFTokenMinter = b.nfTokenMinter
	}
	if b.sequence != nil {
		as.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		as.SetFlags(b.flags)
	}

	return as
}

// BuildAccountSet is a convenience method that returns the concrete *accounttx.AccountSet type.
func (b *AccountSetBuilder) BuildAccountSet() *accounttx.AccountSet {
	return b.Build().(*accounttx.AccountSet)
}
