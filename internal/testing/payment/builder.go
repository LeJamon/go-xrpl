// Package builders provides fluent transaction builder helpers for testing.
// These builders make it easy to construct transactions for test scenarios
// without dealing with the full complexity of the transaction types.
package payment

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

// PaymentBuilder provides a fluent interface for building Payment transactions.
type PaymentBuilder struct {
	from       *testing.Account
	to         *testing.Account
	amount     uint64 // XRP in drops
	issuedAmt  *tx.Amount
	fee        uint64
	destTag    *uint32
	sourceTag  *uint32
	invoiceID  string
	sendMax    *tx.Amount
	deliverMin *tx.Amount
	paths      [][]payment.PathStep
	sequence   *uint32
	flags         uint32
	memos         []tx.MemoWrapper
	credentialIDs []string
	mptIssuanceID string
	domainID      *string
}

// Pay creates a new PaymentBuilder for an XRP payment.
// The amount is specified in drops (1 XRP = 1,000,000 drops).
func Pay(from, to *testing.Account, amount uint64) *PaymentBuilder {
	return &PaymentBuilder{
		from:   from,
		to:     to,
		amount: amount,
		fee:    10, // Default fee: 10 drops
	}
}

// PayIssued creates a new PaymentBuilder for an issued currency payment.
func PayIssued(from, to *testing.Account, amount tx.Amount) *PaymentBuilder {
	return &PaymentBuilder{
		from:      from,
		to:        to,
		issuedAmt: &amount,
		fee:       10, // Default fee: 10 drops
	}
}

// Fee sets the transaction fee in drops.
func (b *PaymentBuilder) Fee(f uint64) *PaymentBuilder {
	b.fee = f
	return b
}

// DestTag sets the destination tag.
func (b *PaymentBuilder) DestTag(tag uint32) *PaymentBuilder {
	b.destTag = &tag
	return b
}

// SourceTag sets the source tag.
func (b *PaymentBuilder) SourceTag(tag uint32) *PaymentBuilder {
	b.sourceTag = &tag
	return b
}

// InvoiceID sets the invoice ID (256-bit hash as hex string).
func (b *PaymentBuilder) InvoiceID(id string) *PaymentBuilder {
	b.invoiceID = id
	return b
}

// SendMax sets the maximum amount to send (for cross-currency payments).
func (b *PaymentBuilder) SendMax(amount tx.Amount) *PaymentBuilder {
	b.sendMax = &amount
	return b
}

// DeliverMin sets the minimum amount to deliver (for partial payments).
func (b *PaymentBuilder) DeliverMin(amount tx.Amount) *PaymentBuilder {
	b.deliverMin = &amount
	return b
}

// Paths sets the payment paths for cross-currency payments.
// Each path is a slice of PathStep.
func (b *PaymentBuilder) Paths(paths [][]payment.PathStep) *PaymentBuilder {
	b.paths = paths
	return b
}

// PathsXRP adds a single path through XRP for cross-currency payments.
// This is a convenience method for the common case of using XRP as a bridge.
// Note: For XRP bridging, this adds a path with a single {Currency: "XRP"} element.
// This tells the strand builder to route through XRP as an intermediary currency,
// creating two book steps: srcCurrency→XRP and XRP→dstCurrency.
// Reference: rippled path(~XRP) creates a currency-only path element with type 0x10.
func (b *PaymentBuilder) PathsXRP() *PaymentBuilder {
	b.paths = [][]payment.PathStep{{
		{Currency: "XRP", Type: int(payment.PathTypeCurrency)},
	}}
	return b
}

// PathsIOUToIOU adds a path for IOU to IOU payments through an offer book.
// This creates a path that goes from srcCurrency to dstCurrency through the order book.
// srcCurrency: The currency being sent (e.g., "BTC")
// srcIssuer: The issuer of the source currency
// dstCurrency: The currency being received (e.g., "USD")
// dstIssuer: The issuer of the destination currency
func (b *PaymentBuilder) PathsIOUToIOU(srcCurrency string, srcIssuer *testing.Account, dstCurrency string, dstIssuer *testing.Account) *PaymentBuilder {
	// For IOU->IOU cross-currency payments, the path specifies the intermediate steps.
	// ~USD in rippled means "through the order book for USD" - this is represented
	// as a path step with just the currency (and optionally issuer) fields.
	// Reference: rippled path(~USD) creates STPathElement with currency only.
	b.paths = [][]payment.PathStep{
		{
			{
				Currency: dstCurrency,
				Issuer:   dstIssuer.Address,
			},
		},
	}
	return b
}

// PathsCurrency adds a single path step specifying a currency (with optional issuer).
// This is the Go equivalent of rippled's path(~USD) or path(~EUR).
// For IOU currencies, the issuer is required.
// For XRP, pass currency="XRP" and issuer=nil.
func (b *PaymentBuilder) PathsCurrency(currency string, issuer *testing.Account) *PaymentBuilder {
	step := payment.PathStep{Currency: currency}
	if issuer != nil {
		step.Issuer = issuer.Address
	}
	b.paths = [][]payment.PathStep{{step}}
	return b
}

// Sequence sets the sequence number explicitly.
func (b *PaymentBuilder) Sequence(seq uint32) *PaymentBuilder {
	b.sequence = &seq
	return b
}

// Flags sets arbitrary transaction flags.
func (b *PaymentBuilder) Flags(flags uint32) *PaymentBuilder {
	b.flags |= flags
	return b
}

// PartialPayment enables the partial payment flag.
func (b *PaymentBuilder) PartialPayment() *PaymentBuilder {
	b.flags |= payment.PaymentFlagPartialPayment
	return b
}

// NoDirectRipple enables the no direct ripple flag.
func (b *PaymentBuilder) NoDirectRipple() *PaymentBuilder {
	b.flags |= payment.PaymentFlagNoDirectRipple
	return b
}

// LimitQuality enables the limit quality flag.
func (b *PaymentBuilder) LimitQuality() *PaymentBuilder {
	b.flags |= payment.PaymentFlagLimitQuality
	return b
}

// CredentialIDs sets the credential IDs for authorized deposits.
// Each ID is a 64-character hex hash of a credential ledger entry.
// Reference: rippled credentials::ids({credIdx})
func (b *PaymentBuilder) CredentialIDs(ids []string) *PaymentBuilder {
	b.credentialIDs = ids
	return b
}

// MPTIssuanceID sets the MPT issuance ID for MPT direct payments.
func (b *PaymentBuilder) MPTIssuanceID(id string) *PaymentBuilder {
	b.mptIssuanceID = id
	return b
}

// DomainID sets the permissioned domain ID for this payment.
// The domainIDHex should be a 64-character hex string.
func (b *PaymentBuilder) DomainID(domainIDHex string) *PaymentBuilder {
	b.domainID = &domainIDHex
	return b
}

// WithMemo adds a memo to the transaction.
func (b *PaymentBuilder) WithMemo(memoType, memoData, memoFormat string) *PaymentBuilder {
	b.memos = append(b.memos, tx.MemoWrapper{
		Memo: tx.Memo{
			MemoType:   memoType,
			MemoData:   memoData,
			MemoFormat: memoFormat,
		},
	})
	return b
}

// Build constructs the Payment transaction.
func (b *PaymentBuilder) Build() tx.Transaction {
	var amount tx.Amount
	if b.issuedAmt != nil {
		amount = *b.issuedAmt
	} else {
		amount = tx.NewXRPAmount(int64(b.amount))
	}

	payment := payment.NewPayment(b.from.Address, b.to.Address, amount)
	payment.Fee = fmt.Sprintf("%d", b.fee)

	if b.destTag != nil {
		payment.DestinationTag = b.destTag
	}
	if b.sourceTag != nil {
		payment.SourceTag = b.sourceTag
	}
	if b.invoiceID != "" {
		payment.InvoiceID = b.invoiceID
	}
	if b.sendMax != nil {
		payment.SendMax = b.sendMax
	}
	if b.deliverMin != nil {
		payment.DeliverMin = b.deliverMin
	}
	if b.paths != nil {
		payment.Paths = b.paths
	}
	if b.sequence != nil {
		payment.SetSequence(*b.sequence)
	}
	if b.flags != 0 {
		payment.SetFlags(b.flags)
	}
	if len(b.memos) > 0 {
		payment.Memos = b.memos
	}
	if b.credentialIDs != nil {
		payment.CredentialIDs = b.credentialIDs
	}
	if b.mptIssuanceID != "" {
		payment.MPTokenIssuanceID = b.mptIssuanceID
	}
	if b.domainID != nil {
		payment.DomainID = b.domainID
	}

	return payment
}

// BuildPayment is a convenience method that returns the concrete *payment.Payment type.
func (b *PaymentBuilder) BuildPayment() *payment.Payment {
	return b.Build().(*payment.Payment)
}
