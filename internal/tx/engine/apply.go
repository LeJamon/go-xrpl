package engine

import (
	"context"
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// Apply processes a transaction and applies it to the ledger.
// Pseudo-transactions (Amendment, SetFee, UNLModify) are rejected here;
// use ApplyPseudo() for pseudo-transaction application (e.g., during block processing).
// Reference: rippled passesLocalChecks() rejects pseudo-transactions submitted by users.
//
// Equivalent to ApplyWithContext(context.Background(), tx).
func (e *Engine) Apply(tx txcore.Transaction) txcore.ApplyResult {
	return e.ApplyWithContext(context.Background(), tx)
}

func (e *Engine) ApplyWithContext(ctx context.Context, tx txcore.Transaction) txcore.ApplyResult {
	// Reject pseudo-transactions — they cannot be submitted by users.
	// Reference: rippled passesLocalChecks() in NetworkOPs.cpp
	txType := tx.TxType()
	if txType.IsPseudoTransaction() {
		return txcore.ApplyResult{
			Result:  ter.TemINVALID,
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
		return txcore.ApplyResult{
			Result:  result,
			Applied: false,
			Message: result.Message(),
		}
	}

	// Step 2: Compute transaction hash (needed by preclaim for tefALREADY check)
	txHash, err := txcore.ComputeTransactionHash(tx)
	if err != nil {
		return txcore.ApplyResult{
			Result:  ter.TefINTERNAL,
			Applied: false,
			Message: fmt.Sprintf("failed to compute transaction hash: %v", err),
		}
	}

	// A zero transaction id is never valid. rippled rejects it in preflight0
	// (Transactor.cpp), the earliest point at which the id is known; the Go
	// engine computes the id here, so the equivalent guard runs before preclaim.
	if txHash == ([32]byte{}) {
		return txcore.ApplyResult{
			Result:  ter.TemINVALID,
			Applied: false,
			Message: "transaction id may not be zero",
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
		return txcore.ApplyResult{
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
	// TapFAIL_HARD for a preclaim tec is handled at the commit branch below;
	// the two flags are disjoint bits, so the order these gates run in is
	// immaterial (at most one fires).
	if result.IsTec() && (e.config.ApplyFlags&txcore.TapRETRY) != 0 {
		return txcore.ApplyResult{
			Result:  result,
			Applied: false,
			Message: result.Message(),
		}
	}

	// Step 4: Calculate and apply fee
	fee := e.calculateFee(tx)

	// Step 5: Apply the transaction
	metadata := &txcore.Metadata{
		AffectedNodes:     make([]txcore.AffectedNode, 0),
		TransactionResult: ter.TesSUCCESS,
	}

	if result.IsSuccess() {
		// doApply returns the fee actually charged. On the tec/invariant recovery
		// paths this is clamped to the payer's balance (rippled reset()); on the
		// success path it equals the declared fee.
		result, fee = e.doApply(ctx, tx, metadata, txHash)
	} else if result.IsTec() {
		// Tec from preclaim. When TapFAIL_HARD is set a tec result must do
		// nothing — no fee charged, no sequence consumed, not applied. Reference:
		// rippled Transactor.cpp:1114-1120 discards the context for any tec claim
		// under tapFAIL_HARD before the reset()/commit logic runs.
		if (e.config.ApplyFlags & txcore.TapFAIL_HARD) != 0 {
			return txcore.ApplyResult{
				Result:  result,
				Applied: false,
				Message: result.Message(),
			}
		}
		// Otherwise the fee must still be deducted and sequence consumed, but
		// doApply() is NOT called — the transaction has no side effects. We share
		// the same recovery helpers used by doApply's own tec path
		// (consumeTicketForRecovery, writeRecoveryAccount, payDelegatedFeeOnTable);
		// the only difference is that the sandbox here is empty, so we skip
		// the "discard sandbox + replay deletions" steps.
		// Reference: rippled applySteps.cpp — preclaim tec with likelyToClaimFee=true
		// still enters Transactor::operator() which always applies fee/sequence.
		committed, chargedFee := e.commitPreclaimTec(ctx, tx, txHash, fee, result, metadata)
		// commitPreclaimTec returns the original tec on a clean fee claim, or an
		// invariant-escalated code (tec/tefINVARIANT_FAILED) / tefINTERNAL when the
		// fee-only delta fails its invariant pass or cannot be written.
		if !committed.IsTec() {
			return txcore.ApplyResult{
				Result:  committed,
				Applied: false,
				Message: committed.Message(),
			}
		}
		result = committed
		fee = chargedFee
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
	if result.IsTec() && (e.config.ApplyFlags&txcore.TapFAIL_HARD) != 0 {
		// fail_hard: a tec from doApply was discarded (doApply returned fee 0
		// without committing its recovery table), so it must not count as
		// applied either. Reference: rippled Transactor.cpp:1114-1120 sets
		// applied = false for any tec claim under tapFAIL_HARD.
		applied = false
	}
	if result.IsTec() && (e.config.ApplyFlags&txcore.TapRETRY) != 0 && !isReapplyOnRetryTec(result) {
		// Retry pass: a generic tec is NOT applied (no fee, no sequence) — it
		// stays in the retry queue for a pass without TapRETRY. doApply already
		// returned fee 0 without committing its recovery table for this case.
		// The four work-on-tec codes are excluded here because doApply reapplied
		// them (fee + cleanup) even under TapRETRY, so they keep applied=true.
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

	return txcore.ApplyResult{
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
func (e *Engine) ApplyPseudo(tx txcore.Transaction) txcore.ApplyResult {
	return e.applyPseudoTransaction(context.Background(), tx)
}

func (e *Engine) ApplyPseudoWithContext(ctx context.Context, tx txcore.Transaction) txcore.ApplyResult {
	return e.applyPseudoTransaction(ctx, tx)
}

// applyPseudoTransaction handles pseudo-transactions (Amendment, SetFee, UNLModify).
// These transactions have special handling:
// - No source account (account is zero/empty)
// - No fee (fee is 0)
// - No signature
// - No sequence number checks
// Reference: rippled Change.cpp preflight/preclaim/doApply
func (e *Engine) applyPseudoTransaction(reqCtx context.Context, tx txcore.Transaction) txcore.ApplyResult {
	rules := e.rules()

	// Preflight gates — mirror rippled Change::preflight (Change.cpp:36-80).
	// preflight0 in rippled rejects fee/sequence/signing checks before the
	// type-specific switch; replicated here for all pseudo-tx types. Run before
	// computing the transaction hash so malformed inputs surface as a typed tem*
	// result rather than a serialization-induced tefINTERNAL.
	if gate := e.pseudoPreflight(tx, rules); !gate.IsSuccess() {
		return txcore.ApplyResult{
			Result:   gate,
			Applied:  false,
			Fee:      0,
			Metadata: &txcore.Metadata{TransactionResult: gate},
			Message:  gate.Message(),
		}
	}

	// Preclaim gates — mirror rippled Change::preclaim (Change.cpp:82-140).
	// Pseudo-transactions are only legal against a closed ledger; per-type field
	// gating (e.g. XRPFees) runs through the PseudoPreclaim interface.
	if gate := e.pseudoPreclaim(tx, rules); !gate.IsSuccess() {
		return txcore.ApplyResult{
			Result:   gate,
			Applied:  false,
			Fee:      0,
			Metadata: &txcore.Metadata{TransactionResult: gate},
			Message:  gate.Message(),
		}
	}

	// Compute transaction hash
	txHash, err := txcore.ComputeTransactionHash(tx)
	if err != nil {
		return txcore.ApplyResult{
			Result:  ter.TefINTERNAL,
			Applied: false,
			Message: fmt.Sprintf("failed to compute transaction hash: %v", err),
		}
	}

	// A zero transaction id is never valid (rippled preflight0, Transactor.cpp).
	if txHash == ([32]byte{}) {
		return txcore.ApplyResult{
			Result:   ter.TemINVALID,
			Applied:  false,
			Fee:      0,
			Metadata: &txcore.Metadata{TransactionResult: ter.TemINVALID},
			Message:  "transaction id may not be zero",
		}
	}

	// Create metadata
	metadata := &txcore.Metadata{
		AffectedNodes:     make([]txcore.AffectedNode, 0),
		TransactionResult: ter.TesSUCCESS,
	}

	// Create ApplyStateTable to track changes
	table := applystate.NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, rules)

	// Create a minimal ApplyContext for pseudo-transactions
	ctx := &txcore.ApplyContext{
		View:            table,
		Account:         nil, // No account for pseudo-transactions
		Config:          e.config,
		TxHash:          txHash,
		Metadata:        metadata,
		InnerInvariants: e,
		Log:             e.logger,
		Ctx:             reqCtx,
	}

	// Apply the transaction
	var result ter.Result
	if appliable, ok := tx.(txcore.Appliable); ok {
		result = appliable.Apply(ctx)
	} else {
		result = ter.TesSUCCESS
	}

	metadata.TransactionResult = result

	// Apply all tracked changes to the base view and generate metadata
	if result.IsSuccess() {
		generatedMeta, err := table.Apply()
		if err != nil {
			return txcore.ApplyResult{
				Result:   ter.TefINTERNAL,
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

	return txcore.ApplyResult{
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
func (e *Engine) commitPreclaimTec(ctx context.Context, tx txcore.Transaction, txHash [32]byte, fee uint64, origResult ter.Result, metadata *txcore.Metadata) (ter.Result, uint64) {
	common := tx.GetCommon()
	accountID, _ := state.DecodeAccountID(common.Account)
	accountKey := keylet.Account(accountID)

	accountData, readErr := e.view.Read(accountKey)
	if readErr != nil || accountData == nil {
		return ter.TefINTERNAL, 0
	}
	recoveredAccount, parseErr := state.ParseAccountRoot(accountData)
	if parseErr != nil {
		return ter.TefINTERNAL, 0
	}

	st := &applyState{
		tx:                  tx,
		common:              common,
		accountID:           accountID,
		accountKey:          accountKey,
		account:             recoveredAccount,
		originalAccountData: accountData,
		fee:                 fee,
		chargedFee:          fee,
		isDelegated:         common.Delegate != "",
		isTicket:            common.TicketSequence != nil,
		txHash:              txHash,
		metadata:            metadata,
		ctx:                 ctx,
	}

	tecTable := applystate.NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

	if st.isTicket {
		if r := e.consumeTicketForRecovery(st, tecTable); r != ter.TesSUCCESS {
			return r, 0
		}
	}
	if r := e.writeRecoveryAccount(st, tecTable, recoveredAccount); r != ter.TesSUCCESS {
		return r, 0
	}
	if r := e.payDelegatedFeeOnTable(st, tecTable); r != ter.TesSUCCESS {
		return r, 0
	}

	// Invariant check on the fee-only delta before committing. rippled runs
	// checkInvariants for every applied result, including a tec that claims a
	// fee straight out of preclaim without ever entering doApply (Transactor.cpp
	// :1218-1238). A violation escalates to tec/tefINVARIANT_FAILED via the same
	// two-pass reset the doApply tec path uses. The fee-only state this builds is
	// exactly the reset() state, so applyInvariantViolation's semantics fit.
	if r, handled := e.runInvariantsOnTable(st, origResult, tecTable); handled {
		return r, st.chargedFee
	}

	generatedMeta, applyErr := tecTable.Apply()
	if applyErr != nil {
		return ter.TefINTERNAL, 0
	}
	metadata.AffectedNodes = generatedMeta.AffectedNodes
	return origResult, st.chargedFee
}
