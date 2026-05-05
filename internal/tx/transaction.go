package tx

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

// Common errors
var (
	ErrMissingRequiredField   = errors.New("missing required field")
	ErrInvalidTransactionType = errors.New("invalid transaction type")
	ErrInvalidAmount          = errors.New("invalid amount")
	ErrInvalidDestination     = errors.New("invalid destination")
	ErrInvalidAccount         = errors.New("invalid account")
	ErrInvalidFlags           = Errorf(TemINVALID_FLAG, "invalid flags")
	ErrInvalidSequence        = errors.New("invalid sequence")
)

// Transaction is the interface that all transaction types must implement
type Transaction interface {
	// TxType returns the transaction type
	TxType() Type

	// GetCommon returns the common transaction fields
	GetCommon() *Common

	// Validate checks if the transaction is valid
	Validate() error

	// Flatten returns a flat map of all transaction fields for serialization
	Flatten() (map[string]any, error)

	// GetRawBytes returns the original serialized bytes (for hash computation)
	// Returns nil if transaction was not parsed from bytes
	GetRawBytes() []byte

	// SetRawBytes stores the original serialized bytes
	SetRawBytes([]byte)

	// RequiredAmendments returns the list of amendment feature IDs that must be enabled
	// for this transaction type to be valid. Returns nil if no amendments required.
	RequiredAmendments() [][32]byte
}

// Appliable is implemented by transaction types that can apply themselves to ledger state.
// This replaces the central switch statement in Engine.doApply().
type Appliable interface {
	Apply(ctx *ApplyContext) Result
}

// Preclaimer is implemented by transaction types that need additional
// stateful validation beyond the engine's common preclaim checks.
// Preclaim runs AFTER the engine's sequence/fee/signature checks and
// BEFORE doApply. Results from Preclaim are subject to the TapRETRY
// gate: tec codes are NOT applied when TapRETRY is set, allowing the
// transaction to be retried on the next pass.
// Reference: rippled applySteps.h — PreclaimResult.likelyToClaimFee
type Preclaimer interface {
	Preclaim(config EngineConfig) Result
}

// TecApplier is implemented by transaction types that need to apply side-effects
// even when returning a tec result code. In rippled, tecEXPIRED is special-cased
// to re-apply expired credential deletions after the view is reset.
// Reference: rippled Transactor.cpp - tecEXPIRED handling with removeExpiredCredentials
type TecApplier interface {
	ApplyOnTec(ctx *ApplyContext) Result
}

// BatchFeeCalculator is implemented by transaction types that need custom minimum fee calculation.
// Used by Batch transactions which require a higher fee based on inner tx count and signers.
type BatchFeeCalculator interface {
	CalculateMinimumFee(baseFee uint64) uint64
}

// CustomBaseFeeCalculator is implemented by transaction types that override calculateBaseFee()
// and need access to the ledger view and engine config for their minimum fee.
// Reference: rippled Transactor::calculateBaseFee() virtual override pattern —
// rippled's version has access to the view (view.fees().increment).
type CustomBaseFeeCalculator interface {
	CalculateBaseFee(view LedgerView, config EngineConfig) uint64
}

// BatchSignerInfo represents a single batch signer entry for authorization checking.
// Reference: rippled sfBatchSigners array entries.
type BatchSignerInfo struct {
	Account       string       // Signer's account address
	SigningPubKey string       // Signer's public key hex (empty for multi-sign)
	Signers       []SignerInfo // Nested multi-sign signers (non-empty when SigningPubKey is "")
}

// SignerInfo represents a single signer within a multi-sign batch signer.
// Reference: rippled sfSigners array within sfBatchSigner
type SignerInfo struct {
	Account       string // Signer's account address
	SigningPubKey string // Signer's public key hex
}

// BatchSignerProvider is implemented by transaction types that have batch-level signers
// (currently only Batch). The engine uses this to perform checkBatchSign authorization.
// Reference: rippled Batch::checkSign -> Transactor::checkBatchSign
type BatchSignerProvider interface {
	GetBatchSigners() []BatchSignerInfo
}

// Amount is an alias for state.Amount — represents either XRP (as drops int64) or an issued currency amount
type Amount = state.Amount

// NewXRPAmount creates an XRP amount in drops
func NewXRPAmount(drops int64) Amount {
	return state.NewXRPAmountFromInt(drops)
}

// NewIssuedAmount creates an issued currency amount from mantissa and exponent
func NewIssuedAmount(mantissa int64, exponent int, currency, issuer string) Amount {
	return state.NewIssuedAmountFromValue(mantissa, exponent, currency, issuer)
}

// NewIssuedAmountFromFloat64 creates an issued currency amount from a float64 value.
// This is a convenience function for tests and simple use cases.
func NewIssuedAmountFromFloat64(value float64, currency, issuer string) Amount {
	return state.NewIssuedAmountFromFloat64(value, currency, issuer)
}

// Memo represents a memo attached to a transaction
type Memo struct {
	MemoType   string `json:"MemoType,omitempty"`
	MemoData   string `json:"MemoData,omitempty"`
	MemoFormat string `json:"MemoFormat,omitempty"`
}

// MemoWrapper wraps a Memo for JSON serialization
type MemoWrapper struct {
	Memo Memo `json:"Memo"`
}

// Signer represents a signer in a multi-signed transaction
type Signer struct {
	Account       string `json:"Account"`
	SigningPubKey string `json:"SigningPubKey"`
	TxnSignature  string `json:"TxnSignature"`
}

// SignerWrapper wraps a Signer for JSON serialization
type SignerWrapper struct {
	Signer Signer `json:"Signer"`
}

// Common contains fields common to all transaction types
type Common struct {
	// Required fields
	Account         string `json:"Account"`
	TransactionType string `json:"TransactionType"`

	// Fee in drops (required for signing, optional for submission)
	Fee string `json:"Fee,omitempty"`

	// Sequence number (required unless using TicketSequence)
	Sequence *uint32 `json:"Sequence,omitempty"`

	// Optional common fields
	AccountTxnID       string          `json:"AccountTxnID,omitempty"`
	Flags              *uint32         `json:"Flags,omitempty"`
	LastLedgerSequence *uint32         `json:"LastLedgerSequence,omitempty"`
	Memos              []MemoWrapper   `json:"Memos,omitempty"`
	NetworkID          *uint32         `json:"NetworkID,omitempty"`
	Signers            []SignerWrapper `json:"Signers,omitempty"`
	SourceTag          *uint32         `json:"SourceTag,omitempty"`
	SigningPubKey      string          `json:"SigningPubKey,omitempty"`
	TicketSequence     *uint32         `json:"TicketSequence,omitempty"`
	TxnSignature       string          `json:"TxnSignature,omitempty"`

	// Delegate is the account delegating permission to execute this transaction.
	// When present, the fee is charged to the delegate and signature is verified
	// against the delegate's keys.
	// Reference: rippled Transactor.cpp sfDelegate
	Delegate string `json:"Delegate,omitempty"`

	// RawBytes stores the original serialized bytes for hash computation
	RawBytes []byte `json:"-"`

	// PresentFields tracks which fields were present in the original parsed data.
	// This is used to distinguish between a field being absent vs explicitly set to empty.
	PresentFields map[string]bool `json:"-"`
}

// Validate validates the common fields
func (c *Common) Validate() error {
	if c.Account == "" {
		return errors.New("Account is required")
	}
	if c.TransactionType == "" {
		return errors.New("TransactionType is required")
	}
	return nil
}

// HasField checks if a field was present in the original parsed data.
// This is used to distinguish between a field being absent vs explicitly set to empty.
// For example, in DIDSet, an empty URI means "clear the field" while absent means "keep existing".
func (c *Common) HasField(name string) bool {
	if c.PresentFields == nil {
		return false
	}
	return c.PresentFields[name]
}

// SetPresentFields sets the map of fields that were present in the original parsed data.
func (c *Common) SetPresentFields(fields map[string]bool) {
	c.PresentFields = fields
}

// GetRawBytes returns the original serialized bytes
func (c *Common) GetRawBytes() []byte {
	return c.RawBytes
}

// SetRawBytes stores the original serialized bytes
func (c *Common) SetRawBytes(data []byte) {
	c.RawBytes = data
}

// SetFlags sets the flags field
func (c *Common) SetFlags(flags uint32) {
	c.Flags = &flags
}

// GetFlags returns the flags value (0 if not set)
func (c *Common) GetFlags() uint32 {
	if c.Flags == nil {
		return 0
	}
	return *c.Flags
}

// SetSequence sets the sequence number
func (c *Common) SetSequence(seq uint32) {
	c.Sequence = &seq
}

// GetSequence returns the sequence number (0 if not set)
func (c *Common) GetSequence() uint32 {
	if c.Sequence == nil {
		return 0
	}
	return *c.Sequence
}

// SetLastLedgerSequence sets the last ledger sequence
func (c *Common) SetLastLedgerSequence(seq uint32) {
	c.LastLedgerSequence = &seq
}

// AddMemo adds a memo to the transaction
func (c *Common) AddMemo(memoType, memoData, memoFormat string) {
	c.Memos = append(c.Memos, MemoWrapper{
		Memo: Memo{
			MemoType:   memoType,
			MemoData:   memoData,
			MemoFormat: memoFormat,
		},
	})
}

// ToMap converts common fields to a map
func (c *Common) ToMap() map[string]any {
	m := map[string]any{
		"Account":         c.Account,
		"TransactionType": c.TransactionType,
	}

	if c.Fee != "" {
		m["Fee"] = c.Fee
	}
	if c.Sequence != nil {
		m["Sequence"] = *c.Sequence
	}
	if c.AccountTxnID != "" {
		m["AccountTxnID"] = c.AccountTxnID
	}
	// sfFlags is soeOPTIONAL in rippled's common-fields template
	// (TxFormats.cpp:34). STObject::set(SOTemplate) at
	// STObject.cpp:165 stores it as nonPresentObject when the
	// assembler did not set it, and STObject::add at
	// STObject.cpp:907-921 filters STI_NOTPRESENT fields out of the
	// serialized blob. So when c.Flags is nil, the wire format must
	// carry no Flags bytes — emitting Flags=0 anyway would shift the
	// serialized field sequence and produce a different transaction
	// ID than rippled computes for the same JSON.
	if c.Flags != nil {
		m["Flags"] = *c.Flags
	}
	if c.LastLedgerSequence != nil {
		m["LastLedgerSequence"] = *c.LastLedgerSequence
	}
	if len(c.Memos) > 0 {
		m["Memos"] = c.Memos
	}
	if c.NetworkID != nil {
		m["NetworkID"] = *c.NetworkID
	}
	if len(c.Signers) > 0 {
		signers := make([]map[string]any, len(c.Signers))
		for i, sw := range c.Signers {
			signers[i] = map[string]any{
				"Signer": map[string]any{
					"Account":       sw.Signer.Account,
					"SigningPubKey": sw.Signer.SigningPubKey,
					"TxnSignature":  sw.Signer.TxnSignature,
				},
			}
		}
		m["Signers"] = signers
	}
	if c.SourceTag != nil {
		m["SourceTag"] = *c.SourceTag
	}
	if c.SigningPubKey != "" {
		m["SigningPubKey"] = c.SigningPubKey
	}
	if c.TicketSequence != nil {
		m["TicketSequence"] = *c.TicketSequence
	}
	if c.TxnSignature != "" {
		m["TxnSignature"] = c.TxnSignature
	}
	if c.Delegate != "" {
		m["Delegate"] = c.Delegate
	}

	return m
}

// SeqProxy returns the effective sequence value for this transaction.
// For ticket-based transactions (TicketSequence set), returns the ticket sequence.
// For normal transactions, returns the Sequence value.
// Reference: rippled STTx::getSeqProxy()
func (c *Common) SeqProxy() uint32 {
	if c.TicketSequence != nil {
		return *c.TicketSequence
	}
	if c.Sequence != nil {
		return *c.Sequence
	}
	return 0
}

// BaseTx provides a base implementation for transactions
type BaseTx struct {
	Common
	txType Type
}

func (b *BaseTx) TxType() Type {
	return b.txType
}

// GetCommon returns the common transaction fields
func (b *BaseTx) GetCommon() *Common {
	return &b.Common
}

func (b *BaseTx) Validate() error {
	return b.Common.Validate()
}

func (b *BaseTx) Flatten() (map[string]any, error) {
	return b.Common.ToMap(), nil
}

// RequiredAmendments returns no required amendments by default.
// Transaction types that require amendments should override this.
func (b *BaseTx) RequiredAmendments() [][32]byte {
	return nil
}

// NewBaseTx creates a new base transaction
func NewBaseTx(txType Type, account string) *BaseTx {
	return &BaseTx{
		Common: Common{
			Account:         account,
			TransactionType: txType.String(),
		},
		txType: txType,
	}
}
