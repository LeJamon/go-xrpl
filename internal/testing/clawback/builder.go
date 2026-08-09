package clawback

import (
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/clawback"
)

type clawbackBuilder struct {
	issuer *jtx.Account
	amount tx.Amount
	flags  uint32
}

// Claw creates an IOU clawback with an exact integer amount.
func Claw(issuer, holder *jtx.Account, currency string, amount int64) *clawbackBuilder {
	return ClawAmount(issuer, holder, tx.NewIssuedAmount(amount, 0, currency, holder.Address))
}

func ClawAmount(issuer, holder *jtx.Account, amount tx.Amount) *clawbackBuilder {
	amount.Issuer = holder.Address
	return &clawbackBuilder{issuer: issuer, amount: amount}
}

// Flags sets the transaction flags.
func (b *clawbackBuilder) Flags(flags uint32) *clawbackBuilder {
	b.flags = flags
	return b
}

func (b *clawbackBuilder) Build() *clawback.Clawback {
	cb := clawback.NewClawback(b.issuer.Address, b.amount)
	if b.flags != 0 {
		cb.SetFlags(b.flags)
	}
	return cb
}
