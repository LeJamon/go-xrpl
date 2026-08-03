package escrow

import (
	"encoding/hex"
	"fmt"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	escrowtx "github.com/LeJamon/go-xrpl/internal/tx/escrow"
	"github.com/LeJamon/go-xrpl/protocol"
)

// ToRippleTime converts a Go time.Time to Ripple epoch time.
func ToRippleTime(t time.Time) uint32 {
	return protocol.ToRippleTime(t)
}

type transactionOptions struct {
	fee      uint64
	sequence *uint32
}

func defaultTransactionOptions() transactionOptions {
	return transactionOptions{fee: 10}
}

func (o *transactionOptions) apply(txn tx.Transaction) {
	txn.GetCommon().Fee = fmt.Sprintf("%d", o.fee)
	if o.sequence != nil {
		txn.GetCommon().SetSequence(*o.sequence)
	}
}

// EscrowCreateBuilder provides a fluent interface for building EscrowCreate transactions.
type EscrowCreateBuilder struct {
	transactionOptions
	from        *jtx.Account
	to          *jtx.Account
	amount      tx.Amount
	finishAfter *uint32
	cancelAfter *uint32
	condition   *string
	destTag     *uint32
	sourceTag   *uint32
	flags       uint32
}

// EscrowCreate creates a new EscrowCreateBuilder.
// The amount is specified in drops (1 XRP = 1,000,000 drops).
func EscrowCreate(from, to *jtx.Account, amount int64) *EscrowCreateBuilder {
	return &EscrowCreateBuilder{
		transactionOptions: defaultTransactionOptions(),
		from:               from,
		to:                 to,
		amount:             tx.NewXRPAmount(amount),
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
	encoded := hex.EncodeToString(cond)
	b.condition = &encoded
	return b
}

// ConditionHex sets the crypto-condition from a hex string.
func (b *EscrowCreateBuilder) ConditionHex(condHex string) *EscrowCreateBuilder {
	b.condition = &condHex
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
	b.fee = f
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

// IOUAmount sets an IOU amount for token escrow.
func (b *EscrowCreateBuilder) IOUAmount(amount tx.Amount) *EscrowCreateBuilder {
	b.amount = amount
	return b
}

// MPTAmount sets an MPT amount for token escrow.
func (b *EscrowCreateBuilder) MPTAmount(amount tx.Amount) *EscrowCreateBuilder {
	b.amount = amount
	return b
}

// Build constructs the EscrowCreate transaction.
func (b *EscrowCreateBuilder) Build() *escrowtx.EscrowCreate {
	e := escrowtx.NewEscrowCreate(b.from.Address, b.to.Address, b.amount)
	b.transactionOptions.apply(e)

	if b.finishAfter != nil {
		e.FinishAfter = b.finishAfter
	}
	if b.cancelAfter != nil {
		e.CancelAfter = b.cancelAfter
	}
	if b.condition != nil {
		condition := *b.condition
		e.Condition = &condition
	}
	if b.destTag != nil {
		e.DestinationTag = b.destTag
	}
	if b.sourceTag != nil {
		e.SourceTag = b.sourceTag
	}
	if b.flags != 0 {
		e.SetFlags(b.flags)
	}

	return e
}

// EscrowFinishBuilder provides a fluent interface for building EscrowFinish transactions.
type EscrowFinishBuilder struct {
	transactionOptions
	finisher      *jtx.Account
	owner         *jtx.Account
	offerSeq      uint32
	condition     *string
	fulfillment   *string
	credentialIDs []string
}

// EscrowFinish creates a new EscrowFinishBuilder.
// The finisher is the account submitting the transaction, owner is who created the escrow,
// and offerSeq is the sequence number of the EscrowCreate transaction.
func EscrowFinish(finisher *jtx.Account, owner *jtx.Account, offerSeq uint32) *EscrowFinishBuilder {
	return &EscrowFinishBuilder{
		transactionOptions: defaultTransactionOptions(),
		finisher:           finisher,
		owner:              owner,
		offerSeq:           offerSeq,
	}
}

// Fulfillment sets the fulfillment for the crypto-condition.
// Both condition and fulfillment must be provided together.
func (b *EscrowFinishBuilder) Fulfillment(f []byte) *EscrowFinishBuilder {
	encoded := hex.EncodeToString(f)
	b.fulfillment = &encoded
	return b
}

// FulfillmentHex sets the fulfillment from a hex string.
func (b *EscrowFinishBuilder) FulfillmentHex(fHex string) *EscrowFinishBuilder {
	b.fulfillment = &fHex
	return b
}

// Condition sets the crypto-condition (required if fulfillment is provided).
func (b *EscrowFinishBuilder) Condition(cond []byte) *EscrowFinishBuilder {
	encoded := hex.EncodeToString(cond)
	b.condition = &encoded
	return b
}

// ConditionHex sets the crypto-condition from a hex string.
func (b *EscrowFinishBuilder) ConditionHex(condHex string) *EscrowFinishBuilder {
	b.condition = &condHex
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
	if ids == nil {
		b.credentialIDs = nil
	} else {
		b.credentialIDs = append([]string{}, ids...)
	}
	return b
}

// Build constructs the EscrowFinish transaction.
func (b *EscrowFinishBuilder) Build() *escrowtx.EscrowFinish {
	e := escrowtx.NewEscrowFinish(b.finisher.Address, b.owner.Address, b.offerSeq)
	b.transactionOptions.apply(e)

	if b.condition != nil {
		condition := *b.condition
		e.Condition = &condition
	}
	if b.fulfillment != nil {
		fulfillment := *b.fulfillment
		e.Fulfillment = &fulfillment
	}
	if b.credentialIDs != nil {
		e.CredentialIDs = append([]string{}, b.credentialIDs...)
		e.GetCommon().SetPresentFields(map[string]bool{"CredentialIDs": true})
	}

	return e
}

// EscrowCancelBuilder provides a fluent interface for building EscrowCancel transactions.
type EscrowCancelBuilder struct {
	transactionOptions
	canceller *jtx.Account
	owner     *jtx.Account
	offerSeq  uint32
}

// EscrowCancel creates a new EscrowCancelBuilder.
// The canceller is the account submitting the transaction, owner is who created the escrow,
// and offerSeq is the sequence number of the EscrowCreate transaction.
func EscrowCancel(canceller *jtx.Account, owner *jtx.Account, offerSeq uint32) *EscrowCancelBuilder {
	return &EscrowCancelBuilder{
		transactionOptions: defaultTransactionOptions(),
		canceller:          canceller,
		owner:              owner,
		offerSeq:           offerSeq,
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
func (b *EscrowCancelBuilder) Build() *escrowtx.EscrowCancel {
	e := escrowtx.NewEscrowCancel(b.canceller.Address, b.owner.Address, b.offerSeq)
	b.transactionOptions.apply(e)

	return e
}
