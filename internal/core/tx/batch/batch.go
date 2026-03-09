package batch

import (
	"errors"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypeBatch, func() tx.Transaction {
		return &Batch{BaseTx: *tx.NewBaseTx(tx.TypeBatch, "")}
	})
}

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

// BatchSigner is a signer for batch transactions
type BatchSigner struct {
	BatchSigner BatchSignerData `json:"BatchSigner"`
}

// BatchSignerData contains batch signer fields
type BatchSignerData struct {
	Account           string `json:"Account"`
	SigningPubKey     string `json:"SigningPubKey"`
	BatchTxnSignature string `json:"BatchTxnSignature"`
}

// Batch flags
const (
	// tfAllOrNothing fails the batch if any transaction fails
	BatchFlagAllOrNothing uint32 = 0x00000001
	// tfOnlyOne succeeds if exactly one transaction succeeds
	BatchFlagOnlyOne uint32 = 0x00000002
	// tfUntilFailure processes until the first failure
	BatchFlagUntilFailure uint32 = 0x00000004
	// tfIndependent processes all transactions independently
	BatchFlagIndependent uint32 = 0x00000008

	// tfBatchMask is the mask for invalid batch flags
	tfBatchMask uint32 = ^(BatchFlagAllOrNothing | BatchFlagOnlyOne | BatchFlagUntilFailure | BatchFlagIndependent)

	// MaxBatchTransactions is the maximum number of inner transactions
	MaxBatchTransactions = 8
)

// Batch errors
var (
	ErrBatchTooFewTxns      = errors.New("temARRAY_EMPTY: batch must have at least 2 transactions")
	ErrBatchTooManyTxns     = errors.New("temARRAY_TOO_LARGE: batch exceeds 8 transactions")
	ErrBatchInvalidFlags    = errors.New("temINVALID_FLAG: invalid batch flags")
	ErrBatchMustHaveOneFlag = errors.New("temINVALID_FLAG: exactly one batch mode flag required")
	ErrBatchTooManySigners  = errors.New("temARRAY_TOO_LARGE: batch signers exceeds 8 entries")
	ErrBatchDuplicateSigner = errors.New("temREDUNDANT: duplicate batch signer")
	ErrBatchSignerIsOuter   = errors.New("temBAD_SIGNER: batch signer cannot be outer account")
	ErrBatchNilInnerTx     = errors.New("temMALFORMED: inner transaction cannot be nil")
)

// NewBatch creates a new Batch transaction
func NewBatch(account string) *Batch {
	return &Batch{
		BaseTx: *tx.NewBaseTx(tx.TypeBatch, account),
	}
}

// TxType returns the transaction type
func (b *Batch) TxType() tx.Type {
	return tx.TypeBatch
}

// Validate validates the Batch transaction
// Reference: rippled Batch.cpp preflight()
func (b *Batch) Validate() error {
	if err := b.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags
	// Reference: rippled Batch.cpp:213-217
	if b.Common.Flags != nil && *b.Common.Flags&tfBatchMask != 0 {
		return ErrBatchInvalidFlags
	}

	// Must have exactly one of the mutually exclusive flags
	// Reference: rippled Batch.cpp:220-227
	flags := uint32(0)
	if b.Common.Flags != nil {
		flags = *b.Common.Flags
	}
	modeFlags := flags & (BatchFlagAllOrNothing | BatchFlagOnlyOne | BatchFlagUntilFailure | BatchFlagIndependent)
	popCount := 0
	for modeFlags != 0 {
		popCount += int(modeFlags & 1)
		modeFlags >>= 1
	}
	if popCount != 1 {
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

	// Validate each raw transaction has a non-nil inner transaction
	for _, rt := range b.RawTransactions {
		if rt.RawTransaction.InnerTx == nil {
			return ErrBatchNilInnerTx
		}
	}

	// Validate BatchSigners if present
	// Reference: rippled Batch.cpp:394-398
	if len(b.BatchSigners) > MaxBatchTransactions {
		return ErrBatchTooManySigners
	}

	// Check for duplicate signers and signer being outer account
	// Reference: rippled Batch.cpp:406-432
	seenSigners := make(map[string]bool)
	for _, signer := range b.BatchSigners {
		acct := signer.BatchSigner.Account
		if acct == b.Account {
			return ErrBatchSignerIsOuter
		}
		if seenSigners[acct] {
			return ErrBatchDuplicateSigner
		}
		seenSigners[acct] = true
	}

	return nil
}

// Flatten returns a flat map of all transaction fields.
// Inner transactions are flattened to STObject maps via their own Flatten() methods.
// Reference: rippled stores inner transactions as full STObjects in RawTransactions.
func (b *Batch) Flatten() (map[string]any, error) {
	m := b.BaseTx.GetCommon().ToMap()

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
				"Account":      s.BatchSigner.Account,
				"SigningPubKey": s.BatchSigner.SigningPubKey,
			}
			if s.BatchSigner.BatchTxnSignature != "" {
				signerMap["TxnSignature"] = s.BatchSigner.BatchTxnSignature
			}
			signers[i] = map[string]any{
				"BatchSigner": signerMap,
			}
		}
		m["BatchSigners"] = signers
	}

	return m, nil
}

// CalculateMinimumFee calculates the minimum required fee for a Batch transaction.
// Formula: (numSigners + 2) * baseFee + baseFee * numInnerTxns
// Reference: rippled Batch.cpp calculateBaseFee()
func (b *Batch) CalculateMinimumFee(baseFee uint64) uint64 {
	numSigners := uint64(len(b.BatchSigners))
	numInnerTxns := uint64(len(b.RawTransactions))
	return (numSigners+2)*baseFee + baseFee*numInnerTxns
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

// RequiredAmendments returns the amendments required for this transaction type
func (b *Batch) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureBatch}
}

// Apply applies the Batch transaction to the ledger.
// It decodes and processes each inner transaction according to the batch mode flag.
// Reference: rippled apply.cpp applyBatchTransactions()
func (b *Batch) Apply(ctx *tx.ApplyContext) tx.Result {
	if len(b.RawTransactions) == 0 {
		return tx.TemINVALID
	}

	// Write the outer account state (with fee deducted and sequence incremented
	// by the engine) to the view so inner transactions see the correct state.
	accountKey := keylet.Account(ctx.AccountID)
	outerAccountData, err := sle.SerializeAccountRoot(ctx.Account)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(accountKey, outerAccountData); err != nil {
		return tx.TefINTERNAL
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
	// Sequences always advance for attempted inner txns.
	for _, innerTx := range innerTxns {
		if innerTx == nil {
			// Nil inner tx - treat as failure
			if isUntilFailure {
				break
			}
			continue
		}

		result := applyInnerTransaction(ctx, innerTx)

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

	// Sync ctx.Account with the final state in the view so the engine
	// writes back the correct balance/sequence after Apply() returns.
	syncAccountFromView(ctx)

	return tx.TesSUCCESS
}

// applyAllOrNothing processes inner transactions with AllOrNothing semantics.
// All inner txns must succeed, or all changes are rolled back.
// Reference: rippled Batch.cpp applyBatchTransactions() with tfAllOrNothing
func (b *Batch) applyAllOrNothing(ctx *tx.ApplyContext, innerTxns []tx.Transaction) tx.Result {
	// Create a batch-level state table wrapping ctx.View
	batchTable := tx.NewApplyStateTable(ctx.View, ctx.TxHash, ctx.Config.LedgerSequence, ctx.Config.Rules)

	batchCtx := &tx.ApplyContext{
		View:      batchTable,
		Account:   ctx.Account,
		AccountID: ctx.AccountID,
		Config:    ctx.Config,
		TxHash:    ctx.TxHash,
		Metadata:  ctx.Metadata,
		Engine:    ctx.Engine,
	}

	for _, innerTx := range innerTxns {
		if innerTx == nil {
			// Nil inner tx in AllOrNothing → rollback
			return tx.TesSUCCESS
		}

		result := applyInnerTransaction(batchCtx, innerTx)
		if !result.IsSuccess() {
			// Any failure in AllOrNothing → discard batch table (rollback)
			return tx.TesSUCCESS
		}
	}

	// All succeeded — commit batch-level changes to ctx.View
	_, err := batchTable.Apply()
	if err != nil {
		return tx.TefINTERNAL
	}

	// Sync ctx.Account with the final state in the view
	syncAccountFromView(ctx)

	return tx.TesSUCCESS
}

// applyInnerTransaction processes a single inner transaction against the given view.
// It validates the sequence, increments it, and applies the transaction.
// Failed transactions still increment the sequence.
// Reference: rippled apply.cpp applyTransaction() with tapBATCH
func applyInnerTransaction(ctx *tx.ApplyContext, innerTx tx.Transaction) tx.Result {
	common := innerTx.GetCommon()

	// Decode the inner transaction's account
	accountID, err := sle.DecodeAccountID(common.Account)
	if err != nil {
		return tx.TefINTERNAL
	}

	accountKey := keylet.Account(accountID)

	// Read account from the view
	exists, err := ctx.View.Exists(accountKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	if !exists {
		return tx.TerNO_ACCOUNT
	}

	accountData, err := ctx.View.Read(accountKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	account, err := sle.ParseAccountRoot(accountData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check sequence
	if common.Sequence != nil {
		if *common.Sequence < account.Sequence {
			return tx.TefPAST_SEQ
		}
		if *common.Sequence > account.Sequence {
			return tx.TerPRE_SEQ
		}
	}

	// Create per-tx state table for isolation
	perTxTable := tx.NewApplyStateTable(ctx.View, ctx.TxHash, ctx.Config.LedgerSequence, ctx.Config.Rules)

	// Increment sequence
	account.Sequence++

	// Create inner apply context
	innerCtx := &tx.ApplyContext{
		View:      perTxTable,
		Account:   account,
		AccountID: accountID,
		Config:    ctx.Config,
		TxHash:    ctx.TxHash,
		Metadata:  ctx.Metadata,
		Engine:    ctx.Engine,
	}

	// Apply the inner transaction
	var result tx.Result
	if appliable, ok := innerTx.(tx.Appliable); ok {
		result = appliable.Apply(innerCtx)
	} else {
		result = tx.TesSUCCESS
	}

	// Serialize the updated account
	updatedData, err := sle.SerializeAccountRoot(account)
	if err != nil {
		return tx.TefINTERNAL
	}

	if result.IsSuccess() {
		// Success: update account in per-tx table and commit all changes
		if err := perTxTable.Update(accountKey, updatedData); err != nil {
			return tx.TefINTERNAL
		}
		if _, err := perTxTable.Apply(); err != nil {
			return tx.TefINTERNAL
		}
	} else if result.IsTec() {
		// TEC: sequence increments but transaction effects are discarded
		if err := ctx.View.Update(accountKey, updatedData); err != nil {
			return tx.TefINTERNAL
		}
	} else {
		// TEF/TER: sequence still increments for inner batch txns
		if err := ctx.View.Update(accountKey, updatedData); err != nil {
			return tx.TefINTERNAL
		}
	}

	return result
}

// syncAccountFromView reads the outer account from the view and updates ctx.Account
// so that the engine writes back the correct final state (with inner tx sequence/balance changes).
func syncAccountFromView(ctx *tx.ApplyContext) {
	accountKey := keylet.Account(ctx.AccountID)
	data, err := ctx.View.Read(accountKey)
	if err != nil {
		return
	}
	account, err := sle.ParseAccountRoot(data)
	if err != nil {
		return
	}
	ctx.Account.Balance = account.Balance
	ctx.Account.Sequence = account.Sequence
}

