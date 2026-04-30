package tx

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// Apply processes a transaction and applies it to the ledger.
// Pseudo-transactions (Amendment, SetFee, UNLModify) are rejected here;
// use ApplyPseudo() for pseudo-transaction application (e.g., during block processing).
// Reference: rippled passesLocalChecks() rejects pseudo-transactions submitted by users.
func (e *Engine) Apply(tx Transaction) ApplyResult {
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
			Message: "failed to compute transaction hash: " + err.Error(),
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
		result = e.doApply(tx, metadata, txHash)
	} else if result.IsTec() {
		// Tec from preclaim: fee must still be deducted and sequence consumed,
		// but doApply() is NOT called — the transaction has no side effects.
		// This mirrors the tec recovery path inside doApply() (lines 1785-2000)
		// but without needing to discard any doApply changes (since doApply never ran).
		// Reference: rippled applySteps.cpp — preclaim tec with likelyToClaimFee=true
		// still enters Transactor::operator() which always applies fee/sequence.
		tecCommon := tx.GetCommon()
		tecAccountID, _ := state.DecodeAccountID(tecCommon.Account)
		tecAccountKey := keylet.Account(tecAccountID)

		tecAccountData, tecReadErr := e.view.Read(tecAccountKey)
		if tecReadErr != nil || tecAccountData == nil {
			return ApplyResult{
				Result:  TefINTERNAL,
				Applied: false,
				Message: "tec-from-preclaim: failed to read account",
			}
		}

		tecAccount, tecParseErr := state.ParseAccountRoot(tecAccountData)
		if tecParseErr != nil {
			return ApplyResult{
				Result:  TefINTERNAL,
				Applied: false,
				Message: "tec-from-preclaim: failed to parse account",
			}
		}

		tecIsDelegated := tecCommon.Delegate != ""
		tecIsTicket := tecCommon.TicketSequence != nil

		// Deduct fee (unless delegated — delegate pays)
		if !tecIsDelegated {
			tecAccount.Balance -= fee
		}

		// Increment sequence (unless ticket-based)
		if !tecIsTicket && tecCommon.Sequence != nil {
			tecAccount.Sequence = *tecCommon.Sequence + 1
		}

		// Ticket consumption: decrement OwnerCount and TicketCount
		if tecIsTicket && tecAccount.OwnerCount > 0 {
			tecAccount.OwnerCount--
		}
		if tecIsTicket && tecAccount.TicketCount > 0 {
			tecAccount.TicketCount--
		}

		// Thread PreviousTxnID/PreviousTxnLgrSeq
		tecAccount.PreviousTxnID = txHash
		tecAccount.PreviousTxnLgrSeq = e.config.LedgerSequence

		// Update AccountTxnID if tracking is enabled
		{
			var zeroHash [32]byte
			if tecAccount.AccountTxnID != zeroHash {
				tecAccount.AccountTxnID = txHash
			}
		}

		// Create fresh ApplyStateTable for fee-only changes
		tecTable := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

		// Consume ticket through tecTable for proper metadata (DeletedNode + directory changes)
		if tecIsTicket {
			ticketKey := keylet.Ticket(tecAccountID, *tecCommon.TicketSequence)
			ownerDirKey := keylet.OwnerDir(tecAccountID)
			var ticketOwnerNode uint64
			if ticketData, ticketErr := tecTable.Read(ticketKey); ticketErr == nil && ticketData != nil {
				ticketOwnerNode = state.GetOwnerNode(ticketData)
			}
			state.DirRemove(tecTable, ownerDirKey, ticketOwnerNode, ticketKey.Key, true)
			if err := tecTable.Erase(ticketKey); err != nil {
				return ApplyResult{
					Result:  TefINTERNAL,
					Applied: false,
					Message: "tec-from-preclaim: failed to erase ticket",
				}
			}
		}

		// Serialize and write updated account to tecTable
		tecUpdatedData, tecSerErr := state.SerializeAccountRoot(tecAccount)
		if tecSerErr != nil {
			return ApplyResult{
				Result:  TefINTERNAL,
				Applied: false,
				Message: "tec-from-preclaim: failed to serialize account",
			}
		}
		if err := tecTable.Update(tecAccountKey, tecUpdatedData); err != nil {
			return ApplyResult{
				Result:  TefINTERNAL,
				Applied: false,
				Message: "tec-from-preclaim: failed to update account",
			}
		}

		// For delegated transactions, deduct fee from delegate's account
		if tecIsDelegated {
			delegateID, _ := state.DecodeAccountID(tecCommon.Delegate)
			delegateAccountKey := keylet.Account(delegateID)
			delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
			if delegateReadErr != nil || delegateAccountData == nil {
				return ApplyResult{
					Result:  TefINTERNAL,
					Applied: false,
					Message: "tec-from-preclaim: failed to read delegate account",
				}
			}
			delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
			if delegateParseErr != nil {
				return ApplyResult{
					Result:  TefINTERNAL,
					Applied: false,
					Message: "tec-from-preclaim: failed to parse delegate account",
				}
			}
			delegateAccount.Balance -= fee
			delegateAccount.PreviousTxnID = txHash
			delegateAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
			delegateData, delegateSerErr := state.SerializeAccountRoot(delegateAccount)
			if delegateSerErr != nil {
				return ApplyResult{
					Result:  TefINTERNAL,
					Applied: false,
					Message: "tec-from-preclaim: failed to serialize delegate account",
				}
			}
			if err := tecTable.Update(delegateAccountKey, delegateData); err != nil {
				return ApplyResult{
					Result:  TefINTERNAL,
					Applied: false,
					Message: "tec-from-preclaim: failed to update delegate account",
				}
			}
		}

		// Apply all tracked changes and generate metadata
		generatedMeta, applyErr := tecTable.Apply()
		if applyErr != nil {
			return ApplyResult{
				Result:  TefINTERNAL,
				Applied: false,
				Message: "tec-from-preclaim: failed to apply tecTable",
			}
		}
		metadata.AffectedNodes = generatedMeta.AffectedNodes
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
		metadata.TransactionIndex = e.txCount
		e.txCount++
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
func (e *Engine) ApplyPseudo(tx Transaction) ApplyResult {
	return e.applyPseudoTransaction(tx)
}

// applyPseudoTransaction handles pseudo-transactions (Amendment, SetFee, UNLModify).
// These transactions have special handling:
// - No source account (account is zero/empty)
// - No fee (fee is 0)
// - No signature
// - No sequence number checks
// Reference: rippled Change.cpp
func (e *Engine) applyPseudoTransaction(tx Transaction) ApplyResult {
	// Compute transaction hash
	txHash, err := computeTransactionHash(tx)
	if err != nil {
		return ApplyResult{
			Result:  TefINTERNAL,
			Applied: false,
			Message: "failed to compute transaction hash: " + err.Error(),
		}
	}

	// Create metadata
	metadata := &Metadata{
		AffectedNodes:     make([]AffectedNode, 0),
		TransactionResult: TesSUCCESS,
	}

	// Create ApplyStateTable to track changes
	table := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

	// Create a minimal ApplyContext for pseudo-transactions
	ctx := &ApplyContext{
		View:     table,
		Account:  nil, // No account for pseudo-transactions
		Config:   e.config,
		TxHash:   txHash,
		Metadata: metadata,
		Engine:   e,
		Log:      e.logger,
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
				Message:  "failed to apply state changes: " + err.Error(),
			}
		}
		metadata.AffectedNodes = generatedMeta.AffectedNodes
	}

	// Assign TransactionIndex for applied pseudo-transactions
	if result.IsApplied() {
		metadata.TransactionIndex = e.txCount
		e.txCount++
	}

	return ApplyResult{
		Result:   result,
		Applied:  result.IsApplied(),
		Fee:      0, // Pseudo-transactions have no fee
		Metadata: metadata,
		Message:  result.Message(),
	}
}
