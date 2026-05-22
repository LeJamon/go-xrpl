package tx

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// Apply processes a transaction and applies it to the ledger.
// Pseudo-transactions (Amendment, SetFee, UNLModify) are rejected here;
// use ApplyPseudo() for pseudo-transaction application (e.g., during block processing).
// Reference: rippled passesLocalChecks() rejects pseudo-transactions submitted by users.
//
// Equivalent to ApplyWithContext(context.Background(), tx).
func (e *Engine) Apply(tx Transaction) ApplyResult {
	return e.ApplyWithContext(context.Background(), tx)
}

func (e *Engine) ApplyWithContext(ctx context.Context, tx Transaction) ApplyResult {
	// Reject pseudo-transactions — they cannot be submitted by users.
	// Reference: rippled passesLocalChecks() in NetworkOPs.cpp
	txType := tx.TxType()
	if txType.IsPseudoTransaction() {
		return ApplyResult{
			Result:  TemINVALID,
			Applied: false,
			Message: "pseudo-transactions cannot be submitted",
		}
	}

	account := tx.GetCommon().Account
	e.logger.Debug("apply",
		"txType", txType.String(),
		"account", account,
		"ledgerSeq", e.config.LedgerSequence,
	)

	// Step 1: Preflight checks (syntax validation)
	result := e.preflight(tx)
	if !result.IsSuccess() {
		e.logger.Debug("preflight failed",
			"txType", txType.String(),
			"account", account,
			"ter", result.String(),
		)
		return ApplyResult{
			Result:  result,
			Applied: false,
			Message: result.Message(),
		}
	}

	// Step 2: Compute transaction hash (needed by preclaim for tefALREADY check)
	txHash, err := computeTransactionHash(tx)
	if err != nil {
		return ApplyResult{
			Result:  TefINTERNAL,
			Applied: false,
			Message: fmt.Sprintf("failed to compute transaction hash: %v", err),
		}
	}

	// Step 3: Preclaim checks (validate against ledger state)
	result = e.preclaim(tx, txHash)
	if !result.IsSuccess() && !result.IsTec() {
		e.logger.Debug("preclaim failed",
			"txType", txType.String(),
			"account", account,
			"txHash", hex.EncodeToString(txHash[:]),
			"ter", result.String(),
		)
		return ApplyResult{
			Result:  result,
			Applied: false,
			Message: result.Message(),
		}
	}

	// likelyToClaimFee gate: when TapRETRY is set, tec results from
	// preclaim are NOT applied (no fee, no sequence consumed). The tx
	// stays in the retry queue for the next pass where TapRETRY is cleared.
	// Reference: rippled applySteps.h PreclaimResult —
	//   likelyToClaimFee = tesSUCCESS || (isTecClaim && !tapRETRY)
	if result.IsTec() && (e.config.ApplyFlags&TapRETRY) != 0 {
		return ApplyResult{
			Result:  result,
			Applied: false,
			Message: result.Message(),
		}
	}

	// Step 4: Calculate and apply fee
	fee := e.calculateFee(tx)

	// Step 5: Apply the transaction
	metadata := &Metadata{
		AffectedNodes:     make([]AffectedNode, 0),
		TransactionResult: TesSUCCESS,
	}

	if result.IsSuccess() {
		result = e.doApply(ctx, tx, metadata, txHash)
	} else if result.IsTec() {
		// Tec from preclaim: fee must still be deducted and sequence consumed,
		// but doApply() is NOT called — the transaction has no side effects.
		// We share the same recovery helpers used by doApply's own tec path
		// (consumeTicketForRecovery, writeRecoveryAccount, payDelegatedFeeOnTable);
		// the only difference is that the sandbox here is empty, so we skip
		// the "discard sandbox + replay deletions" steps.
		// Reference: rippled applySteps.cpp — preclaim tec with likelyToClaimFee=true
		// still enters Transactor::operator() which always applies fee/sequence.
		if r := e.commitPreclaimTec(ctx, tx, txHash, fee, metadata); r != TesSUCCESS {
			return ApplyResult{
				Result:  r,
				Applied: false,
				Message: r.Message(),
			}
		}
	}

	metadata.TransactionResult = result

	// Determine if the transaction is applied.
	// In rippled (Transactor.cpp:1108): applied = isTesSuccess(result).
	// For specific tec codes without tapRETRY (tecOVERSIZE, tecKILLED,
	// tecINCOMPLETE, tecEXPIRED, or isTecClaimHardFail), applied is
	// set to true (line 1215). With tapRETRY set, regular tec codes
	// are NOT applied — they return Retry for the next pass.
	// Reference: rippled Transactor.cpp operator() lines 1108-1216
	applied := result.IsApplied()
	if result.IsTec() && (e.config.ApplyFlags&TapRETRY) != 0 {
		// Retry pass: tec results are NOT applied. The doApply tec
		// recovery already committed fee+sequence to the table, but we
		// DON'T count this as applied so the conformance runner retries.
		// Note: the fee IS consumed (matching rippled where tec from
		// doApply still consumes fee even with tapRETRY, but the tx is
		// returned as Retry, not Success).
		applied = false
	}

	// Record fee as destroyed and assign TransactionIndex
	if applied {
		e.view.AdjustDropsDestroyed(drops.XRPAmount(fee))
		metadata.TransactionIndex = e.txCount.Add(1) - 1
	}

	e.logger.Debug("apply result",
		"txHash", hex.EncodeToString(txHash[:]),
		"ter", result.String(),
		"applied", applied,
		"fee", fee,
	)

	return ApplyResult{
		Result:   result,
		Applied:  applied,
		Fee:      fee,
		Metadata: metadata,
		Message:  result.Message(),
	}
}

// ApplyPseudo applies a pseudo-transaction (Amendment, SetFee, UNLModify) to the ledger.
// This is the public entry point for pseudo-transaction application, used by the block
// processor and test environment. Unlike Apply(), this does not reject pseudo-transactions.
// Reference: rippled Change.cpp — pseudo-txs are applied during consensus, not user submission.
//
// Equivalent to ApplyPseudoWithContext(context.Background(), tx).
func (e *Engine) ApplyPseudo(tx Transaction) ApplyResult {
	return e.applyPseudoTransaction(context.Background(), tx)
}

func (e *Engine) ApplyPseudoWithContext(ctx context.Context, tx Transaction) ApplyResult {
	return e.applyPseudoTransaction(ctx, tx)
}

// applyPseudoTransaction handles pseudo-transactions (Amendment, SetFee, UNLModify).
// These transactions have special handling:
// - No source account (account is zero/empty)
// - No fee (fee is 0)
// - No signature
// - No sequence number checks
// Reference: rippled Change.cpp preflight/preclaim/doApply
func (e *Engine) applyPseudoTransaction(reqCtx context.Context, tx Transaction) ApplyResult {
	rules := e.rules()

	// Preflight gates — mirror rippled Change::preflight (Change.cpp:36-80).
	// preflight0 in rippled rejects fee/sequence/signing checks before the
	// type-specific switch; replicated here for all pseudo-tx types. Run before
	// computing the transaction hash so malformed inputs surface as a typed tem*
	// result rather than a serialization-induced tefINTERNAL.
	if gate := pseudoPreflight(tx, rules); !gate.IsSuccess() {
		return ApplyResult{
			Result:   gate,
			Applied:  false,
			Fee:      0,
			Metadata: &Metadata{TransactionResult: gate},
			Message:  gate.Message(),
		}
	}

	// Preclaim gates — mirror rippled Change::preclaim (Change.cpp:82-140).
	// Pseudo-transactions are only legal against a closed ledger; per-type field
	// gating (e.g. XRPFees) runs through the PseudoPreclaim interface.
	if gate := e.pseudoPreclaim(tx, rules); !gate.IsSuccess() {
		return ApplyResult{
			Result:   gate,
			Applied:  false,
			Fee:      0,
			Metadata: &Metadata{TransactionResult: gate},
			Message:  gate.Message(),
		}
	}

	// Compute transaction hash
	txHash, err := computeTransactionHash(tx)
	if err != nil {
		return ApplyResult{
			Result:  TefINTERNAL,
			Applied: false,
			Message: fmt.Sprintf("failed to compute transaction hash: %v", err),
		}
	}

	// Create metadata
	metadata := &Metadata{
		AffectedNodes:     make([]AffectedNode, 0),
		TransactionResult: TesSUCCESS,
	}

	// Create ApplyStateTable to track changes
	table := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, rules)

	// Create a minimal ApplyContext for pseudo-transactions
	ctx := &ApplyContext{
		View:     table,
		Account:  nil, // No account for pseudo-transactions
		Config:   e.config,
		TxHash:   txHash,
		Metadata: metadata,
		Engine:   e,
		Log:      e.logger,
		Ctx:      reqCtx,
	}

	// Apply the transaction
	var result Result
	if appliable, ok := tx.(Appliable); ok {
		result = appliable.Apply(ctx)
	} else {
		result = TesSUCCESS
	}

	metadata.TransactionResult = result

	// Apply all tracked changes to the base view and generate metadata
	if result.IsSuccess() {
		generatedMeta, err := table.Apply()
		if err != nil {
			return ApplyResult{
				Result:   TefINTERNAL,
				Applied:  false,
				Metadata: metadata,
				Message:  fmt.Sprintf("failed to apply state changes: %v", err),
			}
		}
		metadata.AffectedNodes = generatedMeta.AffectedNodes
	}

	// Assign TransactionIndex for applied pseudo-transactions
	if result.IsApplied() {
		metadata.TransactionIndex = e.txCount.Add(1) - 1
	}

	return ApplyResult{
		Result:   result,
		Applied:  result.IsApplied(),
		Fee:      0, // Pseudo-transactions have no fee
		Metadata: metadata,
		Message:  result.Message(),
	}
}

// commitPreclaimTec handles the tec-from-preclaim path: the transaction was
// rejected by preclaim with a tec code, doApply() never ran, but the fee and
// sequence still need to be charged. Reuses the same recovery helpers that
// doApply's own tec path uses (consumeTicketForRecovery, writeRecoveryAccount,
// payDelegatedFeeOnTable) so the fee/seq commit semantics stay in lockstep.
// Reference: rippled applySteps.cpp — likelyToClaimFee tec still enters
// Transactor::operator() which calls reset(fee) before returning.
func (e *Engine) commitPreclaimTec(ctx context.Context, tx Transaction, txHash [32]byte, fee uint64, metadata *Metadata) Result {
	common := tx.GetCommon()
	accountID, _ := state.DecodeAccountID(common.Account)
	accountKey := keylet.Account(accountID)

	accountData, readErr := e.view.Read(accountKey)
	if readErr != nil || accountData == nil {
		return TefINTERNAL
	}
	recoveredAccount, parseErr := state.ParseAccountRoot(accountData)
	if parseErr != nil {
		return TefINTERNAL
	}

	st := &applyState{
		tx:          tx,
		common:      common,
		accountID:   accountID,
		accountKey:  accountKey,
		fee:         fee,
		isDelegated: common.Delegate != "",
		isTicket:    common.TicketSequence != nil,
		txHash:      txHash,
		metadata:    metadata,
		ctx:         ctx,
	}

	tecTable := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

	if st.isTicket {
		if r := e.consumeTicketForRecovery(st, tecTable); r != TesSUCCESS {
			return r
		}
	}
	if r := e.writeRecoveryAccount(st, tecTable, recoveredAccount); r != TesSUCCESS {
		return r
	}
	if r := e.payDelegatedFeeOnTable(st, tecTable); r != TesSUCCESS {
		return r
	}

	generatedMeta, applyErr := tecTable.Apply()
	if applyErr != nil {
		return TefINTERNAL
	}
	metadata.AffectedNodes = generatedMeta.AffectedNodes
	return TesSUCCESS
}
