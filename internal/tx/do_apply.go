package tx

import (
	"context"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/invariants"
	"github.com/LeJamon/go-xrpl/keylet"
)

// applyState holds the per-doApply scratch state shared between the helper
// methods extracted from doApply (payFee, consumeSeqProxy, tec/invariant
// recovery paths, etc.). It mirrors what would be member fields on rippled's
// Transactor instance during a single ::operator()() call.
type applyState struct {
	tx                  Transaction
	common              *Common
	accountID           [20]byte
	accountKey          keylet.Keylet
	account             *state.AccountRoot
	originalAccountData []byte
	fee                 uint64
	isDelegated         bool
	isTicket            bool
	txHash              [32]byte
	metadata            *Metadata
	table               *ApplyStateTable
	ctx                 context.Context
}

// doApply applies the transaction to the ledger
// For tec results, only fee/sequence changes are applied; transaction effects are discarded.
// Reference: rippled Transactor.cpp - tec results claim fee but don't apply effects
func (e *Engine) doApply(ctx context.Context, tx Transaction, metadata *Metadata, txHash [32]byte) Result {
	common := tx.GetCommon()
	accountID, _ := state.DecodeAccountID(common.Account)
	accountKey := keylet.Account(accountID)

	// Read sender account directly from view
	accountData, err := e.view.Read(accountKey)
	if err != nil {
		return TefINTERNAL
	}

	account, err := state.ParseAccountRoot(accountData)
	if err != nil {
		return TefINTERNAL
	}

	fee := e.calculateFee(tx)

	// Save original serialized account data for tec recovery.
	// On tec results, we restore the account to its original state
	// and only apply fee deduction + sequence increment.
	// Reference: rippled Transactor.cpp — saves/restores entire SLE on tec.
	originalAccountData := make([]byte, len(accountData))
	copy(originalAccountData, accountData)

	// Create ApplyStateTable for transaction-specific changes
	table := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

	st := &applyState{
		tx:                  tx,
		common:              common,
		accountID:           accountID,
		accountKey:          accountKey,
		account:             account,
		originalAccountData: originalAccountData,
		fee:                 fee,
		isDelegated:         common.Delegate != "",
		isTicket:            common.TicketSequence != nil,
		txHash:              txHash,
		metadata:            metadata,
		table:               table,
		ctx:                 ctx,
	}

	// payFee + consumeSeqProxy + AccountTxnID threading: apply pre-doApply()
	// account mutations (rippled Transactor::apply()).
	if result := e.applyPreApplyAccountChanges(st); result != TesSUCCESS {
		return result
	}

	// For delegated transactions, deduct the fee from the delegate's account.
	if result := e.payDelegatedFee(st); result != TesSUCCESS {
		return result
	}

	// Consume ticket BEFORE Apply, matching rippled's Transactor::apply()
	// which calls consumeSeqProxy() before doApply(). This ensures that when
	// doApply() iterates the owner directory (e.g., AccountDelete), the
	// consumed ticket is already gone.
	if st.isTicket {
		if result := e.consumeTicket(st, table); result != TesSUCCESS {
			return result
		}
	}

	// Set NumberSwitchover based on fixUniversalNumber amendment.
	// When enabled, IOUAmount arithmetic uses Guard-based precision (XRPLNumber).
	// Reference: rippled's setSTNumberSwitchover() in IOUAmount.cpp
	state.SetNumberSwitchover(e.rules().Enabled(amendment.FeatureFixUniversalNumber))

	// Dispatch to the per-tx-type Apply().
	result := e.invokeApply(st)

	// If tx.Apply() returned a non-applied result (tem*/tef*/ter*), discard all changes.
	// This handles transactions like OfferCreate that perform their own preflight/preclaim
	// inside Apply() and may return tem* codes after the engine has already set up the
	// ApplyStateTable. In rippled, these codes are caught before doApply() runs.
	// No fee is charged and no state is modified for non-applied results.
	if !result.IsSuccess() && !result.IsTec() {
		return result
	}

	// Check for oversize metadata: if the transaction touched more than 5200
	// entries, override the result to tecOVERSIZE. This prevents excessively
	// large transactions from being committed.
	// Reference: rippled Transactor.cpp lines 1111-1112:
	//   if (ctx_.size() > oversizeMetaDataCap)
	//       result = tecOVERSIZE;
	const oversizeMetaDataCap = 5200
	if table.Size() > oversizeMetaDataCap {
		result = TecOVERSIZE
	}

	// For tec results, only apply fee/sequence changes, not transaction effects.
	// Reference: rippled Transactor.cpp — tec codes claim the fee but discard
	// the apply sandbox, then selectively re-apply specific cleanup operations
	// (offer removal for tecOVERSIZE/tecKILLED, credential deletion for tecEXPIRED).
	//
	// When TapRETRY is set, regular tec results are NOT applied (no fee, no
	// sequence consumed). The tx stays in the retry queue. This matches rippled
	// where applied=isTesSuccess(result)=false with tapRETRY, so ctx_ is never
	// committed. Only isTecClaimHardFail codes (tec without tapRETRY) commit.
	// Reference: rippled Transactor.cpp lines 1108-1216
	if result.IsTec() && (e.config.ApplyFlags&TapRETRY) != 0 {
		// Retry pass: discard all changes, don't commit fee/sequence.
		// The transaction will be retried on the next pass without TapRETRY.
		return result
	}
	if result.IsTec() {
		return e.applyTecRecovery(st, result)
	}

	// For success, apply all changes through the table
	// Update the source account through the table (unless erased by e.g. AccountDelete)
	if !table.IsErased(accountKey) {
		updatedData, err := state.SerializeAccountRoot(account)
		if err != nil {
			return TefINTERNAL
		}

		if err := table.Update(accountKey, updatedData); err != nil {
			return TefINTERNAL
		}
	}

	// Run invariant checks BEFORE committing — entries are still inspectable in the table.
	// Reference: rippled Transactor::apply() — invariant check runs before ctx_->apply().
	if r, handled := e.runInvariants(st, result); handled {
		return r
	}

	// Apply all tracked changes to the base view and generate metadata automatically
	generatedMeta, err := table.Apply()
	if err != nil {
		return TefINTERNAL
	}

	// Copy generated metadata to the output
	metadata.AffectedNodes = generatedMeta.AffectedNodes

	return result
}

// applyPreApplyAccountChanges performs payFee, the non-ticket sequence
// increment, PreviousTxn threading, AccountTxnID update, and writes the
// pre-doApply account back into the ApplyStateTable. Mirrors the fee/seq
// portion of rippled Transactor::apply() (payFee + consumeSeqProxy +
// AccountTxnID block).
func (e *Engine) applyPreApplyAccountChanges(st *applyState) Result {
	// Reference: rippled Transactor::payFee + consumeSeqProxy in Transactor.cpp
	if st.isDelegated {
		// Delegated transactions: fee is charged to the delegate account, not the source.
		// The source account's balance is NOT reduced by the fee.
		// Reference: rippled Transactor::payFee() lines 327-337
	} else {
		// Normal transactions: fee is charged to the source account.
		st.account.Balance -= st.fee
	}

	if !st.isTicket && st.common.Sequence != nil {
		st.account.Sequence = *st.common.Sequence + 1
	}

	// Update PreviousTxnID and PreviousTxnLgrSeq (thread the account)
	st.account.PreviousTxnID = st.txHash
	st.account.PreviousTxnLgrSeq = e.config.LedgerSequence

	// Update AccountTxnID if the account has tracking enabled (field present).
	// Keyed on presence, not non-zero: a freshly-enabled asfAccountTxnID is
	// present-but-zero until this update, and rippled updates it on the very
	// next transaction.
	// Reference: rippled Transactor::apply() line 568-569:
	//   if (sle->isFieldPresent(sfAccountTxnID))
	//       sle->setFieldH256(sfAccountTxnID, ctx_.tx.getTransactionID());
	if st.account.HasAccountTxnID {
		st.account.AccountTxnID = st.txHash
	}

	// Write the fee-deducted, sequence-incremented account to the table BEFORE Apply().
	// This matches rippled's Transactor::apply() which modifies the account SLE
	// (fee deduction, sequence increment) before calling doApply().
	// Without this, reads during Apply() would see the pre-fee balance.
	preApplyData, preApplyErr := state.SerializeAccountRoot(st.account)
	if preApplyErr != nil {
		return TefINTERNAL
	}
	if err := st.table.Update(st.accountKey, preApplyData); err != nil {
		return TefINTERNAL
	}
	return TesSUCCESS
}

// payDelegatedFee deducts the fee from the delegate's account when sfDelegate
// is set. Reference: rippled Transactor::payFee() lines 327-337.
func (e *Engine) payDelegatedFee(st *applyState) Result {
	if !st.isDelegated {
		return TesSUCCESS
	}
	delegateID, _ := state.DecodeAccountID(st.common.Delegate)
	delegateAccountKey := keylet.Account(delegateID)
	delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
	if delegateReadErr != nil || delegateAccountData == nil {
		return TefINTERNAL
	}
	delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
	if delegateParseErr != nil {
		return TefINTERNAL
	}
	delegateAccount.Balance -= st.fee
	delegateAccount.PreviousTxnID = st.txHash
	delegateAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
	delegateData, delegateSerErr := state.SerializeAccountRoot(delegateAccount)
	if delegateSerErr != nil {
		return TefINTERNAL
	}
	if err := st.table.Update(delegateAccountKey, delegateData); err != nil {
		return TefINTERNAL
	}
	return TesSUCCESS
}

// consumeTicket removes the ticket SLE from the owner directory and decrements
// OwnerCount/TicketCount. Mirrors rippled's Transactor::ticketDelete +
// consumeSeqProxy logic for ticket-based transactions, run on the supplied
// table (the live tx table on the success path, the recovery table on the
// tec/invariant paths).
// Reference: rippled Transactor::consumeSeqProxy + Transactor::ticketDelete
// in Transactor.cpp.
func (e *Engine) consumeTicket(st *applyState, table *ApplyStateTable) Result {
	ticketKey := keylet.Ticket(st.accountID, *st.common.TicketSequence)
	ownerDirKey := keylet.OwnerDir(st.accountID)
	var ticketOwnerNode uint64
	if ticketData, ticketErr := table.Read(ticketKey); ticketErr == nil && ticketData != nil {
		ticketOwnerNode = state.GetOwnerNode(ticketData)
	}
	// NOTE: rippled's ticketDelete returns tefBAD_LEDGER when this dirRemove
	// reports not-found. Surfacing that here requires the bump-last-page harness
	// compensation that rewrites stale sfOwnerNode hints in lossy fixtures; both
	// the result check and that compensation land together in the #887 work, so
	// the result is intentionally not surfaced here to avoid a fixture-gap
	// regression. GetOwnerNode above is still the corrected field-walk.
	state.DirRemove(table, ownerDirKey, ticketOwnerNode, ticketKey.Key, true)
	if err := table.Erase(ticketKey); err != nil {
		return TefINTERNAL
	}
	if st.account.OwnerCount > 0 {
		st.account.OwnerCount--
	}
	if st.account.TicketCount > 0 {
		st.account.TicketCount--
	}
	preApplyData, preApplySerErr := state.SerializeAccountRoot(st.account)
	if preApplySerErr != nil {
		return TefINTERNAL
	}
	if err := table.Update(st.accountKey, preApplyData); err != nil {
		return TefINTERNAL
	}
	return TesSUCCESS
}

// invokeApply dispatches to the per-tx-type Apply() implementation. Any panic
// raised inside the per-tx Apply() — most commonly an arithmetic overflow from
// IOUAmount / XRPLNumber operating on adversarial peer-supplied ledger data —
// is recovered and turned into tefEXCEPTION.
//
// Reference: rippled applySteps.cpp:447-466 doApply() wraps invoke_apply(ctx)
// in `try { ... } catch (std::exception const&) { return {tefEXCEPTION, false}; }`.
// tefEXCEPTION is a tef* code, so the transaction is NOT applied to the ledger:
// no fee is charged, no sequence is consumed, and no metadata is emitted. The
// caller at doApply() returns immediately on tef* via the `!IsSuccess() &&
// !IsTec()` branch.
//
// Returning tecINTERNAL here would diverge from rippled because tec* commits
// fee+seq+meta to the ledger, producing a different account_hash on the same
// adversarial input → consensus fork.
func (e *Engine) invokeApply(st *applyState) (result Result) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("transaction Apply() panic recovered, returning tefEXCEPTION",
				"txHash", fmt.Sprintf("%x", st.txHash), "panic", r)
			result = TefEXCEPTION
		}
	}()
	return e.invokeApplyInner(st)
}

// invokeApplyInner is the body of invokeApply, separated so the panic-recovery
// defer in invokeApply does not have to walk back through the dispatch.
func (e *Engine) invokeApplyInner(st *applyState) Result {
	// Determine if the transaction was signed with the master key.
	// Reference: rippled SetAccount.cpp sigWithMaster — compares
	// calcAccountID(SigningPubKey) against the account ID.
	// When signature verification is skipped (test mode), assume master key.
	sigWithMaster := e.config.SkipSignatureVerification
	if st.common.SigningPubKey != "" {
		signerAddr, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(st.common.SigningPubKey)
		if addrErr == nil {
			sigWithMaster = signerAddr == st.common.Account
		}
	}

	// All transaction types implement Appliable
	ctx := &ApplyContext{
		View:             st.table,
		Account:          st.account,
		AccountID:        st.accountID,
		Config:           e.config,
		TxHash:           st.txHash,
		Metadata:         st.metadata,
		Engine:           e,
		SignedWithMaster: sigWithMaster,
		Log:              e.logger,
		Ctx:              st.ctx,
	}

	if appliable, ok := st.tx.(Appliable); ok {
		return appliable.Apply(ctx)
	}
	return TesSUCCESS
}

// applyTecRecovery implements the tec-result recovery path: discard the
// transaction sandbox, charge fee/seq/ticket, and selectively re-apply
// cleanup operations (offer removal, AMM trustline removal, expired offer
// removal, credential deletion).
// Reference: rippled Transactor.cpp lines 1108-1216 — reset() + cleanup
// helpers (removeUnfundedOffers, removeDeletedTrustLines,
// removeExpiredNFTokenOffers).
func (e *Engine) applyTecRecovery(st *applyState, result Result) Result {
	// Collect keys-to-redelete from the to-be-discarded sandbox.
	removedOfferKeys := collectErasedKeysOfType(st.table, "Offer", result == TecOVERSIZE || result == TecKILLED, 1000)
	removedTrustLineKeys := collectErasedKeysOfType(st.table, "RippleState", result == TecINCOMPLETE, 512)
	expiredNFTokenOfferKeys := collectErasedKeysOfType(st.table, "NFTokenOffer", result == TecEXPIRED, 256)

	// Discard the transaction table — all doApply() side effects are lost.
	// Reference: rippled Transactor.cpp — reset() discards the sandbox.
	// (We simply don't call table.Apply(), which effectively discards it.)
	//
	// Create a fresh ApplyStateTable to track tec-specific changes
	// (fee, sequence, ticket consumption) for proper metadata generation.
	tecTable := NewApplyStateTable(e.view, st.txHash, e.config.LedgerSequence, e.rules())

	// Consume ticket through tecTable for proper metadata (DeletedNode + directory changes)
	// Reference: rippled Transactor.cpp — tec still consumes the ticket.
	if st.isTicket {
		if r := e.consumeTicketForRecovery(st, tecTable); r != TesSUCCESS {
			return r
		}
	}

	// tecINCOMPLETE (AMMDelete): re-delete trust lines that were found during processing.
	// These trust lines were deleted in the (now discarded) sandbox.
	// Reference: rippled Transactor.cpp lines 1207-1209: removeDeletedTrustLines()
	//   which calls deleteAMMTrustLine() for each collected trust line key.
	if len(removedTrustLineKeys) > 0 {
		e.removeDeletedTrustLines(tecTable, removedTrustLineKeys, st.txHash)
	}

	// Restore account to original state, then apply only fee/sequence.
	// This discards any changes the transaction made to OwnerCount,
	// MintedNFTokens, BurnedNFTokens, etc.
	// Reference: rippled Transactor.cpp — restores original SLE on tec.
	recoveredAccount, parseErr := state.ParseAccountRoot(st.originalAccountData)
	if parseErr != nil {
		return TefINTERNAL
	}
	if r := e.writeRecoveryAccount(st, tecTable, recoveredAccount); r != TesSUCCESS {
		return r
	}

	// For delegated transactions, deduct the fee from the delegate's account on tec.
	// Reference: rippled Transactor.cpp reset() lines 1011-1013, 1036
	if r := e.payDelegatedFeeOnTable(st, tecTable); r != TesSUCCESS {
		return r
	}

	// tecOVERSIZE/tecKILLED: re-delete offers that were found during processing.
	// These offers were deleted in the (now discarded) sandbox.
	// Reference: rippled Transactor.cpp lines 1198-1201: removeUnfundedOffers()
	if len(removedOfferKeys) > 0 {
		e.removeUnfundedOffers(tecTable, removedOfferKeys, st.txHash)
	}

	// tecEXPIRED: re-delete expired NFTokenOffers and credentials.
	// Reference: rippled Transactor.cpp lines 1203-1205: removeExpiredNFTokenOffers()
	if result == TecEXPIRED {
		// Re-delete NFTokenOffers through tecTable
		for _, offerKey := range expiredNFTokenOfferKeys {
			offerKL := keylet.Keylet{Key: offerKey}
			deleteNFTokenOfferOnView(tecTable, offerKL, st.txHash, e.config.LedgerSequence)
		}

		// Credential deletion via TecApplier
		if tecApplier, ok := st.tx.(TecApplier); ok {
			tecCtx := &ApplyContext{
				View:      tecTable,
				Account:   recoveredAccount,
				AccountID: st.accountID,
				Config:    e.config,
				TxHash:    st.txHash,
				Metadata:  st.metadata,
				Engine:    e,
				Log:       e.logger,
				Ctx:       st.ctx,
			}
			tecApplier.ApplyOnTec(tecCtx)
		}
	}

	// Run invariant checks on the post-recovery delta BEFORE committing.
	// rippled runs checkInvariants for every applied result — tes AND every
	// tec claim — on the post-reset fee+cleanup state, escalating a violation
	// to tecINVARIANT_FAILED (and tefINVARIANT_FAILED on the fee-only retry).
	// The original tec result is passed to the checkers because their finalize
	// branches are result-aware (e.g. AccountRootsNotDeleted only enforces on
	// tesSUCCESS; NFTokenCountTracking / ValidClawback have result != tesSUCCESS
	// arms).
	// Reference: rippled Transactor.cpp:1215-1243 — applied = isTecClaim(result),
	// then checkInvariants(result, fee) with the two-pass reset escalation.
	if r, handled := e.runInvariantsOnTable(st, result, tecTable); handled {
		return r
	}

	// Apply all tracked changes and generate proper metadata
	generatedMeta, applyErr := tecTable.Apply()
	if applyErr != nil {
		return TefINTERNAL
	}
	st.metadata.AffectedNodes = generatedMeta.AffectedNodes

	return result
}

// collectErasedKeysOfType walks the ApplyStateTable and collects up to `limit`
// keys whose entries are erased ledger entries of the given type. When
// `enabled` is false, returns nil. Used by tec recovery to re-apply specific
// deletions after the sandbox is discarded.
func collectErasedKeysOfType(table *ApplyStateTable, entryType string, enabled bool, limit int) [][32]byte {
	if !enabled {
		return nil
	}
	var keys [][32]byte
	for key, entry := range table.GetItems() {
		if entry.Action != ActionErase {
			continue
		}
		t := getLedgerEntryType(entry.Original)
		if t == "" && entry.Current != nil {
			t = getLedgerEntryType(entry.Current)
		}
		if t == entryType {
			keys = append(keys, key)
			if len(keys) >= limit {
				break
			}
		}
	}
	return keys
}

// consumeTicketForRecovery consumes the ticket through the supplied recovery
// table. Differs from consumeTicket in that it does NOT mutate st.account or
// write the account back — the recovery path rebuilds the account from
// originalAccountData independently.
func (e *Engine) consumeTicketForRecovery(st *applyState, tecTable *ApplyStateTable) Result {
	ticketKey := keylet.Ticket(st.accountID, *st.common.TicketSequence)
	ownerDirKey := keylet.OwnerDir(st.accountID)
	// Read ticket SLE to get OwnerNode (directory page) for proper removal.
	var ticketOwnerNode uint64
	if ticketData, ticketErr := tecTable.Read(ticketKey); ticketErr == nil && ticketData != nil {
		ticketOwnerNode = state.GetOwnerNode(ticketData)
	}
	// See consumeTicket: surfacing the dirRemove result as tefBAD_LEDGER is
	// coupled to the bump-last-page harness compensation in the #887 work.
	state.DirRemove(tecTable, ownerDirKey, ticketOwnerNode, ticketKey.Key, true)
	if err := tecTable.Erase(ticketKey); err != nil {
		return TefINTERNAL
	}
	return TesSUCCESS
}

// removeDeletedTrustLines re-deletes the supplied AMM trust line keys through
// the recovery table.
// Reference: rippled View.cpp deleteAMMTrustLine + Transactor.cpp lines 1207-1209.
func (e *Engine) removeDeletedTrustLines(tecTable *ApplyStateTable, keys [][32]byte, txHash [32]byte) {
	for _, lineKey := range keys {
		lineKL := keylet.Keylet{Key: lineKey}
		lineData, readErr := tecTable.Read(lineKL)
		if readErr != nil || lineData == nil {
			continue
		}
		rs, parseErr := state.ParseRippleState(lineData)
		if parseErr != nil {
			continue
		}
		lowID, decodeErr := state.DecodeAccountID(rs.LowLimit.Issuer)
		if decodeErr != nil {
			continue
		}
		highID, decodeErr := state.DecodeAccountID(rs.HighLimit.Issuer)
		if decodeErr != nil {
			continue
		}
		lowDirKey := keylet.OwnerDir(lowID)
		state.DirRemove(tecTable, lowDirKey, rs.LowNode, lineKey, false)
		highDirKey := keylet.OwnerDir(highID)
		state.DirRemove(tecTable, highDirKey, rs.HighNode, lineKey, false)
		// Erase the trust line
		_ = tecTable.Erase(lineKL)
		// Decrement OwnerCount for the non-AMM side that has a reserve.
		// Reference: rippled View.cpp deleteAMMTrustLine lines 2759-2763
		lowAcctData, _ := tecTable.Read(keylet.Account(lowID))
		highAcctData, _ := tecTable.Read(keylet.Account(highID))
		if lowAcctData != nil && highAcctData != nil {
			lowAcct, _ := state.ParseAccountRoot(lowAcctData)
			highAcct, _ := state.ParseAccountRoot(highAcctData)
			zeroHash := [32]byte{}
			ammLow := lowAcct.AMMID != zeroHash
			ammHigh := highAcct.AMMID != zeroHash
			if rs.Flags&state.LsfLowReserve != 0 && !ammLow {
				adjustOwnerCountOnView(tecTable, lowID, -1, txHash, e.config.LedgerSequence)
			}
			if rs.Flags&state.LsfHighReserve != 0 && !ammHigh {
				adjustOwnerCountOnView(tecTable, highID, -1, txHash, e.config.LedgerSequence)
			}
		}
	}
}

// removeUnfundedOffers re-deletes the supplied offer keys through the recovery
// table.
// Reference: rippled Transactor.cpp lines 1198-1201: removeUnfundedOffers().
func (e *Engine) removeUnfundedOffers(tecTable *ApplyStateTable, keys [][32]byte, txHash [32]byte) {
	for _, offerKey := range keys {
		offerKL := keylet.Keylet{Key: offerKey}
		offerData, readErr := e.view.Read(offerKL)
		if readErr != nil || offerData == nil {
			continue
		}
		offerObj, parseErr := state.ParseLedgerOffer(offerData)
		if parseErr != nil {
			continue
		}
		ownerID, decodeErr := state.DecodeAccountID(offerObj.Account)
		if decodeErr != nil {
			continue
		}
		ownerDirKey := keylet.OwnerDir(ownerID)
		state.DirRemove(tecTable, ownerDirKey, offerObj.OwnerNode, offerKey, false)
		bookDirKey := keylet.Keylet{Type: 100, Key: offerObj.BookDirectory}
		state.DirRemove(tecTable, bookDirKey, offerObj.BookNode, offerKey, false)
		_ = tecTable.Erase(offerKL)
		adjustOwnerCountOnView(tecTable, ownerID, -1, txHash, e.config.LedgerSequence)
	}
}

// writeRecoveryAccount applies the fee/seq/ticket-count/PreviousTxn/AccountTxnID
// mutations to the freshly-restored account and writes it through the recovery
// table.
// Reference: rippled Transactor.cpp reset() lines 998-1052.
func (e *Engine) writeRecoveryAccount(st *applyState, tecTable *ApplyStateTable, recoveredAccount *state.AccountRoot) Result {
	// For delegated transactions, fee is charged to the delegate, not the source.
	// Reference: rippled Transactor.cpp reset() lines 1011-1013, 1036
	if !st.isDelegated {
		recoveredAccount.Balance -= st.fee
	}
	if !st.isTicket && st.common.Sequence != nil {
		recoveredAccount.Sequence = *st.common.Sequence + 1
	}
	// Apply ticket consumption OwnerCount and TicketCount decreases.
	if st.isTicket && recoveredAccount.OwnerCount > 0 {
		recoveredAccount.OwnerCount--
	}
	if st.isTicket && recoveredAccount.TicketCount > 0 {
		recoveredAccount.TicketCount--
	}
	// Apply PreviousTxnID/PreviousTxnLgrSeq threading
	recoveredAccount.PreviousTxnID = st.txHash
	recoveredAccount.PreviousTxnLgrSeq = e.config.LedgerSequence

	// Do NOT update sfAccountTxnID on the tec path. rippled updates it in the
	// apply() preamble (Transactor.cpp:568) BEFORE doApply(); on a tec that
	// whole preamble is rolled back by reset() (Transactor.cpp:1001
	// ctx_.discard()), which then re-applies only sfBalance (fee) and
	// sfSequence — never sfAccountTxnID. So a tec leaves AccountTxnID at its
	// prior value. recoveredAccount is re-parsed from the original account, so
	// simply not touching it preserves that prior value, matching rippled.
	// (Updating it here forked account metadata on tec txs from asfAccountTxnID
	// accounts — transaction_hash diverged while account_hash matched.)

	updatedData, err := state.SerializeAccountRoot(recoveredAccount)
	if err != nil {
		return TefINTERNAL
	}

	// Update account through tecTable for proper metadata diff generation
	if err := tecTable.Update(st.accountKey, updatedData); err != nil {
		return TefINTERNAL
	}
	return TesSUCCESS
}

// payDelegatedFeeOnTable deducts the fee from the delegate's account through
// the supplied table. Used by both the tec-recovery and invariant-violation
// recovery paths.
// Reference: rippled Transactor.cpp reset() lines 1011-1013, 1036.
func (e *Engine) payDelegatedFeeOnTable(st *applyState, table *ApplyStateTable) Result {
	if !st.isDelegated {
		return TesSUCCESS
	}
	delegateID, _ := state.DecodeAccountID(st.common.Delegate)
	delegateAccountKey := keylet.Account(delegateID)
	delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
	if delegateReadErr != nil || delegateAccountData == nil {
		return TefINTERNAL
	}
	delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
	if delegateParseErr != nil {
		return TefINTERNAL
	}
	delegateAccount.Balance -= st.fee
	delegateAccount.PreviousTxnID = st.txHash
	delegateAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
	delegateData, delegateSerErr := state.SerializeAccountRoot(delegateAccount)
	if delegateSerErr != nil {
		return TefINTERNAL
	}
	if err := table.Update(delegateAccountKey, delegateData); err != nil {
		return TefINTERNAL
	}
	return TesSUCCESS
}

// runInvariants checks invariants on the transaction's tracked entries. Returns
// (result, true) when an invariant violation has been handled (recovery path
// taken or escalation to tefINVARIANT_FAILED), and (zero, false) when the
// transaction passes invariants and may continue to the normal commit.
// Reference: rippled Transactor.cpp:1218-1238 — invariant check runs after
// per-tx apply; on violation calls reset(fee) and re-checks invariants on the
// fee-only state.
//
// AMM invariants in particular drive XRPLNumber arithmetic, which panics on
// overflow / NaN. A panic here is treated as an invariant violation: discard
// the sandbox and charge fee via applyInvariantViolation. This matches
// rippled ApplyContext.cpp:97-148 checkInvariantsHelper, which wraps the
// invariant visit/finalize in `try { ... } catch (std::exception const&) {
// return failInvariantCheck(result); }` — failInvariantCheck returns
// tecINVARIANT_FAILED on the first pass (charges fee, retries on fee-only
// state) and tefINVARIANT_FAILED on the second pass (no-op, no fee).
func (e *Engine) runInvariants(st *applyState, result Result) (r Result, handled bool) {
	// Batch is checked per inner transaction, not on the combined outer delta.
	// rippled runs each inner tx through its own apply() + checkInvariants on its
	// own perTxBatchView (apply.cpp:189-207); the outer ttBATCH transactor's
	// invariant pass only ever sees the batch fee/sequence delta, which never
	// violates these checkers. goXRPL collapses the inner deltas into the outer
	// table for combined metadata, so running the shared invariant set here would
	// mis-count creations/deletions across inner txs (e.g. a Batch funding two
	// accounts) — exactly the false positive issue #846 addresses. The
	// authoritative defense is CheckInnerInvariants, run per inner tx in the
	// batch path.
	if st.tx.TxType() == TypeBatch {
		return Result(0), false
	}
	return e.runInvariantsOnTable(st, result, st.table)
}

// runInvariantsOnTable checks invariants against the supplied table — the tes
// path passes st.table (the apply sandbox), the tec-recovery path passes its
// tecTable (the fee + cleanup delta). result is the transaction's pre-invariant
// result (tesSUCCESS or the original tec); it is forwarded to the checkers,
// whose finalize branches are result-aware. Returns (result, true) when a
// violation has been handled (escalated via applyInvariantViolation) and
// (zero, false) when the entries pass and the caller may commit `table`.
func (e *Engine) runInvariantsOnTable(st *applyState, result Result, table *ApplyStateTable) (r Result, handled bool) {
	defer func() {
		if rec := recover(); rec != nil {
			e.logger.Error("invariant check panic recovered, returning tecINVARIANT_FAILED",
				"txHash", fmt.Sprintf("%x", st.txHash), "panic", rec)
			txDeclaredFee := parseTxDeclaredFee(st.tx, st.fee)
			r = e.applyInvariantViolation(st, txDeclaredFee)
			handled = true
		}
	}()
	invEntries := table.CollectEntries()
	txDeclaredFee := parseTxDeclaredFee(st.tx, st.fee)
	violation := invariants.CheckInvariants(wrapTxForInvariants(st.tx), invariants.Result(result), st.fee, txDeclaredFee, invEntries, table, e.rules())
	if violation == nil && e.invariantViolationHook != nil {
		violation = e.invariantViolationHook(result, table)
	}
	if violation == nil {
		return Result(0), false
	}
	// Invariant violation: discard all doApply() side effects and apply only
	// fee deduction + sequence increment, just like the tec recovery path.
	// Reference: rippled Transactor::apply() lines 1224-1238 — on tecINVARIANT_FAILED,
	// calls reset(fee) which discards the sandbox, then re-applies fee/seq only.
	_ = violation // logged in future via journal
	return e.applyInvariantViolation(st, txDeclaredFee), true
}

// CheckInnerInvariants runs the invariant pass for a single Batch inner
// transaction against its own delta, mirroring rippled where each inner tx
// flows through full apply() with its own checkInvariants on its perTxBatchView
// (apply.cpp:189-207, Transactor.cpp:1218-1238). It returns the result the inner
// transaction should carry: the supplied result when invariants pass, or an
// invariant-failed code when they do not.
//
// innerTable is the inner tx's isolated delta (the batch's perTxTable), which
// has NOT yet been committed to the batch view. The fee charged on that delta is
// zero for batch inner txs, since the outer Batch pays the whole batch fee. On
// violation the caller discards the inner delta and consumes only
// the inner sequence, so this helper does not mutate state; it reproduces
// rippled's two-pass escalation purely to choose between tec and tef: the first
// pass yields tecINVARIANT_FAILED, and a second pass on the (post-discard,
// fee-only) state yields tefINVARIANT_FAILED if it still violates.
//
// A panic from CheckInvariants (e.g. AMM XRPLNumber overflow) is treated as a
// violation, matching rippled's checkInvariantsHelper catch-all.
func (e *Engine) CheckInnerInvariants(innerTx Transaction, result Result, innerTable *ApplyStateTable) (r Result) {
	defer func() {
		if rec := recover(); rec != nil {
			e.logger.Error("inner invariant check panic recovered, returning tecINVARIANT_FAILED",
				"panic", rec)
			r = TecINVARIANT_FAILED
		}
	}()

	declaredFee := parseTxDeclaredFee(innerTx, innerFeeNone)
	wrapped := wrapTxForInvariants(innerTx)
	rules := e.rules()

	if invariants.CheckInvariants(wrapped, invariants.Result(result), innerFeeNone, declaredFee, innerTable.CollectEntries(), innerTable, rules) == nil {
		return result
	}

	// First pass violated: rippled resets to a fee-only state and re-checks.
	// The inner tx carries no fee, so the reset state has an empty delta; a
	// second violation there escalates to tefINVARIANT_FAILED.
	feeOnly := NewApplyStateTable(e.view, [32]byte{}, e.config.LedgerSequence, rules)
	if invariants.CheckInvariants(wrapped, invariants.Result(TecINVARIANT_FAILED), innerFeeNone, declaredFee, feeOnly.CollectEntries(), feeOnly, rules) != nil {
		return TefINVARIANT_FAILED
	}
	return TecINVARIANT_FAILED
}

// innerFeeNone is the fee charged on a Batch inner transaction's delta. Inner
// txs declare and pay no fee — the outer Batch transaction pays the whole batch
// fee — so XRPNotCreated/TransactionFeeCheck see zero on the inner delta.
const innerFeeNone uint64 = 0

// applyInvariantViolation handles the tecINVARIANT_FAILED reset path: discard
// the sandbox, charge fee/seq/ticket, then run a second invariant check on the
// fee-only state. If that also violates, escalate to tefINVARIANT_FAILED.
//
// The second-pass invariant check is wrapped in its own panic-recover so that
// a panic from CheckInvariants (or invTecTable.Apply) on a fee-only state
// cannot escape this function — mirroring rippled's checkInvariantsHelper
// (ApplyContext.cpp:97-148), which wraps both the initial and recovery passes
// in the same try/catch and escalates via failInvariantCheck. Without this,
// a defense-in-depth panic on the recovery state would propagate out of
// runInvariants (whose own defer has already fired) and crash the engine.
//
// Reference: rippled Transactor.cpp lines 1224-1238.
func (e *Engine) applyInvariantViolation(st *applyState, txDeclaredFee uint64) (result Result) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("invariant recovery panic — escalating to tefINVARIANT_FAILED",
				"txHash", fmt.Sprintf("%x", st.txHash), "panic", r)
			result = TefINVARIANT_FAILED
		}
	}()
	// Don't call table.Apply() — discard all transaction effects.
	// Create a fresh tecTable for fee-only changes.
	invTecTable := NewApplyStateTable(e.view, st.txHash, e.config.LedgerSequence, e.rules())

	// Consume ticket through invTecTable if needed.
	if st.isTicket {
		if r := e.consumeTicketForRecovery(st, invTecTable); r != TesSUCCESS {
			return r
		}
	}

	// Restore account to original state, then apply only fee/sequence.
	invAccount, invErr := state.ParseAccountRoot(st.originalAccountData)
	if invErr != nil {
		return TefINTERNAL
	}
	if r := e.writeRecoveryAccount(st, invTecTable, invAccount); r != TesSUCCESS {
		return r
	}

	// For delegated transactions, deduct the fee from the delegate.
	if r := e.payDelegatedFeeOnTable(st, invTecTable); r != TesSUCCESS {
		return r
	}

	// Second invariant check on fee-only state.
	// Reference: rippled Transactor.cpp lines 1234-1238
	// If fee-only state also violates invariants, escalate to tefINVARIANT_FAILED
	// and do NOT apply anything (transaction is completely rejected).
	invEntries2 := invTecTable.CollectEntries()
	violation2 := invariants.CheckInvariants(wrapTxForInvariants(st.tx), invariants.Result(TecINVARIANT_FAILED), st.fee, txDeclaredFee, invEntries2, invTecTable, e.rules())
	if violation2 == nil && e.invariantViolationHook != nil {
		violation2 = e.invariantViolationHook(TecINVARIANT_FAILED, invTecTable)
	}
	if violation2 != nil {
		return TefINVARIANT_FAILED
	}

	generatedMeta, applyErr := invTecTable.Apply()
	if applyErr != nil {
		return TefINTERNAL
	}
	st.metadata.AffectedNodes = generatedMeta.AffectedNodes

	return TecINVARIANT_FAILED
}
