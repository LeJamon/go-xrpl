package tx

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// preclaim validates the transaction against the current ledger state.
// Mirrors rippled's Transactor::operator()() pre-application pipeline:
//   checkSeqProxy → checkPriorTxAndLastLedger → checkFee → checkPermission →
//   checkSign (+ checkBatchSign) → tx-type preclaim.
func (e *Engine) preclaim(tx Transaction, txHash [32]byte) Result {
	common := tx.GetCommon()

	// Resolve and parse the source account; this is shared by all subsequent steps.
	accountID, account, result := e.preclaimLoadAccount(common)
	if result != TesSUCCESS {
		return result
	}

	if result := e.checkSeqProxy(common, accountID, account); result != TesSUCCESS {
		return result
	}
	if result := e.checkPriorTxAndLastLedger(common, account, txHash); result != TesSUCCESS {
		return result
	}
	if result := e.checkFee(tx, common, account); result != TesSUCCESS {
		return result
	}
	if result := e.checkPermission(tx, common, accountID); result != TesSUCCESS {
		return result
	}
	if result := e.checkSign(tx, common); result != TesSUCCESS {
		return result
	}

	// Step 6: checkBatchSign — batch signer authorization
	// Reference: rippled Batch::checkSign -> Transactor::checkBatchSign
	// This checks that each BatchSigner is authorized to act as their account.
	// This runs even when SkipSignatureVerification is true because it checks
	// authorization (account existence, master key, regular key), not crypto.
	if bsp, ok := tx.(BatchSignerProvider); ok {
		if result := e.checkBatchSign(bsp.GetBatchSigners()); result != TesSUCCESS {
			return result
		}
	}

	// Step 7: Transaction-specific preclaim checks.
	// These run after all common preclaim checks and are subject to the
	// TapRETRY gate in Apply(). tec results from preclaim are NOT applied
	// when TapRETRY is set (likelyToClaimFee = false), matching rippled's
	// PreclaimResult semantics.
	// Reference: rippled applySteps.h — invoke_preclaim dispatches to
	// the transaction type's static preclaim() method.
	if preclaimer, ok := tx.(Preclaimer); ok {
		if result := preclaimer.Preclaim(e.config); result != TesSUCCESS {
			return result
		}
	}

	return TesSUCCESS
}

// preclaimLoadAccount decodes the source account and reads + parses its SLE.
// Returns the decoded accountID, the parsed AccountRoot, and a TER result.
func (e *Engine) preclaimLoadAccount(common *Common) ([20]byte, *state.AccountRoot, Result) {
	accountID, err := state.DecodeAccountID(common.Account)
	if err != nil {
		return [20]byte{}, nil, TemBAD_SRC_ACCOUNT
	}

	accountKey := keylet.Account(accountID)
	exists, err := e.view.Exists(accountKey)
	if err != nil {
		return accountID, nil, TefINTERNAL
	}
	if !exists {
		return accountID, nil, TerNO_ACCOUNT
	}

	accountData, err := e.view.Read(accountKey)
	if err != nil {
		return accountID, nil, TefINTERNAL
	}

	account, err := state.ParseAccountRoot(accountData)
	if err != nil {
		return accountID, nil, TefINTERNAL
	}
	return accountID, account, TesSUCCESS
}

// checkSeqProxy validates Sequence/TicketSequence against the account state.
// Reference: rippled Transactor::checkSeqProxy in Transactor.cpp.
func (e *Engine) checkSeqProxy(common *Common, accountID [20]byte, account *state.AccountRoot) Result {
	// Check for both Sequence (non-zero) and TicketSequence set → temSEQ_AND_TICKET
	// Reference: rippled Transactor::checkSeqProxy in Transactor.cpp line 375
	if common.Sequence != nil && *common.Sequence != 0 && common.TicketSequence != nil {
		if e.rules().Enabled(amendment.FeatureTicketBatch) {
			return TemSEQ_AND_TICKET
		}
	}

	// Check sequence number or ticket
	if common.TicketSequence != nil {
		// Ticket-based transaction: validate the ticket exists
		if *common.TicketSequence >= account.Sequence {
			// Ticket hasn't been created yet
			return TerPRE_TICKET
		}
		ticketKey := keylet.Ticket(accountID, *common.TicketSequence)
		ticketExists, ticketErr := e.view.Exists(ticketKey)
		if ticketErr != nil || !ticketExists {
			return TefNO_TICKET
		}
	} else if common.Sequence != nil {
		if *common.Sequence < account.Sequence {
			return TefPAST_SEQ
		}
		if *common.Sequence > account.Sequence {
			return TerPRE_SEQ
		}
	}
	return TesSUCCESS
}

// checkPriorTxAndLastLedger validates AccountTxnID, LastLedgerSequence, and
// dedupes by transaction hash.
// Reference: rippled Transactor::checkPriorTxAndLastLedger in Transactor.cpp.
func (e *Engine) checkPriorTxAndLastLedger(common *Common, account *state.AccountRoot, txHash [32]byte) Result {
	// AccountTxnID check — if the transaction specifies an AccountTxnID, it must match
	// the account's stored AccountTxnID (the hash of the last tx this account submitted).
	if common.AccountTxnID != "" {
		txAccountTxnID, decErr := hex.DecodeString(common.AccountTxnID)
		if decErr != nil || len(txAccountTxnID) != 32 {
			return TefWRONG_PRIOR
		}
		var txPrior [32]byte
		copy(txPrior[:], txAccountTxnID)
		if txPrior != account.AccountTxnID {
			return TefWRONG_PRIOR
		}
	}

	// LastLedgerSequence check
	if common.LastLedgerSequence != nil {
		if e.config.LedgerSequence > *common.LastLedgerSequence {
			return TefMAX_LEDGER
		}
	}

	// Duplicate transaction detection — if this transaction hash already exists in the
	// view (already applied to this ledger), return tefALREADY.
	// Reference: rippled Transactor::checkPriorTxAndLastLedger — ctx.view.txExists()
	if e.view.TxExists(txHash) {
		return TefALREADY
	}
	return TesSUCCESS
}

// checkFee enforces fee adequacy and that the fee payer (delegate or source)
// can afford the fee. Reference: rippled Transactor::checkFee in Transactor.cpp.
func (e *Engine) checkFee(tx Transaction, common *Common, account *state.AccountRoot) Result {
	// When a delegate is present, the fee is checked against the delegate's balance.
	fee := e.calculateFee(tx)
	baseFeeForTx := e.preclaimBaseFee(tx, common, account)

	// Fee adequacy check: only when the ledger is open.
	// Reference: rippled Transactor::checkFee lines 277-290:
	//   "Only check fee is sufficient when the ledger is open."
	//   When the view is NOT open, fee=0 is accepted (line 292-293).
	if e.config.OpenLedger {
		if fee < baseFeeForTx {
			return TelINSUF_FEE_P
		}
	}

	// When fee is zero, skip batch fee check and balance checks.
	// Reference: rippled Transactor::checkFee line 292-293:
	//   if (feePaid == beast::zero) return tesSUCCESS;
	if fee == 0 {
		return TesSUCCESS
	}

	if feeCalc, ok := tx.(BatchFeeCalculator); ok {
		batchMinFee := feeCalc.CalculateMinimumFee(e.config.BaseFee)
		if fee < batchMinFee {
			return TelINSUF_FEE_P
		}
	}

	// Determine who pays the fee: delegate (if present) or the source account.
	// Reference: rippled Transactor::checkFee lines 295-297:
	//   auto const id = ctx.tx.isFieldPresent(sfDelegate)
	//       ? ctx.tx.getAccountID(sfDelegate)
	//       : ctx.tx.getAccountID(sfAccount);
	feePayerBalance, balResult := e.feePayerBalance(common, account)
	if balResult != TesSUCCESS {
		return balResult
	}
	if feePayerBalance < fee {
		return TerINSUF_FEE_B
	}
	return TesSUCCESS
}

// preclaimBaseFee computes the minimum base fee for this transaction type,
// applying multi-sign multipliers, custom calculators, and the SetRegularKey
// free-password-change special case.
// Reference: rippled applySteps.cpp calculateBaseFee() + SetRegularKey.cpp.
func (e *Engine) preclaimBaseFee(tx Transaction, common *Common, account *state.AccountRoot) uint64 {
	var baseFeeForTx uint64
	if feeCalc, ok := tx.(CustomBaseFeeCalculator); ok {
		baseFeeForTx = feeCalc.CalculateBaseFee(e.view, e.config)
	} else {
		baseFeeForTx = e.config.BaseFee
		if IsMultiSigned(tx) {
			baseFeeForTx = CalculateMultiSigFee(e.config.BaseFee, len(common.Signers))
		}
	}
	// SetRegularKey special case: free password change when lsfPasswordSpent not set.
	// Reference: rippled SetRegularKey.cpp calculateBaseFee
	if tx.TxType() == TypeRegularKeySet {
		signedWithMaster := false
		if spk := common.SigningPubKey; spk != "" {
			sigAddr, sigErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(spk)
			if sigErr == nil && sigAddr == common.Account {
				signedWithMaster = true
			}
		} else if e.config.SkipSignatureVerification && !IsMultiSigned(tx) {
			signedWithMaster = true
		}
		if signedWithMaster && account.Flags&state.LsfPasswordSpent == 0 {
			baseFeeForTx = 0
		}
	}
	return baseFeeForTx
}

// feePayerBalance returns the balance of the account that will be charged the fee
// (delegate when sfDelegate is present, otherwise the source account).
func (e *Engine) feePayerBalance(common *Common, account *state.AccountRoot) (uint64, Result) {
	if common.Delegate == "" {
		return account.Balance, TesSUCCESS
	}
	delegateID, delegateErr := state.DecodeAccountID(common.Delegate)
	if delegateErr != nil {
		return 0, TerNO_ACCOUNT
	}
	delegateAccountKey := keylet.Account(delegateID)
	delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
	if delegateReadErr != nil || delegateAccountData == nil {
		return 0, TerNO_ACCOUNT
	}
	delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
	if delegateParseErr != nil {
		return 0, TefINTERNAL
	}
	return delegateAccount.Balance, TesSUCCESS
}

// checkPermission validates that, when sfDelegate is set, the delegate SLE
// grants permission for this transaction type.
// Reference: rippled Transactor::checkPermission in Transactor.cpp lines 213-227
// and DelegateUtils.cpp checkTxPermission().
func (e *Engine) checkPermission(tx Transaction, common *Common, accountID [20]byte) Result {
	if common.Delegate == "" {
		return TesSUCCESS
	}
	delegateID, _ := state.DecodeAccountID(common.Delegate)
	delegateKeylet := keylet.DelegateKeylet(accountID, delegateID)
	delegateData, readErr := e.view.Read(delegateKeylet)
	if readErr != nil || delegateData == nil {
		return TecNO_DELEGATE_PERMISSION
	}
	delegateEntry, parseErr := state.ParseDelegate(delegateData)
	if parseErr != nil {
		return TecNO_DELEGATE_PERMISSION
	}
	// Check if the delegate SLE grants permission for this tx type.
	// In rippled: permissionValue == tx.getTxnType() + 1
	txTypeValue := uint32(tx.TxType())
	if !delegateEntry.HasTxPermission(txTypeValue) {
		return TecNO_DELEGATE_PERMISSION
	}
	return TesSUCCESS
}

// checkSign performs signature authorization for both single-signed and
// multi-signed transactions, dispatching to checkSingleSign / checkMultiSign.
// Reference: rippled Transactor::checkSign in Transactor.cpp.
// When a delegate is present, the idAccount for signature checking is the
// delegate. Reference: rippled line 602:
//   auto const idAccount = ctx.tx[~sfDelegate].value_or(ctx.tx[sfAccount]);
func (e *Engine) checkSign(tx Transaction, common *Common) Result {
	if IsMultiSigned(tx) {
		return e.checkMultiSign(common)
	}
	if common.SigningPubKey != "" {
		return e.checkSingleSign(common)
	}
	return TesSUCCESS
}

// checkMultiSign verifies the multi-sign signers against the idAccount's
// SignerList and quorum.
// Reference: rippled Transactor::checkMultiSign in Transactor.cpp lines 743-911.
func (e *Engine) checkMultiSign(common *Common) Result {
	// Multi-signed transaction: always check signer authorization and quorum.
	// This runs regardless of SkipSignatureVerification because quorum and
	// signer authorization (master key disabled, regular key, phantom accounts)
	// are ledger-state checks, not cryptographic checks.
	idAccount := common.Account
	if common.Delegate != "" {
		idAccount = common.Delegate
	}
	idAccountID, idErr := state.DecodeAccountID(idAccount)
	if idErr != nil {
		return TefBAD_SIGNATURE
	}
	// Convert tx Signers to SignerInfo for checkBatchMultiSign
	txSigners := make([]SignerInfo, len(common.Signers))
	for i, sw := range common.Signers {
		txSigners[i] = SignerInfo{
			Account:       sw.Signer.Account,
			SigningPubKey: sw.Signer.SigningPubKey,
		}
	}
	return e.checkBatchMultiSign(idAccountID, txSigners)
}

// checkSingleSign validates a single-signed transaction's signing key against
// the idAccount's master/regular key configuration.
// Reference: rippled Transactor::checkSingleSign in Transactor.cpp lines 682-740.
func (e *Engine) checkSingleSign(common *Common) Result {
	// Single-signed transaction: check signing key authorization.
	// This runs regardless of SkipSignatureVerification because authorization
	// (master key disabled, regular key) is a ledger-state check, not a
	// cryptographic check. The actual signature verification is done in
	// Validate() and gated by SkipSignatureVerification.
	signerAddress, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(common.SigningPubKey)
	if addrErr != nil {
		return TefBAD_AUTH
	}

	// Determine the idAccount: delegate if present, else source account.
	idAccount := common.Account
	if common.Delegate != "" {
		idAccount = common.Delegate
	}

	// Read the idAccount's data for signature authorization check
	idAccountID, idErr := state.DecodeAccountID(idAccount)
	if idErr != nil {
		return TefBAD_AUTH
	}
	idAccountKey := keylet.Account(idAccountID)
	idAccountData, idReadErr := e.view.Read(idAccountKey)
	if idReadErr != nil || idAccountData == nil {
		return TerNO_ACCOUNT
	}
	idAccountRoot, idParseErr := state.ParseAccountRoot(idAccountData)
	if idParseErr != nil {
		return TefINTERNAL
	}

	isMasterDisabled := (idAccountRoot.Flags & state.LsfDisableMaster) != 0

	if e.rules().Enabled(amendment.FeatureFixMasterKeyAsRegularKey) {
		// With fixMasterKeyAsRegularKey: check regular key first, then master.
		// This allows the master key to serve as a regular key even when
		// master signing is disabled (e.g., regkey(alice, alice) + disable master).
		// Reference: rippled Transactor::checkSingleSign lines 691-713
		if signerAddress == idAccountRoot.RegularKey {
			// Signed with regular key — allowed
			return TesSUCCESS
		}
		if !isMasterDisabled && signerAddress == idAccount {
			// Signed with enabled master key — allowed
			return TesSUCCESS
		}
		if isMasterDisabled && signerAddress == idAccount {
			// Signed with disabled master key
			return TefMASTER_DISABLED
		}
		// Signed with an unauthorized key
		return TefBAD_AUTH
	}

	// Without fixMasterKeyAsRegularKey: check master key first.
	// If signer == account, it's a master key sign attempt.
	// The regular key is only checked if signer != account.
	// Reference: rippled Transactor::checkSingleSign lines 715-737
	if signerAddress == idAccount {
		// Signing with the master key. Continue if it is not disabled.
		if isMasterDisabled {
			return TefMASTER_DISABLED
		}
		return TesSUCCESS
	}
	if signerAddress == idAccountRoot.RegularKey {
		// Signing with the regular key. Continue.
		return TesSUCCESS
	}
	if idAccountRoot.RegularKey != "" {
		// Signing key does not match master or regular key.
		return TefBAD_AUTH
	}
	// No regular key on account and signing key does not match master key.
	return TefBAD_AUTH_MASTER
}

// checkBatchSign verifies that each batch signer is authorized to sign for their account.
// For single-sign signers (SigningPubKey non-empty): derives account from pubkey, checks authorization.
// For multi-sign signers (SigningPubKey empty): checks signer list exists and quorum is met.
// Reference: rippled Transactor::checkBatchSign in Transactor.cpp lines 635-679
func (e *Engine) checkBatchSign(signers []BatchSignerInfo) Result {
	for _, signer := range signers {
		signerAccountID, err := state.DecodeAccountID(signer.Account)
		if err != nil {
			return TefBAD_AUTH
		}

		if signer.SigningPubKey == "" {
			// Multi-sign batch signer: check nested Signers against the account's SignerList.
			// Reference: rippled checkBatchSign -> checkMultiSign
			if result := e.checkBatchMultiSign(signerAccountID, signer.Signers); result != TesSUCCESS {
				return result
			}
			continue
		}

		// Single-sign batch signer: derive account from public key
		signerAddress, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(signer.SigningPubKey)
		if addrErr != nil {
			return TefBAD_AUTH
		}

		signerAccountKey := keylet.Account(signerAccountID)
		signerAccountData, readErr := e.view.Read(signerAccountKey)

		if readErr != nil || signerAccountData == nil {
			// Account doesn't exist: only allowed if the signer pubkey derives to this account
			// (phantom account pattern — the signer IS the account)
			if signerAddress != signer.Account {
				return TefBAD_AUTH
			}
			// Phantom account — allowed
			continue
		}

		signerAccountRoot, parseErr := state.ParseAccountRoot(signerAccountData)
		if parseErr != nil {
			return TefINTERNAL
		}

		// Check authorization: master key, regular key, or disabled master
		// Reference: rippled Transactor::checkSingleSign
		isMasterDisabled := (signerAccountRoot.Flags & state.LsfDisableMaster) != 0

		if signerAddress == signerAccountRoot.RegularKey {
			// Signed with regular key — allowed
		} else if !isMasterDisabled && signerAddress == signer.Account {
			// Signed with enabled master key — allowed
		} else if isMasterDisabled && signerAddress == signer.Account {
			// Signed with disabled master key
			return TefMASTER_DISABLED
		} else {
			// Signed with an unauthorized key
			return TefBAD_AUTH
		}
	}
	return TesSUCCESS
}

// checkBatchMultiSign verifies a multi-sign batch signer's nested Signers against
// the account's SignerList. This mirrors rippled's checkMultiSign.
// Reference: rippled Transactor::checkMultiSign in Transactor.cpp lines 742-911
func (e *Engine) checkBatchMultiSign(accountID [20]byte, txSigners []SignerInfo) Result {
	signerListKey := keylet.SignerList(accountID)
	signerListData, err := e.view.Read(signerListKey)
	if err != nil || signerListData == nil {
		return TefNOT_MULTI_SIGNING
	}

	signerList, parseErr := state.ParseSignerList(signerListData)
	if parseErr != nil {
		return TefINTERNAL
	}

	// Build a map from r-address to signer entry for O(1) lookup.
	// This avoids ordering issues between binary AccountID sort (rippled/ledger)
	// and r-address string sort (Go's AddMultiSigner).
	authorizedSigners := make(map[string]state.AccountSignerEntry, len(signerList.SignerEntries))
	for _, se := range signerList.SignerEntries {
		authorizedSigners[se.Account] = se
	}

	// Verify each tx signer is authorized and accumulate weights.
	// Reference: rippled checkMultiSign — all signers must be valid.
	var weightSum uint32

	for _, txSigner := range txSigners {
		txSignerAccountID, decErr := state.DecodeAccountID(txSigner.Account)
		if decErr != nil {
			return TefBAD_SIGNATURE
		}

		// Look up the signer in the authorized signers map
		authEntry, found := authorizedSigners[txSigner.Account]
		if !found {
			return TefBAD_SIGNATURE
		}

		// Derive account from the signer's public key
		var signingAcctIDFromPubKey string
		if txSigner.SigningPubKey == "" {
			// In simulation/dry-run mode, empty pubkey maps to the signer account itself
			signingAcctIDFromPubKey = txSigner.Account
		} else {
			addr, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(txSigner.SigningPubKey)
			if addrErr != nil {
				return TefBAD_SIGNATURE
			}
			signingAcctIDFromPubKey = addr
		}

		signerAccountKey := keylet.Account(txSignerAccountID)
		signerAccountData, readErr := e.view.Read(signerAccountKey)

		if signingAcctIDFromPubKey == txSigner.Account {
			// Either Phantom or Master key
			if readErr == nil && signerAccountData != nil {
				// Account exists — check master key not disabled
				signerAccountRoot, parseErr := state.ParseAccountRoot(signerAccountData)
				if parseErr != nil {
					return TefINTERNAL
				}
				if (signerAccountRoot.Flags & state.LsfDisableMaster) != 0 {
					return TefMASTER_DISABLED
				}
			}
			// Phantom account or master key allowed — continue
		} else {
			// May be a Regular Key
			if readErr != nil || signerAccountData == nil {
				// Non-phantom signer lacks account root
				return TefBAD_SIGNATURE
			}

			signerAccountRoot, parseErr := state.ParseAccountRoot(signerAccountData)
			if parseErr != nil {
				return TefINTERNAL
			}

			if signerAccountRoot.RegularKey == "" {
				// Account lacks RegularKey
				return TefBAD_SIGNATURE
			}

			if signingAcctIDFromPubKey != signerAccountRoot.RegularKey {
				// Wrong RegularKey
				return TefBAD_SIGNATURE
			}
		}

		// Signer is legitimate — add weight
		weightSum += uint32(authEntry.SignerWeight)
	}

	// Check quorum
	if weightSum < signerList.SignerQuorum {
		return TefBAD_QUORUM
	}

	return TesSUCCESS
}
