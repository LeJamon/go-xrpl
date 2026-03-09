package trustset

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/tx"
	trustsettx "github.com/LeJamon/goXRPLd/internal/tx/trustset"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

// TrustSetBuilder provides a fluent interface for building TrustSet transactions.
type TrustSetBuilder struct {
	account     *testing.Account
	limitAmount tx.Amount
	qualityIn   *uint32
	qualityOut  *uint32
	fee         uint64
	sequence    *uint32
	flags       uint32
}

// TrustSet creates a new TrustSetBuilder.
// The limitAmount specifies the currency, issuer, and maximum balance to trust.
func TrustSet(account *testing.Account, limitAmount tx.Amount) *TrustSetBuilder {
	return &TrustSetBuilder{
		account:     account,
		limitAmount: limitAmount,
		fee:         10, // Default fee: 10 drops
	}
}

// TrustLine is a convenience function to create a trust line builder.
// limit is the maximum amount the account will trust the issuer for.
func TrustLine(account *testing.Account, currency string, issuer *testing.Account, limit string) *TrustSetBuilder {
	// Parse limit as float and convert to issued amount
	var limitFloat float64
	fmt.Sscanf(limit, "%f", &limitFloat)
	limitAmount := tx.NewIssuedAmountFromFloat64(limitFloat, currency, issuer.Address)
	return TrustSet(account, limitAmount)
}

// TrustUSD creates a trust line for USD with the specified issuer and limit.
func TrustUSD(account *testing.Account, issuer *testing.Account, limit string) *TrustSetBuilder {
	return TrustLine(account, "USD", issuer, limit)
}

// TrustEUR creates a trust line for EUR with the specified issuer and limit.
func TrustEUR(account *testing.Account, issuer *testing.Account, limit string) *TrustSetBuilder {
	return TrustLine(account, "EUR", issuer, limit)
}

// TrustBTC creates a trust line for BTC with the specified issuer and limit.
func TrustBTC(account *testing.Account, issuer *testing.Account, limit string) *TrustSetBuilder {
	return TrustLine(account, "BTC", issuer, limit)
}

// QualityIn sets the incoming quality factor.
// Value of 1e9 (1,000,000,000) represents 1:1 ratio.
// A value of 1.01e9 means incoming payments are worth 1% more.
func (b *TrustSetBuilder) QualityIn(quality uint32) *TrustSetBuilder {
	b.qualityIn = &quality
	return b
}

// QualityOut sets the outgoing quality factor.
// Value of 1e9 (1,000,000,000) represents 1:1 ratio.
// A value of 0.99e9 means outgoing payments are worth 1% less.
func (b *TrustSetBuilder) QualityOut(quality uint32) *TrustSetBuilder {
	b.qualityOut = &quality
	return b
}

// Fee sets the transaction fee in drops.
func (b *TrustSetBuilder) Fee(f uint64) *TrustSetBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *TrustSetBuilder) Sequence(seq uint32) *TrustSetBuilder {
	b.sequence = &seq
	return b
}

// SetAuth authorizes the other party to hold the currency.
// This is required when the issuer has RequireAuth enabled.
func (b *TrustSetBuilder) SetAuth() *TrustSetBuilder {
	b.flags |= trustsettx.TrustSetFlagSetfAuth
	return b
}

// Auth is an alias for SetAuth for convenience.
func (b *TrustSetBuilder) Auth() *TrustSetBuilder {
	return b.SetAuth()
}

// NoRipple blocks rippling on this trust line.
// Rippling allows balance to flow through the account for payments.
func (b *TrustSetBuilder) NoRipple() *TrustSetBuilder {
	b.flags |= trustsettx.TrustSetFlagSetNoRipple
	return b
}

// ClearNoRipple removes the no-ripple flag, allowing rippling.
func (b *TrustSetBuilder) ClearNoRipple() *TrustSetBuilder {
	b.flags |= trustsettx.TrustSetFlagClearNoRipple
	return b
}

// Freeze freezes this trust line.
// The issuer can freeze the holder's balance.
func (b *TrustSetBuilder) Freeze() *TrustSetBuilder {
	b.flags |= trustsettx.TrustSetFlagSetFreeze
	return b
}

// ClearFreeze removes the freeze from this trust line.
func (b *TrustSetBuilder) ClearFreeze() *TrustSetBuilder {
	b.flags |= trustsettx.TrustSetFlagClearFreeze
	return b
}

// Build constructs the TrustSet transaction.
func (b *TrustSetBuilder) Build() tx.Transaction {
	ts := trustsettx.NewTrustSet(b.account.Address, b.limitAmount)
	ts.Fee = fmt.Sprintf("%d", b.fee)

	if b.qualityIn != nil {
		ts.QualityIn = b.qualityIn
	}
	if b.qualityOut != nil {
		ts.QualityOut = b.qualityOut
	}
	if b.sequence != nil {
		ts.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		ts.SetFlags(b.flags)
	}

	return ts
}

// BuildTrustSet is a convenience method that returns the concrete *trustsettx.TrustSet type.
func (b *TrustSetBuilder) BuildTrustSet() *trustsettx.TrustSet {
	return b.Build().(*trustsettx.TrustSet)
}

// Quality constant for 1:1 ratio (no premium or discount).
const QualityParity uint32 = 1_000_000_000

// QualityFromPercentage calculates a quality value from a percentage.
// 100 means 1:1 (parity), 101 means 1% premium, 99 means 1% discount.
func QualityFromPercentage(percentage float64) uint32 {
	return uint32(percentage / 100 * float64(QualityParity))
}
