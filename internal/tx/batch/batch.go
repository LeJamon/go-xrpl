package batch

import (
	"encoding/json"
	"fmt"
	"math/bits"
	"strconv"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// Batch is a transaction that contains multiple inner transactions.
type Batch struct {
	tx.BaseTx

	// RawTransactions contains the inner transactions as nested STObjects (required)
	RawTransactions []RawTransaction `json:"RawTransactions" xrpl:"RawTransactions"`

	// BatchSigners are the batch-level signers (optional)
	BatchSigners []BatchSigner `json:"BatchSigners,omitempty" xrpl:"BatchSigners,omitempty"`
}

// RawTransaction wraps an inner transaction object.
// Matches rippled's sfRawTransaction (OBJECT, field 34) structure.
type RawTransaction struct {
	RawTransaction RawTransactionData `json:"RawTransaction"`
}

// RawTransactionData contains the inner transaction as a full object (STObject).
// Reference: rippled stores inner transactions as nested STObjects, not hex blobs.
type RawTransactionData struct {
	InnerTx tx.Transaction
}

func (r *RawTransactionData) UnmarshalJSON(data []byte) error {
	parsed, err := tx.ParseJSON(data)
	if err != nil {
		return fmt.Errorf("parse inner transaction JSON: %w", err)
	}
	var innerMap map[string]any
	if err := json.Unmarshal(data, &innerMap); err != nil {
		return err
	}
	typeName, _ := innerMap["TransactionType"].(string)
	txType, knownType := tx.TypeFromName(typeName)
	if knownType {
		if err := tx.ValidateTemplateFields(txType, innerMap); err != nil {
			return fmt.Errorf("validate inner transaction: %w", err)
		}
	}
	raw, err := tx.SerializeTransaction(parsed)
	if err != nil {
		return fmt.Errorf("encode inner transaction: %w", err)
	}
	inner, err := tx.ParseFromBinary(raw)
	if err != nil {
		return fmt.Errorf("parse inner transaction: %w", err)
	}
	r.InnerTx = inner
	return nil
}

// BatchSigner is a signer for batch transactions
type BatchSigner struct {
	BatchSigner BatchSignerData `json:"BatchSigner"`
}

// BatchSignerData contains batch signer fields.
// For single-sign: SigningPubKey is non-empty, Signers is nil.
// For multi-sign: SigningPubKey is "", Signers contains the nested multi-signers.
// Reference: rippled sfBatchSigner object
type BatchSignerData struct {
	Account           string             `json:"Account"`
	SigningPubKey     string             `json:"SigningPubKey"`
	BatchTxnSignature string             `json:"TxnSignature,omitempty"`
	Signers           []tx.SignerWrapper `json:"Signers,omitempty"`
	signingPubKeySet  bool
	txnSignatureSet   bool
	signersSet        bool
}

func (s *BatchSignerData) UnmarshalJSON(data []byte) error {
	type signerAlias BatchSignerData
	var decoded signerAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*s = BatchSignerData(decoded)
	_, s.signingPubKeySet = fields["SigningPubKey"]
	_, s.txnSignatureSet = fields["TxnSignature"]
	_, s.signersSet = fields["Signers"]
	return nil
}

func (s BatchSignerData) hasTxnSignature() bool {
	return s.txnSignatureSet || s.BatchTxnSignature != ""
}

func (s BatchSignerData) hasSigners() bool {
	return s.signersSet || len(s.Signers) > 0
}

// Batch flags. The mode-flag bit positions match rippled TxFlags.h exactly so
// that cross-implementation batches share one canonical flag word (and one
// signing digest); the low-nibble values previously used here were wire- and
// signature-incompatible with rippled.
const (
	// tfAllOrNothing fails the batch if any transaction fails
	BatchFlagAllOrNothing uint32 = 0x00010000
	// tfOnlyOne succeeds if exactly one transaction succeeds
	BatchFlagOnlyOne uint32 = 0x00020000
	// tfUntilFailure processes until the first failure
	BatchFlagUntilFailure uint32 = 0x00040000
	// tfIndependent processes all transactions independently
	BatchFlagIndependent uint32 = 0x00080000

	// tfBatchMask is the mask of invalid outer Batch flags. It permits the four
	// mode bits plus tfFullyCanonicalSig, and rejects tfInnerBatchTxn on the
	// outer (nested batches are not supported), matching rippled TxFlags.h.
	tfBatchMask uint32 = ^(tx.TfUniversal | BatchFlagAllOrNothing | BatchFlagOnlyOne | BatchFlagUntilFailure | BatchFlagIndependent) | tx.TfInnerBatchTxn

	// MaxBatchTransactions is the maximum number of inner transactions.
	MaxBatchTransactions = tx.MaxBatchTransactions
	// MaxBatchSigners is the maximum number of accounts that may authorize a Batch.
	MaxBatchSigners = tx.MaxBatchSigners
)

// Batch errors. Inner-tx errors mirror the per-inner rejections in rippled
// Batch.cpp:249-374 (Batch::preflight inner loop).
var (
	ErrBatchTooFewTxns            = ter.Errorf(ter.TemARRAY_EMPTY, "batch must have at least 2 transactions")
	ErrBatchTooManyTxns           = ter.Errorf(ter.TemARRAY_TOO_LARGE, "batch exceeds 8 transactions")
	ErrBatchMustHaveOneFlag       = ter.Errorf(ter.TemINVALID_FLAG, "exactly one batch mode flag required")
	ErrBatchTooManySigners        = ter.Errorf(ter.TemARRAY_TOO_LARGE, "batch signers exceeds 24 entries")
	ErrBatchDuplicateSigner       = ter.Errorf(ter.TemBAD_SIGNER, "duplicate batch signer")
	ErrBatchUnsortedSigner        = ter.Errorf(ter.TemBAD_SIGNER, "batch signers must be strictly ordered")
	ErrBatchSignerIsOuter         = ter.Errorf(ter.TemBAD_SIGNER, "batch signer cannot be outer account")
	ErrBatchSignerNotRequired     = ter.Errorf(ter.TemBAD_SIGNER, "no account signature for inner txn")
	ErrBatchMissingSigner         = ter.Errorf(ter.TemBAD_SIGNER, "missing batch signer for inner txn account")
	ErrBatchInvalidSignature      = ter.Errorf(ter.TemBAD_SIGNATURE, "invalid batch txn signature")
	ErrBatchNilInnerTx            = ter.Errorf(ter.TemMALFORMED, "inner transaction cannot be nil")
	ErrBatchDuplicateInnerTx      = ter.Errorf(ter.TemREDUNDANT, "duplicate inner transaction")
	ErrBatchInnerIsBatch          = ter.Errorf(ter.TemINVALID, "inner transaction cannot itself be a Batch")
	ErrBatchInnerDisabledType     = ter.Errorf(ter.TemINVALID_INNER_BATCH, "inner transaction type is not allowed in a batch")
	ErrBatchInnerMissingFlag      = ter.Errorf(ter.TemINVALID_FLAG, "inner transaction missing tfInnerBatchTxn flag")
	ErrBatchInnerHasTxnSignature  = ter.Errorf(ter.TemBAD_SIGNATURE, "inner transaction cannot include TxnSignature")
	ErrBatchInnerHasSigners       = ter.Errorf(ter.TemBAD_SIGNER, "inner transaction cannot include Signers")
	ErrBatchInnerHasSigningPubKey = ter.Errorf(ter.TemBAD_REGKEY, "inner transaction SigningPubKey must be empty")
	ErrBatchInnerBadFee           = ter.Errorf(ter.TemBAD_FEE, "inner transaction must have a fee of 0")
	ErrBatchInnerFeeSponsored     = ter.Errorf(ter.TemINVALID_FLAG, "inner transaction cannot sponsor its fee")
	ErrBatchInnerSeqAndTicket     = ter.Errorf(ter.TemSEQ_AND_TICKET, "inner transaction must have exactly one of Sequence and TicketSequence")
	ErrBatchInnerTicketAndTxnID   = ter.Errorf(ter.TemINVALID_INNER_BATCH, "inner transaction must not carry AccountTxnID when using a ticket")
	ErrBatchInnerDupSeqOrTicket   = ter.Errorf(ter.TemREDUNDANT, "duplicate inner Sequence or TicketSequence for account")
	ErrBatchInvalidInnerTx        = ter.Errorf(ter.TemINVALID_INNER_BATCH, "inner transaction failed validation")
	ErrBatchInnerHashUncomputable = ter.Errorf(ter.TemINVALID, "failed to compute inner transaction hash")
)

// disabledInnerTxTypes are transaction types that may not appear as inner
// transactions of a Batch. The check is unconditional — it is not gated on any
// amendment — so a batch wrapping one of these is rejected at preflight
// regardless of whether the wrapped feature is enabled.
// Reference: rippled Batch::disabledTxTypes (Batch.h) / Batch::preflight.
var disabledInnerTxTypes = map[tx.Type]struct{}{
	tx.TypeVaultCreate:             {},
	tx.TypeVaultSet:                {},
	tx.TypeVaultDelete:             {},
	tx.TypeVaultDeposit:            {},
	tx.TypeVaultWithdraw:           {},
	tx.TypeVaultClawback:           {},
	tx.TypeLoanBrokerSet:           {},
	tx.TypeLoanBrokerDelete:        {},
	tx.TypeLoanBrokerCoverDeposit:  {},
	tx.TypeLoanBrokerCoverWithdraw: {},
	tx.TypeLoanBrokerCoverClawback: {},
	tx.TypeLoanSet:                 {},
	tx.TypeLoanDelete:              {},
	tx.TypeLoanManage:              {},
	tx.TypeLoanPay:                 {},
}

// NewBatch creates a new Batch transaction
func NewBatch(account string) *Batch {
	return &Batch{
		BaseTx: *tx.NewBaseTx(tx.TypeBatch, account),
	}
}

func (b *Batch) TxType() tx.Type {
	return tx.TypeBatch
}

// GetFlagsMask adopts the engine FlagsMasker seam, checking tfBatchMask at
// preflight0 — before the popcount mode check in Validate — mirroring rippled
// Batch::getFlagsMask.
func (b *Batch) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tfBatchMask
}

// InnerTxCount returns the number of inner transactions in the batch.
// This is used by the test environment to count inner batch transactions
// for fee metrics in ProcessClosedLedger.
func (b *Batch) InnerTxCount() int {
	return len(b.RawTransactions)
}

// InnerTransactions implements tx.BatchOuter.
// Reference: rippled Batch.cpp:303-312.
func (b *Batch) InnerTransactions() []tx.Transaction {
	txns := make([]tx.Transaction, len(b.RawTransactions))
	for i, rt := range b.RawTransactions {
		txns[i] = rt.RawTransaction.InnerTx
	}
	return txns
}

// checkInnerSignatureFields rejects signature material on an inner batch object,
// mirroring the checkSignatureFields lambda in rippled Batch::preflight: a
// TxnSignature yields temBAD_SIGNATURE, a Signers array temBAD_SIGNER, and a
// non-empty SigningPubKey temBAD_REGKEY. It is applied to every inner
// transaction and to its nested CounterpartySignature/SponsorSignature.
func checkInnerSignatureFields(signingPubKey string, hasTxnSignature, hasSigners bool) error {
	if hasTxnSignature {
		return ErrBatchInnerHasTxnSignature
	}
	if hasSigners {
		return ErrBatchInnerHasSigners
	}
	if signingPubKey != "" {
		return ErrBatchInnerHasSigningPubKey
	}
	return nil
}

// PreflightInnerTransactions validates and preflights each inner transaction in
// protocol order before advancing to the next one.
func (b *Batch) PreflightInnerTransactions(preflight func(tx.Transaction) ter.Result) error {
	flags := b.GetFlags()
	enforceUnique := flags&(BatchFlagAllOrNothing|BatchFlagUntilFailure) != 0
	uniqueHashes := make(map[[32]byte]struct{}, len(b.RawTransactions))
	accountSeqTicket := make(map[string]map[uint32]struct{})
	for _, rt := range b.RawTransactions {
		inner := rt.RawTransaction.InnerTx
		if inner == nil {
			return ErrBatchNilInnerTx
		}
		if inner.TxType() == tx.TypeClawback {
			if err := tx.ValidateTransactionTemplateAllowlist(inner); err != nil {
				return ErrBatchInvalidInnerTx
			}
		}

		hash, err := tx.ComputeTransactionHash(inner)
		if err != nil {
			return ErrBatchInnerHashUncomputable
		}
		if _, dup := uniqueHashes[hash]; dup {
			return ErrBatchDuplicateInnerTx
		}
		uniqueHashes[hash] = struct{}{}

		if inner.TxType() == tx.TypeBatch {
			return ErrBatchInnerIsBatch
		}

		if _, disabled := disabledInnerTxTypes[inner.TxType()]; disabled {
			return ErrBatchInnerDisabledType
		}

		innerCommon := inner.GetCommon()

		if innerCommon.GetFlags()&tx.TfInnerBatchTxn == 0 {
			return ErrBatchInnerMissingFlag
		}
		if err := checkInnerSignatureFields(
			innerCommon.SigningPubKey,
			innerCommon.HasField("TxnSignature") || innerCommon.TxnSignature != "",
			innerCommon.HasField("Signers") || len(innerCommon.Signers) > 0,
		); err != nil {
			return err
		}
		var wireFields map[string]any
		if raw := inner.GetRawBytes(); len(raw) != 0 {
			wireFields, _ = binarycodec.DecodeBytes(raw)
		}
		// A CounterpartySignature is optional on an inner transaction and should
		// not be present, but if it is it must not carry any signature material.
		if cp := innerCommon.CounterpartySignature; cp != nil {
			hasTxnSignature, hasSigners := nestedSignatureFieldPresence(wireFields, "CounterpartySignature")
			if err := checkInnerSignatureFields(
				cp.SigningPubKey,
				hasTxnSignature || cp.HasField("TxnSignature") || cp.TxnSignature != "",
				hasSigners || cp.HasField("Signers") || len(cp.Signers) > 0,
			); err != nil {
				return err
			}
		}
		if sponsor := innerCommon.SponsorSignature; sponsor != nil {
			hasTxnSignature, hasSigners := nestedSignatureFieldPresence(wireFields, "SponsorSignature")
			if err := checkInnerSignatureFields(
				sponsor.SigningPubKey,
				hasTxnSignature || sponsor.HasField("TxnSignature") || sponsor.TxnSignature != "",
				hasSigners || sponsor.HasField("Signers") || len(sponsor.Signers) > 0,
			); err != nil {
				return err
			}
		}
		if err := validateInnerFee(innerCommon.Fee); err != nil {
			return err
		}
		// Inner transactions have Fee=0, so fee sponsorship is nonsensical and
		// explicitly rejected by rippled. Reserve sponsorship remains allowed
		// for the transaction types on the common allow-list.
		if innerCommon.Sponsor != "" && innerCommon.SponsorFlags != nil &&
			*innerCommon.SponsorFlags&tx.SpfSponsorFee != 0 {
			return ErrBatchInnerFeeSponsored
		}
		if preflight(inner) != ter.TesSUCCESS {
			return ErrBatchInvalidInnerTx
		}

		// sfSequence absent and sfSequence==0 are equivalent here.
		seqVal := uint32(0)
		if innerCommon.Sequence != nil {
			seqVal = *innerCommon.Sequence
		}
		hasTicket := innerCommon.TicketSequence != nil
		if hasTicket && seqVal != 0 {
			return ErrBatchInnerSeqAndTicket
		}
		if !hasTicket && seqVal == 0 {
			return ErrBatchInnerSeqAndTicket
		}

		if enforceUnique {
			acct := innerCommon.Account
			seen, ok := accountSeqTicket[acct]
			if !ok {
				seen = make(map[uint32]struct{})
				accountSeqTicket[acct] = seen
			}
			if seqVal != 0 {
				if _, dup := seen[seqVal]; dup {
					return ErrBatchInnerDupSeqOrTicket
				}
				seen[seqVal] = struct{}{}
			}
			if hasTicket {
				ticket := *innerCommon.TicketSequence
				if _, dup := seen[ticket]; dup {
					return ErrBatchInnerDupSeqOrTicket
				}
				seen[ticket] = struct{}{}
			}
		}
	}
	return nil
}

func nestedSignatureFieldPresence(fields map[string]any, name string) (hasTxnSignature, hasSigners bool) {
	object, _ := fields[name].(map[string]any)
	_, hasTxnSignature = object["TxnSignature"]
	_, hasSigners = object["Signers"]
	return hasTxnSignature, hasSigners
}

func (b *Batch) requiredBatchSigners() map[string]struct{} {
	required := make(map[string]struct{})
	for _, rt := range b.RawTransactions {
		inner := rt.RawTransaction.InnerTx
		if inner == nil {
			continue
		}
		common := inner.GetCommon()
		authorizer := common.Account
		if common.Delegate != "" {
			authorizer = common.Delegate
		}
		if authorizer != b.Account {
			required[authorizer] = struct{}{}
		}
		if cp, ok := inner.(tx.CounterpartyProvider); ok {
			if counterparty := cp.GetCounterparty(); counterparty != "" && counterparty != b.Account {
				required[counterparty] = struct{}{}
			}
		}
		if common.SponsorSignature != nil && common.Sponsor != "" && common.Sponsor != b.Account {
			required[common.Sponsor] = struct{}{}
		}
	}
	return required
}

// Reference: rippled Batch.cpp:314-322 — inner fee must be present and 0.
func validateInnerFee(fee string) error {
	if fee == "" {
		return ErrBatchInnerBadFee
	}
	feeInt, err := strconv.ParseInt(fee, 10, 64)
	if err != nil || feeInt != 0 {
		return ErrBatchInnerBadFee
	}
	return nil
}

func (b *Batch) validateBatchSignerBounds() error {
	if len(b.BatchSigners) > MaxBatchSigners {
		return ErrBatchTooManySigners
	}
	for _, wrapper := range b.BatchSigners {
		signer := wrapper.BatchSigner
		n := len(signer.Signers)
		if n > sign.MaxMultiSigners || (signer.SigningPubKey == "" && n < sign.MinMultiSigners) {
			return ErrBatchInvalidSignature
		}
	}
	return nil
}

func (b *Batch) ValidateBatchOuter() error {
	if err := b.BaseTx.Validate(); err != nil {
		return err
	}

	// The tfBatchMask flag check runs at preflight0 via GetFlagsMask (before this
	// body), matching rippled where getFlagsMask precedes Batch::preflight.

	// Must have exactly one of the mutually exclusive flags
	// Reference: rippled Batch.cpp:220-227
	flags := uint32(0)
	if b.Common.Flags != nil {
		flags = *b.Common.Flags
	}
	modeFlags := flags & (BatchFlagAllOrNothing | BatchFlagOnlyOne | BatchFlagUntilFailure | BatchFlagIndependent)
	if bits.OnesCount32(modeFlags) != 1 {
		return ErrBatchMustHaveOneFlag
	}

	// Must have at least 2 transactions
	// Reference: rippled Batch.cpp:229-234
	if len(b.RawTransactions) <= 1 {
		return ErrBatchTooFewTxns
	}

	// Max 8 transactions per batch
	// Reference: rippled Batch.cpp:237-241
	if len(b.RawTransactions) > MaxBatchTransactions {
		return ErrBatchTooManyTxns
	}
	if len(b.BatchSigners) > MaxBatchSigners {
		return ErrBatchTooManySigners
	}

	return nil
}

// Reference: rippled Batch.cpp preflight()
func (b *Batch) Validate() error {
	if err := b.ValidateBatchOuter(); err != nil {
		return err
	}
	return b.PreflightInnerTransactions(func(tx.Transaction) ter.Result {
		return ter.TesSUCCESS
	})
}

// PreflightSigValidated checks exact signer coverage only after all cryptographic
// signatures have passed, matching Batch::preflightSigValidated.
func (b *Batch) PreflightSigValidated() error {
	return b.validateBatchSigners(b.requiredBatchSigners())
}

// Inner transactions are flattened to STObject maps via their own Flatten() methods.
// Reference: rippled stores inner transactions as full STObjects in RawTransactions.
func (b *Batch) Flatten() (map[string]any, error) {
	m := b.BaseTx.GetCommon().ToMap()
	tx.PopulateRequiredWireFields(m, b.GetCommon())

	// Build RawTransactions array with inner tx objects flattened to maps
	rawTxns := make([]map[string]any, len(b.RawTransactions))
	for i, rt := range b.RawTransactions {
		if rt.RawTransaction.InnerTx == nil {
			return nil, fmt.Errorf("inner transaction %d is nil", i)
		}
		if rt.RawTransaction.InnerTx.TxType() == tx.TypeClawback {
			if err := tx.ValidateTransactionTemplateAllowlist(rt.RawTransaction.InnerTx); err != nil {
				return nil, ter.Errorf(ter.TemMALFORMED, "invalid inner transaction %d: %v", i, err)
			}
		}
		innerMap, err := rt.RawTransaction.InnerTx.Flatten()
		if err != nil {
			return nil, fmt.Errorf("failed to flatten inner tx %d: %w", i, err)
		}
		tx.PopulateRequiredWireFields(innerMap, rt.RawTransaction.InnerTx.GetCommon())
		rawTxns[i] = map[string]any{
			"RawTransaction": innerMap,
		}
	}
	m["RawTransactions"] = rawTxns

	// Build BatchSigners if present
	if len(b.BatchSigners) > 0 {
		signers := make([]map[string]any, len(b.BatchSigners))
		for i, s := range b.BatchSigners {
			signerMap := map[string]any{
				"Account": s.BatchSigner.Account,
			}
			if s.BatchSigner.SigningPubKey != "" || s.BatchSigner.signingPubKeySet {
				signerMap["SigningPubKey"] = s.BatchSigner.SigningPubKey
			}
			if s.BatchSigner.hasTxnSignature() {
				signerMap["TxnSignature"] = s.BatchSigner.BatchTxnSignature
			}
			// Include nested Signers for multi-sign batch signers
			if s.BatchSigner.hasSigners() {
				nestedSigners := make([]map[string]any, len(s.BatchSigner.Signers))
				for j, nested := range s.BatchSigner.Signers {
					nestedMap := map[string]any{
						"Account":       nested.Signer.Account,
						"SigningPubKey": nested.Signer.SigningPubKey,
						"TxnSignature":  nested.Signer.TxnSignature,
					}
					nestedSigners[j] = map[string]any{
						"Signer": nestedMap,
					}
				}
				signerMap["Signers"] = nestedSigners
			}
			signers[i] = map[string]any{
				"BatchSigner": signerMap,
			}
		}
		m["BatchSigners"] = signers
	}

	return m, nil
}

// CalculateMinimumFee returns the transaction's base fee. Preclaim performs the
// same calculation and rejects malformed or overflowing totals.
// The total fee a batch must pay is the sum of:
//   - batchBase   = view.fees().base + Transactor::calculateBaseFee(view, tx)
//     = baseFee + (1 + len(outer.Signers) + len(sponsor.Signers)) * baseFee
//   - txnFees     = Σ inner-tx dispatched base fees
//   - signerFees  = effectiveSignerCount * baseFee
//
// effectiveSignerCount counts each BatchSigner once when it carries a
// direct BatchTxnSignature and as len(Signers) when the entry is a
// multi-signed batch signer (Batch.cpp:128-134). Inner transactions use the
// same per-type fee dispatch as standalone transactions.
func (b *Batch) CalculateMinimumFee(view tx.LedgerView, config tx.EngineConfig) uint64 {
	if fee, ok := b.calculateMinimumFee(view, config); ok {
		return fee
	}
	return config.BaseFee
}

func (b *Batch) calculateMinimumFee(view tx.LedgerView, config tx.EngineConfig) (uint64, bool) {
	const maxAmount = ^uint64(0) >> 1

	if len(b.RawTransactions) > MaxBatchTransactions || len(b.BatchSigners) > MaxBatchSigners {
		return 0, false
	}

	outerSignerCount := uint64(len(b.Common.Signers) + sign.SponsorSignerCount(b))
	baseFee, ok := batchFeeMul(config.BaseFee, outerSignerCount+1, maxAmount)
	if !ok {
		return 0, false
	}
	batchBase, ok := batchFeeAdd(config.BaseFee, baseFee, maxAmount)
	if !ok {
		return 0, false
	}

	var txnFees uint64
	for _, raw := range b.RawTransactions {
		inner := raw.RawTransaction.InnerTx
		if inner == nil || inner.TxType() == tx.TypeBatch {
			return 0, false
		}
		innerFee := sign.CalculateBaseFee(inner, view, config)
		txnFees, ok = batchFeeAdd(txnFees, innerFee, maxAmount)
		if !ok {
			return 0, false
		}
	}

	var signerCount uint64
	for _, wrapper := range b.BatchSigners {
		signer := wrapper.BatchSigner
		switch {
		case signer.hasTxnSignature():
			signerCount++
		case len(signer.Signers) > sign.MaxMultiSigners:
			return 0, false
		case len(signer.Signers) > 0:
			signerCount += uint64(len(signer.Signers))
		}
	}
	signerFees, ok := batchFeeMul(config.BaseFee, signerCount, maxAmount)
	if !ok {
		return 0, false
	}
	innerFees, ok := batchFeeAdd(txnFees, signerFees, maxAmount)
	if !ok {
		return 0, false
	}
	return batchFeeAdd(batchBase, innerFees, maxAmount)
}

func batchFeeAdd(a, b, max uint64) (uint64, bool) {
	if a > max || b > max-a {
		return 0, false
	}
	return a + b, true
}

func batchFeeMul(a, b, max uint64) (uint64, bool) {
	if a != 0 && b > max/a {
		return 0, false
	}
	return a * b, true
}

func (b *Batch) Preclaim(view tx.LedgerView, config tx.EngineConfig) ter.Result {
	if _, ok := b.calculateMinimumFee(view, config); !ok {
		return ter.TecINSUFF_FEE
	}
	return ter.TesSUCCESS
}

// AddInnerTransaction adds an inner transaction to the batch.
// The transaction should have Fee="0", SigningPubKey="", and tfInnerBatchTxn flag set.
func (b *Batch) AddInnerTransaction(innerTx tx.Transaction) {
	b.RawTransactions = append(b.RawTransactions, RawTransaction{
		RawTransaction: RawTransactionData{
			InnerTx: innerTx,
		},
	})
}

func (b *Batch) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureBatchV1_1}
}

// GetBatchSigners returns the batch signers as BatchSignerInfo for authorization checking.
// Implements tx.BatchSignerProvider.
func (b *Batch) GetBatchSigners() []tx.BatchSignerInfo {
	result := make([]tx.BatchSignerInfo, len(b.BatchSigners))
	for i, s := range b.BatchSigners {
		info := tx.BatchSignerInfo{
			Account:       s.BatchSigner.Account,
			SigningPubKey: s.BatchSigner.SigningPubKey,
		}
		// Include nested multi-sign signers
		if len(s.BatchSigner.Signers) > 0 {
			info.Signers = make([]tx.SignerInfo, len(s.BatchSigner.Signers))
			for j, nested := range s.BatchSigner.Signers {
				info.Signers[j] = tx.SignerInfo{
					Account:       nested.Signer.Account,
					SigningPubKey: nested.Signer.SigningPubKey,
				}
			}
		}
		result[i] = info
	}
	return result
}

func (b *Batch) Apply(ctx *tx.ApplyContext) ter.Result {
	return ter.TesSUCCESS
}

// ApplyInnerTransactions processes the inner transactions after the outer Batch
// transaction has committed.
func (b *Batch) ApplyInnerTransactions(ctx *tx.ApplyContext) (ter.Result, []tx.AppliedInnerTransaction) {
	ctx.Log.Trace("batch apply",
		"account", b.Account,
		"txCount", len(b.RawTransactions),
		"flags", b.GetFlags(),
	)

	if len(b.RawTransactions) == 0 {
		return ter.TemINVALID, nil
	}

	flags := b.GetFlags()
	isAllOrNothing := flags&BatchFlagAllOrNothing != 0
	isOnlyOne := flags&BatchFlagOnlyOne != 0
	isUntilFailure := flags&BatchFlagUntilFailure != 0

	// Collect inner transactions
	innerTxns := make([]tx.Transaction, len(b.RawTransactions))
	for i, rawTx := range b.RawTransactions {
		innerTxns[i] = rawTx.RawTransaction.InnerTx
	}

	// For AllOrNothing mode, we use a batch-level state table that wraps ctx.View.
	// If any inner tx fails, we discard the entire batch-level table (rollback).
	// For other modes, we process directly against ctx.View.
	if isAllOrNothing {
		return b.applyAllOrNothing(ctx, innerTxns)
	}

	// For OnlyOne, UntilFailure, Independent modes:
	// Process inner transactions directly against ctx.View.
	var appliedInners []tx.AppliedInnerTransaction
	for _, innerTx := range innerTxns {
		if innerTx == nil {
			// Nil inner tx - treat as failure
			if isUntilFailure {
				break
			}
			continue
		}

		result, metadata := applyInnerWithEngine(
			ctx,
			innerTx,
			ctx.TransactionIndex+1+uint32(len(appliedInners)),
		)
		if metadata != nil {
			appliedInners = append(appliedInners, tx.AppliedInnerTransaction{
				Transaction: innerTx,
				Metadata:    metadata,
			})
		}

		if result.IsSuccess() {
			if isOnlyOne {
				break // Stop after first success
			}
		} else {
			if isUntilFailure {
				break // Stop at first failure
			}
			// OnlyOne and Independent: continue
		}
	}

	return ter.TesSUCCESS, appliedInners
}

// applyAllOrNothing processes inner transactions with AllOrNothing semantics.
// All inner txns must succeed, or all changes are rolled back.
// Reference: rippled Batch.cpp applyBatchTransactions() with tfAllOrNothing
func (b *Batch) applyAllOrNothing(
	ctx *tx.ApplyContext,
	innerTxns []tx.Transaction,
) (ter.Result, []tx.AppliedInnerTransaction) {
	// Create a batch-level state table wrapping ctx.View
	base, ok := ctx.View.(applystate.AtomicLedgerView)
	if !ok {
		return ter.TefINTERNAL, nil
	}
	batchTable := applystate.NewApplyStateTable(base, ctx.TxHash, ctx.Config.LedgerSequence, ctx.Config.Rules)

	batchCtx := &tx.ApplyContext{
		View:                   batchTable,
		Account:                ctx.Account,
		AccountID:              ctx.AccountID,
		Common:                 ctx.Common,
		Config:                 ctx.Config,
		TxHash:                 ctx.TxHash,
		TransactionIndex:       ctx.TransactionIndex,
		Metadata:               ctx.Metadata,
		InnerInvariants:        ctx.InnerInvariants,
		InnerTransactionEngine: ctx.InnerTransactionEngine,
		Log:                    ctx.Log,
		Ctx:                    ctx.Ctx,
	}

	appliedInners := make([]tx.AppliedInnerTransaction, 0, len(innerTxns))
	for _, innerTx := range innerTxns {
		if innerTx == nil {
			// Nil inner tx in AllOrNothing → rollback
			return ter.TesSUCCESS, nil
		}

		result, metadata := applyInnerWithEngine(
			batchCtx,
			innerTx,
			ctx.TransactionIndex+1+uint32(len(appliedInners)),
		)
		if !result.IsSuccess() {
			// Any failure in AllOrNothing → discard batch table (rollback)
			return ter.TesSUCCESS, nil
		}
		appliedInners = append(appliedInners, tx.AppliedInnerTransaction{
			Transaction: innerTx,
			Metadata:    metadata,
		})
	}

	if err := batchTable.ApplyUnthreaded(); err != nil {
		return ter.TefINTERNAL, nil
	}

	return ter.TesSUCCESS, appliedInners
}

func applyInnerWithEngine(
	ctx *tx.ApplyContext,
	innerTx tx.Transaction,
	transactionIndex uint32,
) (ter.Result, *tx.Metadata) {
	if ctx.InnerTransactionEngine == nil {
		return ter.TefINTERNAL, nil
	}
	result := ctx.InnerTransactionEngine.ApplyInnerTransaction(
		ctx.Ctx,
		ctx.View,
		innerTx,
		ctx.TxHash,
		transactionIndex,
	)
	if !result.Applied {
		return result.Result, nil
	}
	return result.Result, result.Metadata
}
