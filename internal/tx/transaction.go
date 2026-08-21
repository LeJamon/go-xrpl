package tx

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// Common errors
var (
	ErrMissingRequiredField   = errors.New("missing required field")
	ErrInvalidTransactionType = errors.New("invalid transaction type")
	ErrInvalidAmount          = errors.New("invalid amount")
	ErrInvalidDestination     = errors.New("invalid destination")
	ErrInvalidAccount         = errors.New("invalid account")
	ErrInvalidFlags           = ter.Errorf(ter.TemINVALID_FLAG, "invalid flags")
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

	// GetRawBytes returns the canonical serialized bytes retained for hashing.
	// It returns nil if the transaction has not been parsed or bound to bytes.
	GetRawBytes() []byte

	// SetRawBytes stores canonical serialized bytes retained by the transaction.
	SetRawBytes([]byte)

	// RequiredAmendments returns the list of amendment feature IDs that must be enabled
	// for this transaction type to be valid. Returns nil if no amendments required.
	RequiredAmendments() [][32]byte
}

// Appliable is implemented by transaction types that can apply themselves to ledger state.
// This replaces the central switch statement in Engine.doApply().
type Appliable interface {
	Apply(ctx *ApplyContext) ter.Result
}

// AppliedInnerTransaction is a Batch inner transaction whose state changes and
// transaction metadata were committed with its outer transaction.
type AppliedInnerTransaction struct {
	Transaction Transaction
	Metadata    *Metadata
}

// BatchInnerApplier applies a Batch transaction's inner transactions after the
// outer transaction has committed. This keeps outer and inner metadata isolated.
type BatchInnerApplier interface {
	ApplyInnerTransactions(ctx *ApplyContext) (ter.Result, []AppliedInnerTransaction)
}

// RulesPreflighter is implemented by transaction types whose preflight has
// amendment-rules-dependent checks that cannot live in the rules-free Validate()
// body. The engine runs PreflightRules right after Validate(), so these checks
// reject (with a tem* code and no fee) at the correct pipeline stage, before any
// ledger-state preclaim runs — matching rippled where rules-gated tem* checks
// are interleaved into the transactor's per-type preflight() body (T::preflight).
type RulesPreflighter interface {
	PreflightRules(rules *amendment.Rules) error
}

// RulesAwarePreflighter is implemented by transaction types whose complete
// type-specific preflight body must interleave amendment-dependent checks.
type RulesAwarePreflighter interface {
	PreflightWithRules(rules *amendment.Rules) error
}

// ExtraFeaturesChecker is implemented by transaction types with an amendment
// gate that rippled evaluates in T::checkExtraFeatures — which runs in
// invokePreflight BEFORE preflight1's common checks (flags mask, NetworkID,
// account, fee, signing key). The engine runs it first so an amendment-gated
// rejection (e.g. CreateOffer carrying sfDomainID under a disabled
// PermissionedDEX → temDISABLED) precedes any common-field TER. A non-nil error
// should carry a temDISABLED-class code via ter.Errorf.
//
// A type opts in by implementing it and moving the corresponding amendment gate
// out of its Validate()/PreflightRules body. Reference: rippled Transactor.h
// invokePreflight.
type ExtraFeaturesChecker interface {
	CheckExtraFeatures(rules *amendment.Rules) error
}

// FlagsMasker is implemented by transaction types that declare the set of flag
// bits that are invalid for the type. The engine rejects a transaction whose
// flags intersect the mask with temINVALID_FLAG at the preflight0 position,
// mirroring rippled preflight0's `tx.getFlags() & T::getFlagsMask(ctx)`. The
// mask may depend on the active amendments (e.g. a flag valid only once an
// amendment is enabled).
//
// A type that does not implement it gets no engine-level flag rejection, because
// the universal mask would reject every valid type-specific flag
// (tfPartialPayment, tfPassive, …). A type opts in by implementing GetFlagsMask
// (typically `^(tfUniversal | typeSpecificBits)`, i.e. the base tfUniversalMask
// for a type with no type-specific flags) and dropping the equivalent flag check
// from Validate(). Nearly every transaction type adopts it; the pipeline stage
// matters because the mask fires at preflight0, ahead of the fee/account/signing
// checks, so a stray flag beats a bad fee — matching rippled.
type FlagsMasker interface {
	GetFlagsMask(rules *amendment.Rules) uint32
}

// SigValidatedPreflighter is implemented by transaction types with a preflight
// check that rippled runs in T::preflightSigValidated — the invokePreflight stage
// AFTER preflight2's cryptographic signature verification. The engine runs it
// once signature verification succeeds, so a check placed here is trumped by a
// bad-signature temINVALID, matching rippled. EscrowFinish adopts it for its
// CredentialIDs shape check (credentials::checkFields), which rippled defers past
// the signature. A non-nil error carries a tem* code via ter.Errorf.
type SigValidatedPreflighter interface {
	PreflightSigValidated() error
}

// BatchInnerPreflightRunner owns the ordered validation of a Batch's inner
// transactions. The callback runs the engine's common and type-specific
// preflight for exactly one inner transaction.
type BatchInnerPreflightRunner interface {
	ValidateBatchOuter() error
	PreflightInnerTransactions(func(Transaction) ter.Result) error
}

// Preclaimer is implemented by transaction types that need additional
// stateful validation beyond the engine's common preclaim checks.
// Preclaim runs AFTER the engine's sequence/fee/signature checks and
// BEFORE doApply. Results from Preclaim are subject to the TapRETRY
// gate: tec codes are NOT applied when TapRETRY is set, allowing the
// transaction to be retried on the next pass.
// Reference: rippled applySteps.h — PreclaimResult.likelyToClaimFee
type Preclaimer interface {
	Preclaim(view LedgerView, config EngineConfig) ter.Result
}

// BadCurrency is the currency code that may not name a non-native (issued)
// amount: the ISO code "XRP" collides with the native asset.
// Reference: rippled protocol/UintTypes.cpp badCurrency()
const BadCurrency = "XRP"

// BatchFeeCalculator is implemented by transaction types that need custom minimum fee calculation.
// Used by Batch transactions which require a higher fee based on inner tx count and signers.
type BatchFeeCalculator interface {
	CalculateMinimumFee(view LedgerView, config EngineConfig) uint64
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

// BatchSignatureVerifier is implemented by transaction types whose batch-level
// signers carry cryptographic signatures over a signing digest (currently only
// Batch). The engine calls VerifyBatchSignatures from the signature-verification
// stage so the check is skipped under SkipSignatureVerification, exactly like the
// outer single/multi-sign verification. A non-nil error fails the transaction with
// temBAD_SIGNATURE. Reference: rippled STTx::checkBatchSign.
type BatchSignatureVerifier interface {
	VerifyBatchSignatures() error
}

// CounterpartyProvider is implemented by transactions that name an account
// whose consent is required in addition to the initiator's authorization.
type CounterpartyProvider interface {
	GetCounterparty() string
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
	MemoType      string `json:"MemoType,omitempty"`
	MemoData      string `json:"MemoData,omitempty"`
	MemoFormat    string `json:"MemoFormat,omitempty"`
	presentFields map[string]bool
}

func (m *Memo) UnmarshalJSON(data []byte) error {
	type memoAlias Memo
	var decoded memoAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*m = Memo(decoded)
	m.presentFields = make(map[string]bool, len(fields))
	for name := range fields {
		m.presentFields[name] = true
	}
	return nil
}

func (m Memo) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.toMap())
}

func (m Memo) toMap() map[string]any {
	fields := make(map[string]any, len(m.presentFields))
	if m.MemoType != "" || m.presentFields["MemoType"] {
		fields["MemoType"] = m.MemoType
	}
	if m.MemoData != "" || m.presentFields["MemoData"] {
		fields["MemoData"] = m.MemoData
	}
	if m.MemoFormat != "" || m.presentFields["MemoFormat"] {
		fields["MemoFormat"] = m.MemoFormat
	}
	return fields
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

// CounterpartySignature is the nested signature object (sfCounterpartySignature).
// It lets a second party attach a signature to a transaction without being the
// signing Account. It carries a single signature (SigningPubKey + TxnSignature)
// or a multi-signature (Signers without SigningPubKey). The field is excluded
// from the transaction's signing data (notSigning), so neither the top-level
// signer nor the counterparty covers it.
type CounterpartySignature struct {
	SigningPubKey string          `json:"SigningPubKey,omitempty"`
	TxnSignature  string          `json:"TxnSignature,omitempty"`
	Signers       []SignerWrapper `json:"Signers,omitempty"`
	presentFields map[string]bool
}

// SponsorSignature is the nested signature object attached by a transaction's
// sponsor. Like CounterpartySignature, the field itself is excluded from the
// transaction signing payload while its inner fields use their normal wire
// definitions.
type SponsorSignature struct {
	SigningPubKey string          `json:"SigningPubKey,omitempty"`
	TxnSignature  string          `json:"TxnSignature,omitempty"`
	Signers       []SignerWrapper `json:"Signers,omitempty"`
	presentFields map[string]bool
}

func (cs *CounterpartySignature) UnmarshalJSON(data []byte) error {
	type signatureAlias CounterpartySignature
	var decoded signatureAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*cs = CounterpartySignature(decoded)
	cs.presentFields = make(map[string]bool, len(fields))
	for name := range fields {
		cs.presentFields[name] = true
	}
	return nil
}

func (ss *SponsorSignature) UnmarshalJSON(data []byte) error {
	type signatureAlias SponsorSignature
	var decoded signatureAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*ss = SponsorSignature(decoded)
	ss.presentFields = make(map[string]bool, len(fields))
	for name := range fields {
		ss.presentFields[name] = true
	}
	return nil
}

func (cs *CounterpartySignature) HasField(name string) bool { return cs.presentFields[name] }
func (ss *SponsorSignature) HasField(name string) bool      { return ss.presentFields[name] }

func (cs *CounterpartySignature) MarkFieldPresent(name string) {
	if cs.presentFields == nil {
		cs.presentFields = make(map[string]bool)
	}
	cs.presentFields[name] = true
}

func (ss *SponsorSignature) MarkFieldPresent(name string) {
	if ss.presentFields == nil {
		ss.presentFields = make(map[string]bool)
	}
	ss.presentFields[name] = true
}

// ToMap serializes the counterparty object for the binary codec.
func (cs *CounterpartySignature) ToMap() map[string]any {
	m := make(map[string]any)
	if cs.SigningPubKey != "" || cs.HasField("SigningPubKey") {
		m["SigningPubKey"] = cs.SigningPubKey
	}
	if cs.TxnSignature != "" || cs.HasField("TxnSignature") {
		m["TxnSignature"] = cs.TxnSignature
	}
	if len(cs.Signers) > 0 || cs.HasField("Signers") {
		signers := make([]map[string]any, len(cs.Signers))
		for i, sw := range cs.Signers {
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
	return m
}

func (ss *SponsorSignature) ToMap() map[string]any {
	m := make(map[string]any)
	if ss.SigningPubKey != "" || ss.HasField("SigningPubKey") {
		m["SigningPubKey"] = ss.SigningPubKey
	}
	if ss.TxnSignature != "" || ss.HasField("TxnSignature") {
		m["TxnSignature"] = ss.TxnSignature
	}
	if len(ss.Signers) > 0 || ss.HasField("Signers") {
		signers := make([]map[string]any, len(ss.Signers))
		for i, sw := range ss.Signers {
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
	return m
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

	// CounterpartySignature is a nested signature attached by a second party.
	// It is excluded from the transaction's signing data and verified
	// separately after the top-level signature.
	CounterpartySignature *CounterpartySignature `json:"CounterpartySignature,omitempty"`

	// Delegate is the account delegating permission to execute this transaction.
	// When present, the fee is charged to the delegate and signature is verified
	// against the delegate's keys.
	// Reference: rippled Transactor.cpp sfDelegate
	Delegate string `json:"Delegate,omitempty"`

	Sponsor          string            `json:"Sponsor,omitempty"`
	SponsorFlags     *uint32           `json:"SponsorFlags,omitempty"`
	SponsorSignature *SponsorSignature `json:"SponsorSignature,omitempty"`

	rawBytes []byte

	// PresentFields tracks which fields were present in the original parsed data.
	// This is used to distinguish between a field being absent vs explicitly set to empty.
	PresentFields map[string]bool `json:"-"`

	// sigVerified records that this transaction's cryptographic signature has
	// already been verified off the open-ledger apply strand, so the in-strand
	// signature check can skip the repeat verify. It is never serialized and is
	// meaningful only for one in-memory parsed transaction. No synchronization
	// guards it: ingress writes the verdict (PrewarmSignature) and then submits
	// on the same goroutine, so the write happens-before the in-strand read and
	// before the parsed transaction is shared with any other goroutine.
	sigVerified     bool
	sigVerifiedTxID [32]byte

	// cachedTxID memoises this transaction's id. The id is a pure function of
	// RawBytes, so the cache stays valid until SetRawBytes replaces them (which
	// clears it). Carries the same single-goroutine, unsynchronised contract as
	// sigVerified: the apply path reuses one parsed transaction across
	// preflight/preclaim/apply, so the id is computed once at ingress and read
	// under the strand without re-hashing.
	cachedTxID [32]byte
	txIDCached bool

	// preflightedRules records the amendment rules under which this
	// transaction's structural preflight last succeeded. Structural preflight
	// (everything except the view-dependent signature check) is a pure function
	// of the transaction fields and the active rules, so when the engine
	// re-preflights the same parsed transaction under the identical rules — as
	// the open-ledger apply strand does, preflighting once in TxQ.Apply and
	// again in Engine.Apply under modifyMu — the second run skips the repeat.
	// The rules pointer keys the verdict: a later ledger rebuilds rules, so a
	// pointer mismatch forces a recompute and the verdict never outlives an
	// amendment change. Same single-goroutine, unsynchronised contract as
	// cachedTxID.
	preflightedRules *amendment.Rules
	preflightedTxID  [32]byte
	rawFieldsID      [32]byte
	rawFieldsIDSet   bool
}

// Validate validates the common fields. preflightCommonFields catches these
// before tx.Validate() runs, but unit tests and parse.go call Common.Validate
// directly, so the codes need to be typed for those paths.
func (c *Common) Validate() error {
	if c.Account == "" {
		return ter.Errorf(ter.TemBAD_SRC_ACCOUNT, "Account is required")
	}
	if c.TransactionType == "" {
		return ter.Errorf(ter.TemINVALID, "TransactionType is required")
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

// GetRawBytes returns the retained canonical serialized bytes.
func (c *Common) GetRawBytes() []byte {
	return append([]byte(nil), c.rawBytes...)
}

// SetRawBytes stores canonical serialized bytes and invalidates every
// verdict derived from the prior transaction contents.
func (c *Common) SetRawBytes(data []byte) {
	sameRaw := bytes.Equal(c.rawBytes, data)
	c.rawBytes = append([]byte(nil), data...)
	c.sigVerified = false
	c.sigVerifiedTxID = [32]byte{}
	c.txIDCached = false
	c.cachedTxID = [32]byte{}
	c.preflightedRules = nil
	c.preflightedTxID = [32]byte{}
	if !sameRaw {
		c.rawFieldsID = [32]byte{}
		c.rawFieldsIDSet = false
	}
}

// MarkRawFieldsIdentity records the current-field identity associated with RawBytes.
func (c *Common) MarkRawFieldsIdentity(txID [32]byte) {
	c.rawFieldsID = txID
	c.rawFieldsIDSet = true
}

// RawFieldsIdentity returns the current-field identity bound to RawBytes.
func (c *Common) RawFieldsIdentity() ([32]byte, bool) {
	return c.rawFieldsID, c.rawFieldsIDSet
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

// MarkSignatureVerified records that the transaction's cryptographic signature
// has been verified, so a later in-strand check can skip re-verifying it.
func (c *Common) MarkSignatureVerified(txID [32]byte) {
	c.sigVerified = true
	c.sigVerifiedTxID = txID
}

// SignatureVerified reports whether the transaction's signature was already
// verified off-strand (see MarkSignatureVerified).
func (c *Common) SignatureVerified(txID ...[32]byte) bool {
	if !c.sigVerified {
		return false
	}
	return len(txID) == 0 || c.sigVerifiedTxID == txID[0]
}

// PreflightVerified reports whether this transaction's structural preflight
// already succeeded under the given rules (see preflightedRules). A nil rules
// never matches, so the cache is a no-op on paths that do not supply rules.
func (c *Common) PreflightVerified(rules *amendment.Rules, txID ...[32]byte) bool {
	if c.preflightedRules == nil || c.preflightedRules != rules {
		return false
	}
	return len(txID) == 0 || c.preflightedTxID == txID[0]
}

// MarkPreflightVerified records that the structural preflight succeeded under
// the given rules so a later in-strand preflight can skip the repeat.
func (c *Common) MarkPreflightVerified(rules *amendment.Rules, txID [32]byte) {
	c.preflightedRules = rules
	c.preflightedTxID = txID
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
	if len(c.Memos) > 0 || c.HasField("Memos") {
		m["Memos"] = flattenMemos(c.Memos)
	}
	if c.NetworkID != nil {
		m["NetworkID"] = *c.NetworkID
	}
	if len(c.Signers) > 0 || c.HasField("Signers") {
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
	if c.TxnSignature != "" || c.HasField("TxnSignature") {
		m["TxnSignature"] = c.TxnSignature
	}
	if c.CounterpartySignature != nil {
		m["CounterpartySignature"] = c.CounterpartySignature.ToMap()
	}
	if c.Delegate != "" {
		m["Delegate"] = c.Delegate
	}
	if c.Sponsor != "" {
		m["Sponsor"] = c.Sponsor
	}
	if c.SponsorFlags != nil {
		m["SponsorFlags"] = *c.SponsorFlags
	}
	if c.SponsorSignature != nil {
		m["SponsorSignature"] = c.SponsorSignature.ToMap()
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

// SeqProxyKey encodes the SeqProxy type and value into a single uint64 suitable
// for canonical ordering. Bits 0..31 hold the value; bit 32 is set for
// ticket-based transactions. This guarantees all sequence-typed entries sort
// strictly before all ticket-typed entries regardless of value, matching
// rippled's SeqProxy::operator< (the type bit dominates the value).
// Reference: rippled SeqProxy.h operator<, STTx::getSeqProxy().
func (c *Common) SeqProxyKey() uint64 {
	if c.Sequence != nil && *c.Sequence != 0 {
		return uint64(*c.Sequence)
	}
	if c.TicketSequence != nil {
		return uint64(*c.TicketSequence) | (1 << 32)
	}
	return 0
}

// BaseTx provides a base implementation for transactions
type BaseTx struct {
	Common
	txType         Type
	fallbackFields map[string]any
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
	fields := make(map[string]any, len(b.fallbackFields)+len(commonFields))
	for name, value := range b.fallbackFields {
		fields[name] = cloneTransactionFieldValue(value)
	}
	for name, value := range b.Common.ToMap() {
		fields[name] = value
	}
	return fields, nil
}

func (b *BaseTx) setFallbackFields(fields map[string]any) {
	b.fallbackFields = make(map[string]any)
	for name, value := range fields {
		if _, common := commonFieldStyles[name]; common {
			continue
		}
		b.fallbackFields[name] = cloneTransactionFieldValue(value)
	}
}

func cloneTransactionFieldValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		copy := make(map[string]any, len(typed))
		for name, nested := range typed {
			copy[name] = cloneTransactionFieldValue(nested)
		}
		return copy
	case []any:
		copy := make([]any, len(typed))
		for i, nested := range typed {
			copy[i] = cloneTransactionFieldValue(nested)
		}
		return copy
	case []map[string]any:
		copy := make([]map[string]any, len(typed))
		for i, nested := range typed {
			copy[i] = cloneTransactionFieldValue(nested).(map[string]any)
		}
		return copy
	default:
		return value
	}
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
