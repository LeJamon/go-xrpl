package batch

import (
	"fmt"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	if err := tx.Register(tx.TypeBatch, func() tx.Transaction {
		return &Batch{BaseTx: *tx.NewBaseTx(tx.TypeBatch, "")}
	}); err != nil {
		panic(err)
	}
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
	ErrBatchTooFewTxns      = tx.Errorf(tx.TemARRAY_EMPTY, "batch must have at least 2 transactions")
	ErrBatchTooManyTxns     = tx.Errorf(tx.TemARRAY_TOO_LARGE, "batch exceeds 8 transactions")
	ErrBatchInvalidFlags    = tx.Errorf(tx.TemINVALID_FLAG, "invalid batch flags")
	ErrBatchMustHaveOneFlag = tx.Errorf(tx.TemINVALID_FLAG, "exactly one batch mode flag required")
	ErrBatchTooManySigners  = tx.Errorf(tx.TemARRAY_TOO_LARGE, "batch signers exceeds 8 entries")
	ErrBatchDuplicateSigner = tx.Errorf(tx.TemREDUNDANT, "duplicate batch signer")
	ErrBatchSignerIsOuter   = tx.Errorf(tx.TemBAD_SIGNER, "batch signer cannot be outer account")
	ErrBatchNilInnerTx      = tx.Errorf(tx.TemMALFORMED, "inner transaction cannot be nil")
)

// NewBatch creates a new Batch transaction
func NewBatch(account string) *Batch {
	return &Batch{
		BaseTx: *tx.NewBaseTx(tx.TypeBatch, account),
	}
}

func (b *Batch) TxType() tx.Type {
	return tx.TypeBatch
}

// InnerTxCount returns the number of inner transactions in the batch.
// This is used by the test environment to count inner batch transactions
// for fee metrics in ProcessClosedLedger.
func (b *Batch) InnerTxCount() int {
	return len(b.RawTransactions)
}

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

// It decodes and processes each inner transaction according to the batch mode flag.
// Reference: rippled apply.cpp applyBatchTransactions()
func (b *Batch) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("batch apply",
		"account", b.Account,
		"txCount", len(b.RawTransactions),
		"flags", b.GetFlags(),
	)

	if len(b.RawTransactions) == 0 {
		return tx.TemINVALID
	}

	// Write the outer account state (with fee deducted and sequence incremented
	// by the engine) to the view so inner transactions see the correct state.
	accountKey := keylet.Account(ctx.AccountID)
	outerAccountData, err := state.SerializeAccountRoot(ctx.Account)
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
	accountID, err := state.DecodeAccountID(common.Account)
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

	account, err := state.ParseAccountRoot(accountData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Determine whether this inner tx uses a ticket or a regular sequence.
	// Reference: rippled Transactor::checkSeqProxy + consumeSeqProxy
	isTicket := common.TicketSequence != nil && (common.Sequence == nil || *common.Sequence == 0)

	if isTicket {
		// Ticket-based inner transaction
		ticketSeq := *common.TicketSequence

		// Ticket must have been created already (ticketSeq < account.Sequence)
		if account.Sequence <= ticketSeq {
			return tx.TerPRE_SEQ // terPRE_TICKET equivalent
		}

		// Check ticket exists in the view
		ticketKey := keylet.Ticket(accountID, ticketSeq)
		ticketExists, tickErr := ctx.View.Exists(ticketKey)
		if tickErr != nil || !ticketExists {
			return tx.TefPAST_SEQ // tefNO_TICKET equivalent
		}
	} else {
		// Regular sequence-based inner transaction
		if common.Sequence != nil {
			if *common.Sequence < account.Sequence {
				return tx.TefPAST_SEQ
			}
			if *common.Sequence > account.Sequence {
				return tx.TerPRE_SEQ
			}
		}
	}

	// Create per-tx state table for isolation
	perTxTable := tx.NewApplyStateTable(ctx.View, ctx.TxHash, ctx.Config.LedgerSequence, ctx.Config.Rules)

	if isTicket {
		// Ticket-based: consume the ticket (delete it, adjust owner/ticket counts).
		// Sequence does NOT increment for ticket transactions.
		// Reference: rippled Transactor::consumeSeqProxy + ticketDelete
		ticketKey := keylet.Ticket(accountID, *common.TicketSequence)
		ownerDirKey := keylet.OwnerDir(accountID)

		// Remove ticket from owner directory
		state.DirRemove(perTxTable, ownerDirKey, 0, ticketKey.Key, true)
		if err := perTxTable.Erase(ticketKey); err != nil {
			return tx.TefINTERNAL
		}

		if account.OwnerCount > 0 {
			account.OwnerCount--
		}
		if account.TicketCount > 0 {
			account.TicketCount--
		}
	} else {
		// Increment sequence for regular sequence transactions
		account.Sequence++
	}

	// Check delegate permission if Delegate field is present on the inner tx.
	// This must happen after sequence increment because tec results still advance the sequence.
	// Reference: rippled Transactor::checkPermission — verifies that the delegate
	// account has a Delegate SLE granting permission for this tx type.
	var delegateResult tx.Result
	if common.Delegate != "" {
		delegateResult = checkDelegatePermission(ctx, accountID, innerTx)
	}

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

	// Apply the inner transaction (skip if delegate check failed)
	var result tx.Result
	if delegateResult != 0 {
		result = delegateResult
	} else if appliable, ok := innerTx.(tx.Appliable); ok {
		result = appliable.Apply(innerCtx)
	} else {
		result = tx.TesSUCCESS
	}

	if result.IsSuccess() {
		// Success: update account in per-tx table and commit all changes.
		// If the inner transaction deleted the account (e.g. AccountDelete),
		// the account SLE was already erased from the per-tx table, so we
		// must not try to update it — just commit the per-tx table as-is.
		accountExists, _ := perTxTable.Exists(accountKey)
		if accountExists {
			updatedData, err := state.SerializeAccountRoot(account)
			if err != nil {
				return tx.TefINTERNAL
			}
			if err := perTxTable.Update(accountKey, updatedData); err != nil {
				return tx.TefINTERNAL
			}
		}
		if _, err := perTxTable.Apply(); err != nil {
			return tx.TefINTERNAL
		}
	} else {
		// TEC/TEF/TER: sequence increments but transaction effects are discarded.
		// Update account state (sequence) directly in the parent view.
		updatedData, err := state.SerializeAccountRoot(account)
		if err != nil {
			return tx.TefINTERNAL
		}
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
	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return
	}
	ctx.Account.Balance = account.Balance
	ctx.Account.Sequence = account.Sequence
	ctx.Account.OwnerCount = account.OwnerCount
	ctx.Account.TicketCount = account.TicketCount
}

// checkDelegatePermission checks whether the Delegate on an inner tx has permission
// to execute the transaction on behalf of the account.
// Reference: rippled Transactor::checkPermission in Transactor.cpp
func checkDelegatePermission(ctx *tx.ApplyContext, accountID [20]byte, innerTx tx.Transaction) tx.Result {
	common := innerTx.GetCommon()
	delegateID, delegateErr := state.DecodeAccountID(common.Delegate)
	if delegateErr != nil {
		return tx.TecNO_DELEGATE_PERMISSION
	}
	delegateKeylet := keylet.DelegateKeylet(accountID, delegateID)
	delegateData, readErr := ctx.View.Read(delegateKeylet)
	if readErr != nil || delegateData == nil {
		return tx.TecNO_DELEGATE_PERMISSION
	}
	delegateEntry, parseErr := state.ParseDelegate(delegateData)
	if parseErr != nil {
		return tx.TecNO_DELEGATE_PERMISSION
	}
	// Check if the delegate SLE grants permission for this tx type.
	txTypeValue := uint32(innerTx.TxType())
	if !delegateEntry.HasTxPermission(txTypeValue) {
		return tx.TecNO_DELEGATE_PERMISSION
	}
	return 0 // success (no error)
}
