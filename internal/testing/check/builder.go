package check

import (
	"fmt"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	checktx "github.com/LeJamon/go-xrpl/internal/tx/check"
)

// GetCheckID computes the check ledger entry ID from the creator account and sequence.
// This matches rippled's getCheckIndex(account, sequence).
func GetCheckID(acc *jtx.Account, seq uint32) string {
	return jtx.CheckID(acc, seq)
}

// --- CheckCreateBuilder ---

// CheckCreateBuilder provides a fluent interface for building CheckCreate transactions.
type CheckCreateBuilder struct {
	common     commonFields
	from       *jtx.Account
	to         *jtx.Account
	sendMax    tx.Amount
	destTag    *uint32
	sourceTag  *uint32
	expiration *uint32
	invoiceID  string
}

// CheckCreate creates a new CheckCreateBuilder.
func CheckCreate(from, to *jtx.Account, sendMax tx.Amount) *CheckCreateBuilder {
	return &CheckCreateBuilder{from: from, to: to, sendMax: sendMax}
}

// DestTag sets the destination tag.
func (b *CheckCreateBuilder) DestTag(tag uint32) *CheckCreateBuilder {
	b.destTag = &tag
	return b
}

// SourceTag sets the source tag.
func (b *CheckCreateBuilder) SourceTag(tag uint32) *CheckCreateBuilder {
	b.sourceTag = &tag
	return b
}

// Expiration sets the check expiration in Ripple epoch seconds.
func (b *CheckCreateBuilder) Expiration(exp uint32) *CheckCreateBuilder {
	b.expiration = &exp
	return b
}

// InvoiceID sets the invoice ID (256-bit hash as hex string).
func (b *CheckCreateBuilder) InvoiceID(id string) *CheckCreateBuilder {
	b.invoiceID = id
	return b
}

// Fee sets the transaction fee in drops.
func (b *CheckCreateBuilder) Fee(f uint64) *CheckCreateBuilder {
	b.common.fee = &f
	return b
}

// Flags sets transaction flags explicitly.
func (b *CheckCreateBuilder) Flags(flags uint32) *CheckCreateBuilder {
	b.common.flags = flags
	return b
}

// Build constructs the CheckCreate transaction.
func (b *CheckCreateBuilder) Build() *checktx.CheckCreate {
	c := checktx.NewCheckCreate(b.from.Address, b.to.Address, b.sendMax)
	b.common.apply(c)

	if b.destTag != nil {
		c.DestinationTag = b.destTag
	}
	if b.expiration != nil {
		c.Expiration = b.expiration
	}
	if b.invoiceID != "" {
		c.InvoiceID = b.invoiceID
	}
	if b.sourceTag != nil {
		c.SourceTag = b.sourceTag
	}
	return c
}

// --- CheckCashBuilder ---

// CheckCashBuilder provides a fluent interface for building CheckCash transactions.
type CheckCashBuilder struct {
	common     commonFields
	account    *jtx.Account
	checkID    string
	amount     *tx.Amount
	deliverMin *tx.Amount
}

// CheckCashAmount creates a CheckCash builder with an exact Amount.
// This matches rippled's check::cash(dest, checkId, amount).
func CheckCashAmount(account *jtx.Account, checkID string, amount tx.Amount) *CheckCashBuilder {
	return &CheckCashBuilder{
		account: account,
		checkID: checkID,
		amount:  &amount,
	}
}

// CheckCashDeliverMin creates a CheckCash builder with a DeliverMin.
// This matches rippled's check::cash(dest, checkId, DeliverMin(amount)).
func CheckCashDeliverMin(account *jtx.Account, checkID string, deliverMin tx.Amount) *CheckCashBuilder {
	return &CheckCashBuilder{
		account:    account,
		checkID:    checkID,
		deliverMin: &deliverMin,
	}
}

// Flags sets transaction flags explicitly.
func (b *CheckCashBuilder) Flags(flags uint32) *CheckCashBuilder {
	b.common.flags = flags
	return b
}

// Build constructs the CheckCash transaction.
func (b *CheckCashBuilder) Build() *checktx.CheckCash {
	c := checktx.NewCheckCash(b.account.Address, b.checkID)
	b.common.apply(c)

	if b.amount != nil {
		c.SetExactAmount(*b.amount)
	}
	if b.deliverMin != nil {
		c.SetDeliverMin(*b.deliverMin)
	}
	return c
}

// --- CheckCancelBuilder ---

// CheckCancelBuilder provides a fluent interface for building CheckCancel transactions.
type CheckCancelBuilder struct {
	common  commonFields
	account *jtx.Account
	checkID string
}

// CheckCancel creates a new CheckCancelBuilder.
func CheckCancel(account *jtx.Account, checkID string) *CheckCancelBuilder {
	return &CheckCancelBuilder{account: account, checkID: checkID}
}

// Fee sets the transaction fee in drops.
func (b *CheckCancelBuilder) Fee(f uint64) *CheckCancelBuilder {
	b.common.fee = &f
	return b
}

// Flags sets transaction flags explicitly.
func (b *CheckCancelBuilder) Flags(flags uint32) *CheckCancelBuilder {
	b.common.flags = flags
	return b
}

// Build constructs the CheckCancel transaction.
func (b *CheckCancelBuilder) Build() *checktx.CheckCancel {
	c := checktx.NewCheckCancel(b.account.Address, b.checkID)
	b.common.apply(c)
	return c
}

type commonFields struct {
	fee   *uint64
	flags uint32
}

func (f commonFields) apply(transaction tx.Transaction) {
	common := transaction.GetCommon()
	if f.fee != nil {
		common.Fee = fmt.Sprintf("%d", *f.fee)
	}
	if f.flags != 0 {
		common.SetFlags(f.flags)
	}
}
