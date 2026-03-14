package clawback

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/clawback"
)

// ClawbackBuilder provides a fluent interface for building Clawback transactions.
type ClawbackBuilder struct {
	issuer   *testing.Account
	holder   *testing.Account
	currency string
	amount   float64
	fee      uint64
	sequence *uint32
	flags    uint32
}

// Claw creates a new ClawbackBuilder.
// issuer is the token issuer performing the clawback.
// holder is the token holder from whom tokens are clawed back.
// currency is the currency code (e.g. "USD").
// amount is the amount to claw back (positive).
// Matches rippled's claw(alice, bob["USD"](200)).
func Claw(issuer, holder *testing.Account, currency string, amount float64) *ClawbackBuilder {
	return &ClawbackBuilder{
		issuer:   issuer,
		holder:   holder,
		currency: currency,
		amount:   amount,
		fee:      10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *ClawbackBuilder) Fee(f uint64) *ClawbackBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *ClawbackBuilder) Sequence(seq uint32) *ClawbackBuilder {
	b.sequence = &seq
	return b
}

// Flags sets the transaction flags.
func (b *ClawbackBuilder) Flags(flags uint32) *ClawbackBuilder {
	b.flags = flags
	return b
}

// Build constructs the Clawback transaction.
func (b *ClawbackBuilder) Build() tx.Transaction {
	// For IOU clawback, the Amount.Issuer field is the holder (not the issuer).
	// The transaction Account is the issuer.
	amount := tx.NewIssuedAmountFromFloat64(b.amount, b.currency, b.holder.Address)
	cb := clawback.NewClawback(b.issuer.Address, amount)
	cb.Fee = fmt.Sprintf("%d", b.fee)

	if b.sequence != nil {
		cb.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		cb.SetFlags(b.flags)
	}

	return cb
}

// BuildClawback is a convenience method that returns the concrete *clawback.Clawback type.
func (b *ClawbackBuilder) BuildClawback() *clawback.Clawback {
	return b.Build().(*clawback.Clawback)
}
