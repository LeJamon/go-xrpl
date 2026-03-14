package escrow

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	escrowtx "github.com/LeJamon/goXRPLd/internal/tx/escrow"
)

// RippleEpoch is the Unix timestamp for the Ripple epoch (January 1, 2000 00:00:00 UTC).
// All Ripple timestamps are seconds since this epoch.
const RippleEpoch = 946684800

// ToRippleTime converts a Go time.Time to Ripple epoch time.
func ToRippleTime(t time.Time) uint32 {
	return uint32(t.Unix() - RippleEpoch)
}

// FromRippleTime converts a Ripple epoch time to Go time.Time.
func FromRippleTime(rippleTime uint32) time.Time {
	return time.Unix(int64(rippleTime)+RippleEpoch, 0)
}

// EscrowCreateBuilder provides a fluent interface for building EscrowCreate transactions.
type EscrowCreateBuilder struct {
	from        *testing.Account
	to          *testing.Account
	amount      int64 // XRP in drops
	finishAfter *uint32
	cancelAfter *uint32
	condition   []byte
	destTag     *uint32
	sourceTag   *uint32
	fee         int64
	sequence    *uint32
	flags       uint32
}

// EscrowCreate creates a new EscrowCreateBuilder.
// The amount is specified in drops (1 XRP = 1,000,000 drops).
func EscrowCreate(from, to *testing.Account, amount int64) *EscrowCreateBuilder {
	return &EscrowCreateBuilder{
		from:   from,
		to:     to,
		amount: amount,
		fee:    10, // Default fee: 10 drops
	}
}

// FinishTime sets the time after which the escrow can be finished.
func (b *EscrowCreateBuilder) FinishTime(t time.Time) *EscrowCreateBuilder {
	finishAfter := ToRippleTime(t)
	b.finishAfter = &finishAfter
	return b
}

// FinishAfter sets the finish time directly as Ripple epoch seconds.
func (b *EscrowCreateBuilder) FinishAfter(rippleTime uint32) *EscrowCreateBuilder {
	b.finishAfter = &rippleTime
	return b
}

// CancelTime sets the time after which the escrow can be cancelled.
func (b *EscrowCreateBuilder) CancelTime(t time.Time) *EscrowCreateBuilder {
	cancelAfter := ToRippleTime(t)
	b.cancelAfter = &cancelAfter
	return b
}

// CancelAfter sets the cancel time directly as Ripple epoch seconds.
func (b *EscrowCreateBuilder) CancelAfter(rippleTime uint32) *EscrowCreateBuilder {
	b.cancelAfter = &rippleTime
	return b
}

// Condition sets the crypto-condition that must be fulfilled.
// The condition should be the raw bytes of the crypto-condition.
func (b *EscrowCreateBuilder) Condition(cond []byte) *EscrowCreateBuilder {
	b.condition = cond
	return b
}

// ConditionHex sets the crypto-condition from a hex string.
func (b *EscrowCreateBuilder) ConditionHex(condHex string) *EscrowCreateBuilder {
	cond, _ := hex.DecodeString(condHex)
	b.condition = cond
	return b
}

// DestTag sets the destination tag.
func (b *EscrowCreateBuilder) DestTag(tag uint32) *EscrowCreateBuilder {
	b.destTag = &tag
	return b
}

// SourceTag sets the source tag.
func (b *EscrowCreateBuilder) SourceTag(tag uint32) *EscrowCreateBuilder {
	b.sourceTag = &tag
	return b
}

// Fee sets the transaction fee in drops.
func (b *EscrowCreateBuilder) Fee(f uint64) *EscrowCreateBuilder {
	b.fee = int64(f)
	return b
}

// Sequence sets the sequence number explicitly.
func (b *EscrowCreateBuilder) Sequence(seq uint32) *EscrowCreateBuilder {
	b.sequence = &seq
	return b
}

// Flags sets the transaction flags (e.g., tfPassive for testing invalid flags).
func (b *EscrowCreateBuilder) Flags(flags uint32) *EscrowCreateBuilder {
	b.flags = flags
	return b
}

// Build constructs the EscrowCreate transaction.
func (b *EscrowCreateBuilder) Build() tx.Transaction {
	amount := tx.NewXRPAmount(b.amount)
	e := escrowtx.NewEscrowCreate(b.from.Address, b.to.Address, amount)
	e.Fee = fmt.Sprintf("%d", b.fee)

	if b.finishAfter != nil {
		e.FinishAfter = b.finishAfter
	}
	if b.cancelAfter != nil {
		e.CancelAfter = b.cancelAfter
	}
	if b.condition != nil {
		s := hex.EncodeToString(b.condition)
		e.Condition = &s
	}
	if b.destTag != nil {
		e.DestinationTag = b.destTag
	}
	if b.sourceTag != nil {
		e.SourceTag = b.sourceTag
	}
	if b.sequence != nil {
		e.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		e.SetFlags(b.flags)
	}

	return e
}

// BuildEscrowCreate is a convenience method that returns the concrete *escrowtx.EscrowCreate type.
func (b *EscrowCreateBuilder) BuildEscrowCreate() *escrowtx.EscrowCreate {
	return b.Build().(*escrowtx.EscrowCreate)
}

// EscrowFinishBuilder provides a fluent interface for building EscrowFinish transactions.
type EscrowFinishBuilder struct {
	finisher      *testing.Account
	owner         *testing.Account
	offerSeq      uint32
	condition     []byte
	fulfillment   []byte
	fee           uint64
	sequence      *uint32
	credentialIDs []string
}

// EscrowFinish creates a new EscrowFinishBuilder.
// The finisher is the account submitting the transaction, owner is who created the escrow,
// and offerSeq is the sequence number of the EscrowCreate transaction.
func EscrowFinish(finisher *testing.Account, owner *testing.Account, offerSeq uint32) *EscrowFinishBuilder {
	return &EscrowFinishBuilder{
		finisher: finisher,
		owner:    owner,
		offerSeq: offerSeq,
		fee:      10, // Default fee: 10 drops
	}
}

// Fulfillment sets the fulfillment for the crypto-condition.
// Both condition and fulfillment must be provided together.
func (b *EscrowFinishBuilder) Fulfillment(f []byte) *EscrowFinishBuilder {
	b.fulfillment = f
	return b
}

// FulfillmentHex sets the fulfillment from a hex string.
func (b *EscrowFinishBuilder) FulfillmentHex(fHex string) *EscrowFinishBuilder {
	f, _ := hex.DecodeString(fHex)
	b.fulfillment = f
	return b
}

// Condition sets the crypto-condition (required if fulfillment is provided).
func (b *EscrowFinishBuilder) Condition(cond []byte) *EscrowFinishBuilder {
	b.condition = cond
	return b
}

// ConditionHex sets the crypto-condition from a hex string.
func (b *EscrowFinishBuilder) ConditionHex(condHex string) *EscrowFinishBuilder {
	cond, _ := hex.DecodeString(condHex)
	b.condition = cond
	return b
}

// WithConditionAndFulfillment sets both the condition and fulfillment together.
// This is the recommended way to provide crypto-condition data.
func (b *EscrowFinishBuilder) WithConditionAndFulfillment(cond, fulfillment []byte) *EscrowFinishBuilder {
	b.condition = cond
	b.fulfillment = fulfillment
	return b
}

// Fee sets the transaction fee in drops.
// Note: Fulfilling a crypto-condition requires extra fee based on fulfillment size.
func (b *EscrowFinishBuilder) Fee(f uint64) *EscrowFinishBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *EscrowFinishBuilder) Sequence(seq uint32) *EscrowFinishBuilder {
	b.sequence = &seq
	return b
}

// CredentialIDs sets the credential IDs for deposit preauth with credentials.
func (b *EscrowFinishBuilder) CredentialIDs(ids []string) *EscrowFinishBuilder {
	b.credentialIDs = ids
	return b
}

// Build constructs the EscrowFinish transaction.
func (b *EscrowFinishBuilder) Build() tx.Transaction {
	e := escrowtx.NewEscrowFinish(b.finisher.Address, b.owner.Address, b.offerSeq)
	e.Fee = fmt.Sprintf("%d", b.fee)

	if b.condition != nil {
		s := hex.EncodeToString(b.condition)
		e.Condition = &s
	}
	if b.fulfillment != nil {
		s := hex.EncodeToString(b.fulfillment)
		e.Fulfillment = &s
	}
	if b.sequence != nil {
		e.SetSequence(*b.sequence)
	}
	if len(b.credentialIDs) > 0 {
		e.CredentialIDs = b.credentialIDs
	}

	return e
}

// BuildEscrowFinish is a convenience method that returns the concrete *escrowtx.EscrowFinish type.
func (b *EscrowFinishBuilder) BuildEscrowFinish() *escrowtx.EscrowFinish {
	return b.Build().(*escrowtx.EscrowFinish)
}

// EscrowCancelBuilder provides a fluent interface for building EscrowCancel transactions.
type EscrowCancelBuilder struct {
	canceller *testing.Account
	owner     *testing.Account
	offerSeq  uint32
	fee       uint64
	sequence  *uint32
}

// EscrowCancel creates a new EscrowCancelBuilder.
// The canceller is the account submitting the transaction, owner is who created the escrow,
// and offerSeq is the sequence number of the EscrowCreate transaction.
func EscrowCancel(canceller *testing.Account, owner *testing.Account, offerSeq uint32) *EscrowCancelBuilder {
	return &EscrowCancelBuilder{
		canceller: canceller,
		owner:     owner,
		offerSeq:  offerSeq,
		fee:       10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *EscrowCancelBuilder) Fee(f uint64) *EscrowCancelBuilder {
	b.fee = f
	return b
}

// Sequence sets the sequence number explicitly.
func (b *EscrowCancelBuilder) Sequence(seq uint32) *EscrowCancelBuilder {
	b.sequence = &seq
	return b
}

// Build constructs the EscrowCancel transaction.
func (b *EscrowCancelBuilder) Build() tx.Transaction {
	e := escrowtx.NewEscrowCancel(b.canceller.Address, b.owner.Address, b.offerSeq)
	e.Fee = fmt.Sprintf("%d", b.fee)

	if b.sequence != nil {
		e.SetSequence(*b.sequence)
	}

	return e
}

// BuildEscrowCancel is a convenience method that returns the concrete *escrowtx.EscrowCancel type.
func (b *EscrowCancelBuilder) BuildEscrowCancel() *escrowtx.EscrowCancel {
	return b.Build().(*escrowtx.EscrowCancel)
}
