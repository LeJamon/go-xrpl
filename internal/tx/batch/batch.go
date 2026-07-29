package batch

import (
	"encoding/hex"
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
	encoded, err := binarycodec.Encode(innerMap)
	if err != nil {
		return fmt.Errorf("encode inner transaction: %w", err)
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode inner transaction: %w", err)
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
	BatchTxnSignature string             `json:"BatchTxnSignature"`
	Signers           []tx.SignerWrapper `json:"Signers,omitempty"`
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

	// MaxBatchTransactions is the maximum number of inner transactions
	MaxBatchTransactions = 8
)

// Batch errors. Inner-tx errors mirror the per-inner rejections in rippled
// Batch.cpp:249-374 (Batch::preflight inner loop).
var (
	ErrBatchTooFewTxns            = ter.Errorf(ter.TemARRAY_EMPTY, "batch must have at least 2 transactions")
	ErrBatchTooManyTxns           = ter.Errorf(ter.TemARRAY_TOO_LARGE, "batch exceeds 8 transactions")
	ErrBatchMustHaveOneFlag       = ter.Errorf(ter.TemINVALID_FLAG, "exactly one batch mode flag required")
	ErrBatchTooManySigners        = ter.Errorf(ter.TemARRAY_TOO_LARGE, "batch signers exceeds 8 entries")
	ErrBatchDuplicateSigner       = ter.Errorf(ter.TemREDUNDANT, "duplicate batch signer")
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
func checkInnerSignatureFields(signingPubKey, txnSignature string, hasSigners bool) error {
	if txnSignature != "" {
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

// validateInnerTransactions runs the per-inner checks and, as a side effect,
// builds the set of inner-tx accounts other than the outer account — the
// accounts that must each be covered by a BatchSigner.
// Reference: rippled Batch.cpp:249-380 (per-inner checks in Batch::preflight).
func (b *Batch) validateInnerTransactions() (map[string]struct{}, error) {
	flags := b.GetFlags()
	enforceUnique := flags&(BatchFlagAllOrNothing|BatchFlagUntilFailure) != 0

	uniqueHashes := make(map[[32]byte]struct{}, len(b.RawTransactions))
	accountSeqTicket := make(map[string]map[uint32]struct{})
	requiredSigners := make(map[string]struct{})

	for _, rt := range b.RawTransactions {
		inner := rt.RawTransaction.InnerTx
		if inner == nil {
			return nil, ErrBatchNilInnerTx
		}

		hash, err := tx.ComputeTransactionHash(inner)
		if err != nil {
			return nil, ErrBatchInnerHashUncomputable
		}
		if _, dup := uniqueHashes[hash]; dup {
			return nil, ErrBatchDuplicateInnerTx
		}
		uniqueHashes[hash] = struct{}{}

		if inner.TxType() == tx.TypeBatch {
			return nil, ErrBatchInnerIsBatch
		}

		if _, disabled := disabledInnerTxTypes[inner.TxType()]; disabled {
			return nil, ErrBatchInnerDisabledType
		}

		innerCommon := inner.GetCommon()

		if innerCommon.GetFlags()&tx.TfInnerBatchTxn == 0 {
			return nil, ErrBatchInnerMissingFlag
		}
		if err := checkInnerSignatureFields(innerCommon.SigningPubKey, innerCommon.TxnSignature, len(innerCommon.Signers) > 0); err != nil {
			return nil, err
		}
		// A CounterpartySignature is optional on an inner transaction and should
		// not be present, but if it is it must not carry any signature material.
		if cp := innerCommon.CounterpartySignature; cp != nil {
			if err := checkInnerSignatureFields(cp.SigningPubKey, cp.TxnSignature, len(cp.Signers) > 0); err != nil {
				return nil, err
			}
		}
		if sponsor := innerCommon.SponsorSignature; sponsor != nil {
			if err := checkInnerSignatureFields(
				sponsor.SigningPubKey,
				sponsor.TxnSignature,
				len(sponsor.Signers) > 0,
			); err != nil {
				return nil, err
			}
		}
		if err := validateInnerFee(innerCommon.Fee); err != nil {
			return nil, err
		}
		// Inner transactions have Fee=0, so fee sponsorship is nonsensical and
		// explicitly rejected by rippled. Reserve sponsorship remains allowed
		// for the transaction types on the common allow-list.
		if innerCommon.Sponsor != "" && innerCommon.SponsorFlags != nil &&
			*innerCommon.SponsorFlags&tx.SpfSponsorFee != 0 {
			return nil, ErrBatchInnerFeeSponsored
		}

		// The inner's own preflight1 rejects a ticket combined with AccountTxnID
		// (getSeqProxy().isTicket() && sfAccountTxnID present -> temINVALID, surfaced
		// on the outer as temINVALID_INNER_BATCH). rippled runs the full inner
		// preflight here, after the fee check; go-xrpl folds this specific rule into
		// the inner loop since it never reaches the deferred engine inner-preflight
		// otherwise. Reference: rippled Batch.cpp inner preflight -> Transactor.cpp preflight1.
		usesTicket := innerCommon.TicketSequence != nil && (innerCommon.Sequence == nil || *innerCommon.Sequence == 0)
		if usesTicket && innerCommon.AccountTxnID != "" {
			return nil, ErrBatchInnerTicketAndTxnID
		}

		// rippled treats sfSequence absent and sfSequence==0 identically via
		// getFieldU32; Go's *uint32 nil and *0 collapse the same way here.
		seqVal := uint32(0)
		if innerCommon.Sequence != nil {
			seqVal = *innerCommon.Sequence
		}
		hasTicket := innerCommon.TicketSequence != nil
		if hasTicket && seqVal != 0 {
			return nil, ErrBatchInnerSeqAndTicket
		}
		if !hasTicket && seqVal == 0 {
			return nil, ErrBatchInnerSeqAndTicket
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
					return nil, ErrBatchInnerDupSeqOrTicket
				}
				seen[seqVal] = struct{}{}
			}
			if hasTicket {
				ticket := *innerCommon.TicketSequence
				if _, dup := seen[ticket]; dup {
					return nil, ErrBatchInnerDupSeqOrTicket
				}
				seen[ticket] = struct{}{}
			}
		}

		// An inner account that is not the outer account must be covered by a
		// BatchSigner. Delegate changes permission/signature authorization for
		// the inner transactor, but it does not replace the inner Account in the
		// outer BatchSigner coverage set.
		// Reference: rippled Batch.cpp:376-379 and Batch_test.cpp
		// testBatchDelegate.
		if innerCommon.Account != b.Account {
			requiredSigners[innerCommon.Account] = struct{}{}
		}
		if innerCommon.SponsorSignature != nil &&
			innerCommon.Sponsor != "" &&
			innerCommon.Sponsor != b.Account {
			requiredSigners[innerCommon.Sponsor] = struct{}{}
		}
	}
	return requiredSigners, nil
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

// Reference: rippled Batch.cpp preflight()
func (b *Batch) Validate() error {
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

	// Runs before the engine's BatchOuter loop so malformed inners surface
	// with their specific TER instead of generic temINVALID_INNER_BATCH.
	// Also collects the inner-tx accounts that each require a BatchSigner.
	// Reference: rippled Batch.cpp:249-380.
	requiredSigners, err := b.validateInnerTransactions()
	if err != nil {
		return err
	}

	// Validate the BatchSigners array: uniqueness, outer-account exclusion,
	// and requiredSigners coverage.
	// Reference: rippled Batch.cpp:387-453.
	return b.validateBatchSigners(requiredSigners)
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
				"Account":       s.BatchSigner.Account,
				"SigningPubKey": s.BatchSigner.SigningPubKey,
			}
			if s.BatchSigner.BatchTxnSignature != "" {
				signerMap["TxnSignature"] = s.BatchSigner.BatchTxnSignature
			}
			// Include nested Signers for multi-sign batch signers
			if len(s.BatchSigner.Signers) > 0 {
				nestedSigners := make([]map[string]any, len(s.BatchSigner.Signers))
				for j, nested := range s.BatchSigner.Signers {
					nestedMap := map[string]any{
						"Account":       nested.Signer.Account,
						"SigningPubKey": nested.Signer.SigningPubKey,
					}
					if nested.Signer.TxnSignature != "" {
						nestedMap["TxnSignature"] = nested.Signer.TxnSignature
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

// CalculateMinimumFee mirrors rippled Batch::calculateBaseFee
// (Batch.cpp:53-150). The total fee a batch must pay is the sum of:
//   - batchBase   = view.fees().base + Transactor::calculateBaseFee(view, tx)
//     = baseFee + (1 + len(outer.Signers) + len(sponsor.Signers)) * baseFee
//   - txnFees     = Σ inner-tx dispatched base fees
//   - signerFees  = effectiveSignerCount * baseFee
//
// effectiveSignerCount counts each BatchSigner once when it carries a
// direct BatchTxnSignature and as len(Signers) when the entry is a
// multi-signed batch signer (Batch.cpp:128-134). Inner transactions use the
// same per-type fee dispatch as standalone transactions. Inner Batch
// transactions return the overflow sentinel as a defense-in-depth fallback.
func (b *Batch) CalculateMinimumFee(view tx.LedgerView, config tx.EngineConfig) uint64 {
	baseFee := config.BaseFee
	outerSigners := uint64(len(b.Common.Signers) + sign.SponsorSignerCount(b))
	batchBase := baseFee + (1+outerSigners)*baseFee

	var txnFees uint64
	for _, rt := range b.RawTransactions {
		inner := rt.RawTransaction.InnerTx
		if inner == nil {
			continue
		}
		txnFees += innerBaseFee(inner, view, config)
	}

	var signerCount uint64
	for _, bs := range b.BatchSigners {
		if bs.BatchSigner.BatchTxnSignature != "" {
			signerCount++
		} else if len(bs.BatchSigner.Signers) > 0 {
			signerCount += uint64(len(bs.BatchSigner.Signers))
		}
	}

	return batchBase + txnFees + signerCount*baseFee
}

// innerBaseFee dispatches the same per-transaction fee override used for a
// standalone transaction. Inner Batch transactions return the overflow sentinel.
func innerBaseFee(inner tx.Transaction, view tx.LedgerView, config tx.EngineConfig) uint64 {
	if inner.TxType() == tx.TypeBatch {
		return overflowFee
	}
	return sign.CalculateBaseFee(inner, view, config)
}

// overflowFee is the sentinel fee returned when fee calculation hits an
// impossible condition (inner ttBATCH, overflow). It is larger than any
// legitimate batch fee can ever be, ensuring the outer tx fails the
// minimum-fee gate. Mirrors rippled Batch.cpp:66 returning INITIAL_XRP
// (100 billion XRP) — we use a similarly impossible drops sentinel.
const overflowFee uint64 = 100_000_000_000 * 1_000_000 // 100 billion XRP in drops

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
	return [][32]byte{amendment.FeatureBatch}
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
