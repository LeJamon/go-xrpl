package tx

import (
	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx/invariants"
	"github.com/LeJamon/goXRPLd/keylet"
)

// doApply applies the transaction to the ledger
// For tec results, only fee/sequence changes are applied; transaction effects are discarded.
// Reference: rippled Transactor.cpp - tec results claim fee but don't apply effects
func (e *Engine) doApply(tx Transaction, metadata *Metadata, txHash [32]byte) Result {
	// Store txHash for use by apply functions
	e.currentTxHash = txHash

	// Deduct fee from sender first (this always happens for applied transactions)
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

	// Deduct fee and handle sequence/ticket
	// Reference: rippled Transactor::payFee + consumeSeqProxy in Transactor.cpp
	isDelegated := common.Delegate != ""
	isTicket := common.TicketSequence != nil

	if isDelegated {
		// Delegated transactions: fee is charged to the delegate account, not the source.
		// The source account's balance is NOT reduced by the fee.
		// Reference: rippled Transactor::payFee() lines 327-337
	} else {
		// Normal transactions: fee is charged to the source account.
		account.Balance -= fee
	}

	if !isTicket && common.Sequence != nil {
		account.Sequence = *common.Sequence + 1
	}

	// Update PreviousTxnID and PreviousTxnLgrSeq (thread the account)
	account.PreviousTxnID = txHash
	account.PreviousTxnLgrSeq = e.config.LedgerSequence

	// Update AccountTxnID if the account has tracking enabled (field is present/non-zero).
	// Reference: rippled Transactor::apply() line 568-569:
	//   if (sle->isFieldPresent(sfAccountTxnID))
	//       sle->setFieldH256(sfAccountTxnID, ctx_.tx.getTransactionID());
	{
		var zeroHash [32]byte
		if account.AccountTxnID != zeroHash {
			account.AccountTxnID = txHash
		}
	}

	// Create ApplyStateTable for transaction-specific changes
	table := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

	// Write the fee-deducted, sequence-incremented account to the table BEFORE Apply().
	// This matches rippled's Transactor::apply() which modifies the account SLE
	// (fee deduction, sequence increment) before calling doApply().
	// Without this, reads during Apply() would see the pre-fee balance.
	{
		preApplyData, preApplyErr := state.SerializeAccountRoot(account)
		if preApplyErr != nil {
			return TefINTERNAL
		}
		if err := table.Update(accountKey, preApplyData); err != nil {
			return TefINTERNAL
		}
	}

	// For delegated transactions, deduct the fee from the delegate's account.
	// Reference: rippled Transactor::payFee() lines 327-337
	if isDelegated {
		delegateID, _ := state.DecodeAccountID(common.Delegate)
		delegateAccountKey := keylet.Account(delegateID)
		delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
		if delegateReadErr != nil || delegateAccountData == nil {
			return TefINTERNAL
		}
		delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
		if delegateParseErr != nil {
			return TefINTERNAL
		}
		delegateAccount.Balance -= fee
		delegateAccount.PreviousTxnID = txHash
		delegateAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
		delegateData, delegateSerErr := state.SerializeAccountRoot(delegateAccount)
		if delegateSerErr != nil {
			return TefINTERNAL
		}
		if err := table.Update(delegateAccountKey, delegateData); err != nil {
			return TefINTERNAL
		}
	}

	// Type-specific application - all operations go through the table
	var result Result

	// Determine if the transaction was signed with the master key.
	// Reference: rippled SetAccount.cpp sigWithMaster — compares
	// calcAccountID(SigningPubKey) against the account ID.
	// When signature verification is skipped (test mode), assume master key.
	sigWithMaster := e.config.SkipSignatureVerification
	if common.SigningPubKey != "" {
		signerAddr, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(common.SigningPubKey)
		if addrErr == nil {
			sigWithMaster = signerAddr == common.Account
		}
	}

	// All transaction types implement Appliable
	ctx := &ApplyContext{
		View:             table,
		Account:          account,
		AccountID:        accountID,
		Config:           e.config,
		TxHash:           txHash,
		Metadata:         metadata,
		Engine:           e,
		SignedWithMaster: sigWithMaster,
		Log:              e.logger,
	}

	// Consume ticket BEFORE Apply, matching rippled's Transactor::apply()
	// which calls consumeSeqProxy() before doApply(). This ensures that when
	// doApply() iterates the owner directory (e.g., AccountDelete), the
	// consumed ticket is already gone.
	if isTicket {
		ticketKey := keylet.Ticket(accountID, *common.TicketSequence)
		ownerDirKey := keylet.OwnerDir(accountID)
		var ticketOwnerNode uint64
		if ticketData, ticketErr := table.Read(ticketKey); ticketErr == nil && ticketData != nil {
			ticketOwnerNode = state.GetOwnerNode(ticketData)
		}
		state.DirRemove(table, ownerDirKey, ticketOwnerNode, ticketKey.Key, true)
		if err := table.Erase(ticketKey); err != nil {
			return TefINTERNAL
		}
		if account.OwnerCount > 0 {
			account.OwnerCount--
		}
		if account.TicketCount > 0 {
			account.TicketCount--
		}
		preApplyData2, preApplyErr2 := state.SerializeAccountRoot(account)
		if preApplyErr2 != nil {
			return TefINTERNAL
		}
		if err := table.Update(accountKey, preApplyData2); err != nil {
			return TefINTERNAL
		}
	}

	// Set NumberSwitchover based on fixUniversalNumber amendment.
	// When enabled, IOUAmount arithmetic uses Guard-based precision (XRPLNumber).
	// Reference: rippled's setSTNumberSwitchover() in IOUAmount.cpp
	state.SetNumberSwitchover(ctx.Rules().Enabled(amendment.FeatureFixUniversalNumber))

	if appliable, ok := tx.(Appliable); ok {
		result = appliable.Apply(ctx)
	} else {
		result = TesSUCCESS
	}

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

	// Ticket was already consumed before Apply (see below). No post-Apply
	// ticket consumption needed for success results.

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
		// For tecOVERSIZE and tecKILLED: collect deleted offers from the table
		// BEFORE discarding, so we can re-remove them from the clean view.
		// Reference: rippled Transactor.cpp lines 1121-1201:
		//   ctx_.visit() collects deleted offer keys, then reset(), then removeUnfundedOffers()
		var removedOfferKeys [][32]byte
		if result == TecOVERSIZE || result == TecKILLED {
			const unfundedOfferRemoveLimit = 1000
			for key, entry := range table.GetItems() {
				if entry.Action == ActionErase {
					entryType := getLedgerEntryType(entry.Original)
					if entryType == "" && entry.Current != nil {
						entryType = getLedgerEntryType(entry.Current)
					}
					if entryType == "Offer" {
						removedOfferKeys = append(removedOfferKeys, key)
						if len(removedOfferKeys) >= unfundedOfferRemoveLimit {
							break
						}
					}
				}
			}
		}

		// Collect deleted trust line keys for tecINCOMPLETE (AMMDelete) re-deletion.
		// Reference: rippled Transactor.cpp lines 1139, 1171-1176, 1207-1209:
		//   ctx_.visit() collects deleted RippleState keys, then reset(), then removeDeletedTrustLines()
		var removedTrustLineKeys [][32]byte
		if result == TecINCOMPLETE {
			const maxDeletableAMMTrustLines = 512
			for key, entry := range table.GetItems() {
				if entry.Action == ActionErase {
					entryType := getLedgerEntryType(entry.Original)
					if entryType == "" && entry.Current != nil {
						entryType = getLedgerEntryType(entry.Current)
					}
					if entryType == "RippleState" {
						removedTrustLineKeys = append(removedTrustLineKeys, key)
						if len(removedTrustLineKeys) >= maxDeletableAMMTrustLines {
							break
						}
					}
				}
			}
		}

		// Collect expired NFTokenOffer keys for tecEXPIRED re-deletion.
		// Reference: rippled Transactor.cpp lines 1140, 1178-1180, 1203-1205
		var expiredNFTokenOfferKeys [][32]byte
		if result == TecEXPIRED {
			const expiredOfferRemoveLimit = 256
			for key, entry := range table.GetItems() {
				if entry.Action == ActionErase {
					entryType := getLedgerEntryType(entry.Original)
					if entryType == "" && entry.Current != nil {
						entryType = getLedgerEntryType(entry.Current)
					}
					if entryType == "NFTokenOffer" {
						expiredNFTokenOfferKeys = append(expiredNFTokenOfferKeys, key)
						if len(expiredNFTokenOfferKeys) >= expiredOfferRemoveLimit {
							break
						}
					}
				}
			}
		}

		// Discard the transaction table — all doApply() side effects are lost.
		// Reference: rippled Transactor.cpp — reset() discards the sandbox.
		// (We simply don't call table.Apply(), which effectively discards it.)

		// Create a fresh ApplyStateTable to track tec-specific changes
		// (fee, sequence, ticket consumption) for proper metadata generation.
		tecTable := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

		// Consume ticket through tecTable for proper metadata (DeletedNode + directory changes)
		// Reference: rippled Transactor.cpp — tec still consumes the ticket.
		if isTicket {
			ticketKey := keylet.Ticket(accountID, *common.TicketSequence)
			ownerDirKey := keylet.OwnerDir(accountID)
			// Read ticket SLE to get OwnerNode (directory page) for proper removal.
			var ticketOwnerNode uint64
			if ticketData, ticketErr := tecTable.Read(ticketKey); ticketErr == nil && ticketData != nil {
				ticketOwnerNode = state.GetOwnerNode(ticketData)
			}
			state.DirRemove(tecTable, ownerDirKey, ticketOwnerNode, ticketKey.Key, true)
			if err := tecTable.Erase(ticketKey); err != nil {
				return TefINTERNAL
			}
		}
		// tecINCOMPLETE (AMMDelete): re-delete trust lines that were found during processing.
		// These trust lines were deleted in the (now discarded) sandbox.
		// Reference: rippled Transactor.cpp lines 1207-1209: removeDeletedTrustLines()
		//   which calls deleteAMMTrustLine() for each collected trust line key.
		if len(removedTrustLineKeys) > 0 {
			for _, lineKey := range removedTrustLineKeys {
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

		// Restore account to original state, then apply only fee/sequence.
		// This discards any changes the transaction made to OwnerCount,
		// MintedNFTokens, BurnedNFTokens, etc.
		// Reference: rippled Transactor.cpp — restores original SLE on tec.
		account, err = state.ParseAccountRoot(originalAccountData)
		if err != nil {
			return TefINTERNAL
		}
		// For delegated transactions, fee is charged to the delegate, not the source.
		// Reference: rippled Transactor.cpp reset() lines 1011-1013, 1036
		if !isDelegated {
			account.Balance -= fee
		}
		if !isTicket && common.Sequence != nil {
			account.Sequence = *common.Sequence + 1
		}
		// Apply ticket consumption OwnerCount and TicketCount decreases.
		if isTicket && account.OwnerCount > 0 {
			account.OwnerCount--
		}
		if isTicket && account.TicketCount > 0 {
			account.TicketCount--
		}
		// Apply PreviousTxnID/PreviousTxnLgrSeq threading
		account.PreviousTxnID = txHash
		account.PreviousTxnLgrSeq = e.config.LedgerSequence

		// Update AccountTxnID if the account has tracking enabled (field is present/non-zero).
		// On the success path, apply() sets this before doApply(). On the tec path,
		// reset() discards all changes then re-applies fee/sequence. The AccountTxnID
		// must also be updated here so the account tracks the last-applied transaction
		// even when the result is a tec code.
		// Reference: rippled Transactor::apply() lines 568-569.
		{
			var zeroHash [32]byte
			if account.AccountTxnID != zeroHash {
				account.AccountTxnID = txHash
			}
		}

		updatedData, err := state.SerializeAccountRoot(account)
		if err != nil {
			return TefINTERNAL
		}

		// Update account through tecTable for proper metadata diff generation
		if err := tecTable.Update(accountKey, updatedData); err != nil {
			return TefINTERNAL
		}

		// For delegated transactions, deduct the fee from the delegate's account on tec.
		// Reference: rippled Transactor.cpp reset() lines 1011-1013, 1036
		if isDelegated {
			delegateID, _ := state.DecodeAccountID(common.Delegate)
			delegateAccountKey := keylet.Account(delegateID)
			delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
			if delegateReadErr != nil || delegateAccountData == nil {
				return TefINTERNAL
			}
			delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
			if delegateParseErr != nil {
				return TefINTERNAL
			}
			delegateAccount.Balance -= fee
			delegateAccount.PreviousTxnID = txHash
			delegateAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
			delegateData, delegateSerErr := state.SerializeAccountRoot(delegateAccount)
			if delegateSerErr != nil {
				return TefINTERNAL
			}
			if err := tecTable.Update(delegateAccountKey, delegateData); err != nil {
				return TefINTERNAL
			}
		}

		// tecOVERSIZE/tecKILLED: re-delete offers that were found during processing.
		// These offers were deleted in the (now discarded) sandbox.
		// Reference: rippled Transactor.cpp lines 1198-1201: removeUnfundedOffers()
		if len(removedOfferKeys) > 0 {
			for _, offerKey := range removedOfferKeys {
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

		// tecEXPIRED: re-delete expired NFTokenOffers and credentials.
		// Reference: rippled Transactor.cpp lines 1203-1205: removeExpiredNFTokenOffers()
		if result == TecEXPIRED {
			// Re-delete NFTokenOffers through tecTable
			for _, offerKey := range expiredNFTokenOfferKeys {
				offerKL := keylet.Keylet{Key: offerKey}
				deleteNFTokenOfferOnView(tecTable, offerKL, txHash, e.config.LedgerSequence)
			}

			// Credential deletion via TecApplier
			if tecApplier, ok := tx.(TecApplier); ok {
				tecCtx := &ApplyContext{
					View:      tecTable,
					Account:   account,
					AccountID: accountID,
					Config:    e.config,
					TxHash:    txHash,
					Metadata:  metadata,
					Engine:    e,
					Log:       e.logger,
				}
				tecApplier.ApplyOnTec(tecCtx)
			}
		}

		// Apply all tracked changes and generate proper metadata
		generatedMeta, applyErr := tecTable.Apply()
		if applyErr != nil {
			return TefINTERNAL
		}
		metadata.AffectedNodes = generatedMeta.AffectedNodes

		return result
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
	{
		invEntries := table.CollectEntries()
		txDeclaredFee := parseTxDeclaredFee(tx, fee)
		if violation := invariants.CheckInvariants(wrapTxForInvariants(tx), invariants.Result(result), fee, txDeclaredFee, invEntries, table, e.rules()); violation != nil {
			// Invariant violation: discard all doApply() side effects and apply only
			// fee deduction + sequence increment, just like the tec recovery path.
			// Reference: rippled Transactor::apply() lines 1224-1238 — on tecINVARIANT_FAILED,
			// calls reset(fee) which discards the sandbox, then re-applies fee/seq only.
			_ = violation // logged in future via journal

			// Don't call table.Apply() — discard all transaction effects.
			// Create a fresh tecTable for fee-only changes.
			invTecTable := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

			// Consume ticket through invTecTable if needed.
			if isTicket {
				ticketKey := keylet.Ticket(accountID, *common.TicketSequence)
				ownerDirKey := keylet.OwnerDir(accountID)
				var ticketOwnerNode uint64
				if ticketData, ticketErr := invTecTable.Read(ticketKey); ticketErr == nil && ticketData != nil {
					ticketOwnerNode = state.GetOwnerNode(ticketData)
				}
				state.DirRemove(invTecTable, ownerDirKey, ticketOwnerNode, ticketKey.Key, true)
				if err := invTecTable.Erase(ticketKey); err != nil {
					return TefINTERNAL
				}
			}

			// Restore account to original state, then apply only fee/sequence.
			invAccount, invErr := state.ParseAccountRoot(originalAccountData)
			if invErr != nil {
				return TefINTERNAL
			}
			if !isDelegated {
				invAccount.Balance -= fee
			}
			if !isTicket && common.Sequence != nil {
				invAccount.Sequence = *common.Sequence + 1
			}
			if isTicket && invAccount.OwnerCount > 0 {
				invAccount.OwnerCount--
			}
			if isTicket && invAccount.TicketCount > 0 {
				invAccount.TicketCount--
			}
			invAccount.PreviousTxnID = txHash
			invAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
			{
				var zeroHash [32]byte
				if invAccount.AccountTxnID != zeroHash {
					invAccount.AccountTxnID = txHash
				}
			}

			invUpdatedData, invSerErr := state.SerializeAccountRoot(invAccount)
			if invSerErr != nil {
				return TefINTERNAL
			}
			if err := invTecTable.Update(accountKey, invUpdatedData); err != nil {
				return TefINTERNAL
			}

			// For delegated transactions, deduct the fee from the delegate.
			if isDelegated {
				delegateID, _ := state.DecodeAccountID(common.Delegate)
				delegateAccountKey := keylet.Account(delegateID)
				delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
				if delegateReadErr != nil || delegateAccountData == nil {
					return TefINTERNAL
				}
				delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
				if delegateParseErr != nil {
					return TefINTERNAL
				}
				delegateAccount.Balance -= fee
				delegateAccount.PreviousTxnID = txHash
				delegateAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
				delegateData, delegateSerErr := state.SerializeAccountRoot(delegateAccount)
				if delegateSerErr != nil {
					return TefINTERNAL
				}
				if err := invTecTable.Update(delegateAccountKey, delegateData); err != nil {
					return TefINTERNAL
				}
			}

			// Second invariant check on fee-only state.
			// Reference: rippled Transactor.cpp lines 1234-1238
			// If fee-only state also violates invariants, escalate to tefINVARIANT_FAILED
			// and do NOT apply anything (transaction is completely rejected).
			{
				invEntries2 := invTecTable.CollectEntries()
				if violation2 := invariants.CheckInvariants(wrapTxForInvariants(tx), invariants.Result(TecINVARIANT_FAILED), fee, txDeclaredFee, invEntries2, invTecTable, e.rules()); violation2 != nil {
					return TefINVARIANT_FAILED
				}
			}

			generatedMeta, applyErr := invTecTable.Apply()
			if applyErr != nil {
				return TefINTERNAL
			}
			metadata.AffectedNodes = generatedMeta.AffectedNodes

			return TecINVARIANT_FAILED
		}
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
