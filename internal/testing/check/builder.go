package check

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	checktx "github.com/LeJamon/goXRPLd/internal/tx/check"
	"github.com/LeJamon/goXRPLd/keylet"
)

// GetCheckID computes the check ledger entry ID from the creator account and sequence.
// This matches rippled's getCheckIndex(account, sequence).
func GetCheckID(acc *testing.Account, seq uint32) string {
	k := keylet.Check(acc.ID, seq)
	return hex.EncodeToString(k.Key[:])
}

// --- CheckCreateBuilder ---

// CheckCreateBuilder provides a fluent interface for building CheckCreate transactions.
type CheckCreateBuilder struct {
	from       *testing.Account
	to         *testing.Account
	sendMax    tx.Amount
	destTag    *uint32
	sourceTag  *uint32
	expiration *uint32
	invoiceID  string
	fee        uint64
	sequence   *uint32
	flags      uint32
}

// CheckCreate creates a new CheckCreateBuilder.
func CheckCreate(from, to *testing.Account, sendMax tx.Amount) *CheckCreateBuilder {
	return &CheckCreateBuilder{
		from:    from,
		to:      to,
		sendMax: sendMax,
		fee:     10, // Default fee: 10 drops
	}
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
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *CheckCreateBuilder) Sequence(seq uint32) *CheckCreateBuilder {
	b.sequence = &seq
	return b
}

// Flags sets transaction flags explicitly.
func (b *CheckCreateBuilder) Flags(flags uint32) *CheckCreateBuilder {
	b.flags = flags
	return b
}

// Build constructs the CheckCreate transaction.
func (b *CheckCreateBuilder) Build() tx.Transaction {
	c := checktx.NewCheckCreate(b.from.Address, b.to.Address, b.sendMax)
	c.Fee = fmt.Sprintf("%d", b.fee)

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
	if b.sequence != nil {
		c.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		c.SetFlags(b.flags)
	}

	return c
}

// BuildCheckCreate is a convenience method that returns the concrete *checktx.CheckCreate type.
func (b *CheckCreateBuilder) BuildCheckCreate() *checktx.CheckCreate {
	return b.Build().(*checktx.CheckCreate)
}

// --- CheckCashBuilder ---

// CheckCashBuilder provides a fluent interface for building CheckCash transactions.
type CheckCashBuilder struct {
	account    *testing.Account
	checkID    string
	amount     *tx.Amount
	deliverMin *tx.Amount
	fee        uint64
	sequence   *uint32
	flags      uint32
}

// CheckCashAmount creates a CheckCash builder with an exact Amount.
// This matches rippled's check::cash(dest, checkId, amount).
func CheckCashAmount(account *testing.Account, checkID string, amount tx.Amount) *CheckCashBuilder {
	return &CheckCashBuilder{
		account: account,
		checkID: checkID,
		amount:  &amount,
		fee:     10, // Default fee: 10 drops
	}
}

// CheckCashDeliverMin creates a CheckCash builder with a DeliverMin.
// This matches rippled's check::cash(dest, checkId, DeliverMin(amount)).
func CheckCashDeliverMin(account *testing.Account, checkID string, deliverMin tx.Amount) *CheckCashBuilder {
	return &CheckCashBuilder{
		account:    account,
		checkID:    checkID,
		deliverMin: &deliverMin,
		fee:        10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *CheckCashBuilder) Fee(f uint64) *CheckCashBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *CheckCashBuilder) Sequence(seq uint32) *CheckCashBuilder {
	b.sequence = &seq
	return b
}

// Flags sets transaction flags explicitly.
func (b *CheckCashBuilder) Flags(flags uint32) *CheckCashBuilder {
	b.flags = flags
	return b
}

// Build constructs the CheckCash transaction.
func (b *CheckCashBuilder) Build() tx.Transaction {
	c := checktx.NewCheckCash(b.account.Address, b.checkID)
	c.Fee = fmt.Sprintf("%d", b.fee)

	if b.amount != nil {
		c.SetExactAmount(*b.amount)
	}
	if b.deliverMin != nil {
		c.SetDeliverMin(*b.deliverMin)
	}
	if b.sequence != nil {
		c.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		c.SetFlags(b.flags)
	}

	return c
}

// BuildCheckCash is a convenience method that returns the concrete *checktx.CheckCash type.
func (b *CheckCashBuilder) BuildCheckCash() *checktx.CheckCash {
	return b.Build().(*checktx.CheckCash)
}

// --- CheckCancelBuilder ---

// CheckCancelBuilder provides a fluent interface for building CheckCancel transactions.
type CheckCancelBuilder struct {
	account  *testing.Account
	checkID  string
	fee      uint64
	sequence *uint32
	flags    uint32
}

// CheckCancel creates a new CheckCancelBuilder.
func CheckCancel(account *testing.Account, checkID string) *CheckCancelBuilder {
	return &CheckCancelBuilder{
		account: account,
		checkID: checkID,
		fee:     10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *CheckCancelBuilder) Fee(f uint64) *CheckCancelBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *CheckCancelBuilder) Sequence(seq uint32) *CheckCancelBuilder {
	b.sequence = &seq
	return b
}

// Flags sets transaction flags explicitly.
func (b *CheckCancelBuilder) Flags(flags uint32) *CheckCancelBuilder {
	b.flags = flags
	return b
}

// Build constructs the CheckCancel transaction.
func (b *CheckCancelBuilder) Build() tx.Transaction {
	c := checktx.NewCheckCancel(b.account.Address, b.checkID)
	c.Fee = fmt.Sprintf("%d", b.fee)

	if b.sequence != nil {
		c.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		c.SetFlags(b.flags)
	}

	return c
}

// BuildCheckCancel is a convenience method that returns the concrete *checktx.CheckCancel type.
func (b *CheckCancelBuilder) BuildCheckCancel() *checktx.CheckCancel {
	return b.Build().(*checktx.CheckCancel)
}
