package tx

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// preclaim validates the transaction against the current ledger state.
// Mirrors rippled's Transactor::operator()() pre-application pipeline:
//
//	checkSeqProxy → checkPriorTxAndLastLedger → checkFee → checkPermission →
//	checkSign (+ checkBatchSign) → tx-type preclaim.
func (e *Engine) preclaim(tx Transaction, txHash [32]byte) ter.Result {
	common := tx.GetCommon()

	// Resolve and parse the source account; this is shared by all subsequent steps.
	accountID, account, result := e.preclaimLoadAccount(common)
	if result != ter.TesSUCCESS {
		return result
	}

	if result := e.checkSeqProxy(common, accountID, account); result != ter.TesSUCCESS {
		return result
	}
	if result := e.checkPriorTxAndLastLedger(common, account, txHash); result != ter.TesSUCCESS {
		return result
	}
	if result := e.checkFee(tx, common, account); result != ter.TesSUCCESS {
		return result
	}
	if result := e.checkPermission(tx, common, accountID); result != ter.TesSUCCESS {
		return result
	}
	if result := e.checkSign(tx, common); result != ter.TesSUCCESS {
		return result
	}

	// Step 6: checkBatchSign — batch signer authorization
	// Reference: rippled Batch::checkSign -> Transactor::checkBatchSign
	// This checks that each BatchSigner is authorized to act as their account.
	// This runs even when SkipSignatureVerification is true because it checks
	// authorization (account existence, master key, regular key), not crypto.
	if bsp, ok := tx.(BatchSignerProvider); ok {
		if result := e.checkBatchSign(bsp.GetBatchSigners()); result != ter.TesSUCCESS {
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
		// Wrap the base view so Rules() reports the engine's rules: the base
		// ledger returns nil, which would silently disable rules-gated reads
		// (e.g. accountFunds' frozen-LP-token check) during preclaim.
		preclaimView := rulesView{LedgerView: e.view, rules: e.config.GetRules()}
		if result := preclaimer.Preclaim(preclaimView, e.config); result != ter.TesSUCCESS {
			return result
		}
	}

	return ter.TesSUCCESS
}

// preclaimLoadAccount decodes the source account and reads + parses its SLE.
// Returns the decoded accountID, the parsed AccountRoot, and a TER result.
func (e *Engine) preclaimLoadAccount(common *Common) ([20]byte, *state.AccountRoot, ter.Result) {
	accountID, err := state.DecodeAccountID(common.Account)
	if err != nil {
		return [20]byte{}, nil, ter.TemBAD_SRC_ACCOUNT
	}

	account, err := ReadAccountRoot(e.view, accountID)
	if err != nil {
		return accountID, nil, ter.TefINTERNAL
	}
	if account == nil {
		return accountID, nil, ter.TerNO_ACCOUNT
	}
	return accountID, account, ter.TesSUCCESS
}

// checkSeqProxy validates Sequence/TicketSequence against the account state.
// Reference: rippled Transactor::checkSeqProxy in Transactor.cpp.
func (e *Engine) checkSeqProxy(common *Common, accountID [20]byte, account *state.AccountRoot) ter.Result {
	// Check for both Sequence (non-zero) and TicketSequence set → temSEQ_AND_TICKET
	// Reference: rippled Transactor::checkSeqProxy in Transactor.cpp line 375
	if common.Sequence != nil && *common.Sequence != 0 && common.TicketSequence != nil {
		if e.rules().Enabled(amendment.FeatureTicketBatch) {
			return ter.TemSEQ_AND_TICKET
		}
	}

	// Check sequence number or ticket
	if common.TicketSequence != nil {
		// Ticket-based transaction: validate the ticket exists
		if *common.TicketSequence >= account.Sequence {
			// Ticket hasn't been created yet
			return ter.TerPRE_TICKET
		}
		ticketKey := keylet.Ticket(accountID, *common.TicketSequence)
		ticketExists, ticketErr := e.view.Exists(ticketKey)
		if ticketErr != nil || !ticketExists {
			return ter.TefNO_TICKET
		}
	} else if common.Sequence != nil {
		if *common.Sequence < account.Sequence {
			return ter.TefPAST_SEQ
		}
		if *common.Sequence > account.Sequence {
			return ter.TerPRE_SEQ
		}
	}
	return ter.TesSUCCESS
}

// checkPriorTxAndLastLedger validates AccountTxnID, LastLedgerSequence, and
// dedupes by transaction hash.
// Reference: rippled Transactor::checkPriorTxAndLastLedger in Transactor.cpp.
func (e *Engine) checkPriorTxAndLastLedger(common *Common, account *state.AccountRoot, txHash [32]byte) ter.Result {
	// AccountTxnID check — if the transaction specifies an AccountTxnID, it must match
	// the account's stored AccountTxnID (the hash of the last tx this account submitted).
	if common.AccountTxnID != "" {
		txAccountTxnID, decErr := hex.DecodeString(common.AccountTxnID)
		if decErr != nil || len(txAccountTxnID) != 32 {
			return ter.TefWRONG_PRIOR
		}
		var txPrior [32]byte
		copy(txPrior[:], txAccountTxnID)
		if txPrior != account.AccountTxnID {
			return ter.TefWRONG_PRIOR
		}
	}

	// LastLedgerSequence check
	if common.LastLedgerSequence != nil {
		if e.config.LedgerSequence > *common.LastLedgerSequence {
			return ter.TefMAX_LEDGER
		}
	}

	// Duplicate transaction detection — if this transaction hash already exists in the
	// view (already applied to this ledger), return tefALREADY.
	// Reference: rippled Transactor::checkPriorTxAndLastLedger — ctx.view.txExists()
	if e.view.TxExists(txHash) {
		return ter.TefALREADY
	}
	return ter.TesSUCCESS
}

// checkFee enforces fee adequacy and that the fee payer (delegate or source)
// can afford the fee. Reference: rippled Transactor::checkFee in Transactor.cpp.
func (e *Engine) checkFee(tx Transaction, common *Common, account *state.AccountRoot) ter.Result {
	// When a delegate is present, the fee is checked against the delegate's balance.
	fee := e.calculateFee(tx)
	baseFeeForTx := e.preclaimBaseFee(tx, common, account)

	// Fee adequacy floor. rippled enforces feePaid >= minimumFee whenever the
	// apply view is open (Transactor::checkFee, Transactor.cpp:278-290), with
	// minimumFee = scaleFeeLoad(baseFee, feeTrack, unlimited); when the view is
	// not open, fee=0 is accepted (Transactor.cpp:292-293). go-xrpl reaches that
	// floor on two gates that share the same check:
	//   - OpenLedger: the open-ledger submission path always enforces it.
	//   - EnforceLoadFee: the TxQ direct-apply / clear-queue / accept paths,
	//     which target the open ledger but run with OpenLedger=false (rippled's
	//     tapNONE). They enforce only while load is elevated. At normal load the
	//     base-fee floor is already guaranteed by the TxQ admission check, and
	//     keeping OpenLedger=false avoids re-rejecting the fee=0 txns those paths
	//     legitimately carry (the SetRegularKey free password change) and the
	//     pseudo-tx gating the OpenLedger flag also controls.
	if e.config.OpenLedger ||
		(e.config.EnforceLoadFee && e.config.FeeTrack != nil &&
			e.config.FeeTrack.GetLoadFactor() > feetrack.LoadBase) {
		if r := e.enforceFeeFloor(fee, baseFeeForTx); r != ter.TesSUCCESS {
			return r
		}
	}

	// When fee is zero, skip batch fee check and balance checks.
	// Reference: rippled Transactor::checkFee line 292-293:
	//   if (feePaid == beast::zero) return tesSUCCESS;
	if fee == 0 {
		return ter.TesSUCCESS
	}

	if feeCalc, ok := tx.(BatchFeeCalculator); ok {
		batchMinFee := feeCalc.CalculateMinimumFee(e.config.BaseFee)
		if fee < batchMinFee {
			return ter.TelINSUF_FEE_P
		}
	}

	// Determine who pays the fee: delegate (if present) or the source account.
	// Reference: rippled Transactor::checkFee lines 295-297:
	//   auto const id = ctx.tx.isFieldPresent(sfDelegate)
	//       ? ctx.tx.getAccountID(sfDelegate)
	//       : ctx.tx.getAccountID(sfAccount);
	feePayerBalance, balResult := e.feePayerBalance(common, account)
	if balResult != ter.TesSUCCESS {
		return balResult
	}
	if feePayerBalance < fee {
		// Reference: rippled Transactor::checkFee lines 304-316. On a closed
		// ledger, a non-zero balance below the fee yields a deterministic
		// claimed-fee result; otherwise the transaction is retryable.
		if feePayerBalance > 0 && !e.config.OpenLedger {
			return ter.TecINSUFF_FEE
		}
		return ter.TerINSUF_FEE_B
	}
	return ter.TesSUCCESS
}

// enforceFeeFloor rejects a fee below the load-scaled minimum, mirroring
// rippled's open-ledger floor: feeDue = scaleFeeLoad(baseFee, feeTrack,
// unlimited); feePaid < feeDue → telINSUF_FEE_P. A scaleFeeLoad overflow (the
// floor exceeds any payable fee, where rippled throws) resolves to the same
// insufficient-fee code. Reference: rippled Transactor::checkFee
// Transactor.cpp:278-290.
func (e *Engine) enforceFeeFloor(fee, baseFeeForTx uint64) ter.Result {
	unlimited := e.config.ApplyFlags&TapUNLIMITED != 0
	feeDue, scaleErr := feetrack.ScaleFeeLoad(baseFeeForTx, e.config.FeeTrack, unlimited)
	if scaleErr != nil {
		return ter.TelINSUF_FEE_P
	}
	if fee < feeDue {
		return ter.TelINSUF_FEE_P
	}
	return ter.TesSUCCESS
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
	// SetRegularKey free password change: the base fee is waived when signed
	// with the master key while lsfPasswordSpent is clear. The same predicate
	// gates the lsfPasswordSpent flag in doApply, so the fee and the flag can
	// never disagree. Reference: rippled SetRegularKey.cpp calculateBaseFee.
	if tx.TxType() == TypeRegularKeySet && SetRegularKeyFeeWaived(e.config.SkipSignatureVerification, common, account) {
		baseFeeForTx = 0
	}
	return baseFeeForTx
}

// feePayerBalance returns the balance of the account that will be charged the fee
// (delegate when sfDelegate is present, otherwise the source account).
func (e *Engine) feePayerBalance(common *Common, account *state.AccountRoot) (uint64, ter.Result) {
	if common.Delegate == "" {
		return account.Balance, ter.TesSUCCESS
	}
	delegateID, delegateErr := state.DecodeAccountID(common.Delegate)
	if delegateErr != nil {
		return 0, ter.TerNO_ACCOUNT
	}
	delegateAccount, readErr := ReadAccountRoot(e.view, delegateID)
	if readErr != nil {
		// Real storage or parse failure, not a missing account.
		return 0, ter.TefINTERNAL
	}
	if delegateAccount == nil {
		return 0, ter.TerNO_ACCOUNT
	}
	return delegateAccount.Balance, ter.TesSUCCESS
}

// checkPermission validates that, when sfDelegate is set, the delegate SLE
// grants permission for this transaction type.
// Reference: rippled Transactor::checkPermission in Transactor.cpp lines 213-227
// and DelegateUtils.cpp checkTxPermission().
func (e *Engine) checkPermission(tx Transaction, common *Common, accountID [20]byte) ter.Result {
	if common.Delegate == "" {
		return ter.TesSUCCESS
	}
	delegateID, _ := state.DecodeAccountID(common.Delegate)
	delegateKeylet := keylet.Delegate(accountID, delegateID)
	delegateData, readErr := e.view.Read(delegateKeylet)
	if readErr != nil || delegateData == nil {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	delegateEntry, parseErr := state.ParseDelegate(delegateData)
	if parseErr != nil {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	// Check if the delegate SLE grants permission for this tx type.
	// In rippled: permissionValue == tx.getTxnType() + 1
	txTypeValue := uint32(tx.TxType())
	if !delegateEntry.HasTxPermission(txTypeValue) {
		return ter.TecNO_DELEGATE_PERMISSION
	}
	return ter.TesSUCCESS
}

// checkSign performs signature authorization for both single-signed and
// multi-signed transactions, dispatching to checkSingleSign / checkMultiSign.
// Reference: rippled Transactor::checkSign in Transactor.cpp.
// When a delegate is present, the idAccount for signature checking is the
// delegate. Reference: rippled line 602:
//
//	auto const idAccount = ctx.tx[~sfDelegate].value_or(ctx.tx[sfAccount]);
func (e *Engine) checkSign(tx Transaction, common *Common) ter.Result {
	if IsMultiSigned(tx) {
		return e.checkMultiSign(common)
	}
	if common.SigningPubKey != "" {
		return e.checkSingleSign(common)
	}
	return ter.TesSUCCESS
}

// checkMultiSign verifies the multi-sign signers against the idAccount's
// SignerList and quorum.
// Reference: rippled Transactor::checkMultiSign in Transactor.cpp lines 743-911.
func (e *Engine) checkMultiSign(common *Common) ter.Result {
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
		return ter.TefBAD_SIGNATURE
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
func (e *Engine) checkSingleSign(common *Common) ter.Result {
	// Single-signed transaction: check signing key authorization.
	// This runs regardless of SkipSignatureVerification because authorization
	// (master key disabled, regular key) is a ledger-state check, not a
	// cryptographic check. The actual signature verification is done in
	// Validate() and gated by SkipSignatureVerification.
	signerAddress, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(common.SigningPubKey)
	if addrErr != nil {
		return ter.TefBAD_AUTH
	}

	// Determine the idAccount: delegate if present, else source account.
	idAccount := common.Account
	if common.Delegate != "" {
		idAccount = common.Delegate
	}

	// Read the idAccount's data for signature authorization check
	idAccountID, idErr := state.DecodeAccountID(idAccount)
	if idErr != nil {
		return ter.TefBAD_AUTH
	}
	idAccountKey := keylet.Account(idAccountID)
	idAccountData, idReadErr := e.view.Read(idAccountKey)
	if idReadErr != nil || idAccountData == nil {
		return ter.TerNO_ACCOUNT
	}
	idAccountRoot, idParseErr := state.ParseAccountRoot(idAccountData)
	if idParseErr != nil {
		return ter.TefINTERNAL
	}

	isMasterDisabled := (idAccountRoot.Flags & state.LsfDisableMaster) != 0

	if e.rules().Enabled(amendment.FeatureFixMasterKeyAsRegularKey) {
		// With fixMasterKeyAsRegularKey: check regular key first, then master.
		// This allows the master key to serve as a regular key even when
		// master signing is disabled (e.g., regkey(alice, alice) + disable master).
		// Reference: rippled Transactor::checkSingleSign lines 691-713
		if signerAddress == idAccountRoot.RegularKey {
			// Signed with regular key — allowed
			return ter.TesSUCCESS
		}
		if !isMasterDisabled && signerAddress == idAccount {
			// Signed with enabled master key — allowed
			return ter.TesSUCCESS
		}
		if isMasterDisabled && signerAddress == idAccount {
			// Signed with disabled master key
			return ter.TefMASTER_DISABLED
		}
		// Signed with an unauthorized key
		return ter.TefBAD_AUTH
	}

	// Without fixMasterKeyAsRegularKey: check master key first.
	// If signer == account, it's a master key sign attempt.
	// The regular key is only checked if signer != account.
	// Reference: rippled Transactor::checkSingleSign lines 715-737
	if signerAddress == idAccount {
		// Signing with the master key. Continue if it is not disabled.
		if isMasterDisabled {
			return ter.TefMASTER_DISABLED
		}
		return ter.TesSUCCESS
	}
	if signerAddress == idAccountRoot.RegularKey {
		// Signing with the regular key. Continue.
		return ter.TesSUCCESS
	}
	if idAccountRoot.RegularKey != "" {
		// Signing key does not match master or regular key.
		return ter.TefBAD_AUTH
	}
	// No regular key on account and signing key does not match master key.
	return ter.TefBAD_AUTH_MASTER
}

// checkBatchSign verifies that each batch signer is authorized to sign for their account.
// For single-sign signers (SigningPubKey non-empty): derives account from pubkey, checks authorization.
// For multi-sign signers (SigningPubKey empty): checks signer list exists and quorum is met.
// Reference: rippled Transactor::checkBatchSign in Transactor.cpp lines 635-679
func (e *Engine) checkBatchSign(signers []BatchSignerInfo) ter.Result {
	for _, signer := range signers {
		signerAccountID, err := state.DecodeAccountID(signer.Account)
		if err != nil {
			return ter.TefBAD_AUTH
		}

		if signer.SigningPubKey == "" {
			// Multi-sign batch signer: check nested Signers against the account's SignerList.
			// Reference: rippled checkBatchSign -> checkMultiSign
			if result := e.checkBatchMultiSign(signerAccountID, signer.Signers); result != ter.TesSUCCESS {
				return result
			}
			continue
		}

		// Single-sign batch signer: derive account from public key
		signerAddress, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(signer.SigningPubKey)
		if addrErr != nil {
			return ter.TefBAD_AUTH
		}

		signerAccountKey := keylet.Account(signerAccountID)
		signerAccountData, readErr := e.view.Read(signerAccountKey)
		if readErr != nil {
			// Real storage failure — view.read() cannot fail in rippled, so a
			// genuine read error here is an internal fault, not a missing account.
			return ter.TefINTERNAL
		}

		if signerAccountData == nil {
			// Account doesn't exist: only allowed if the signer pubkey derives to this account
			// (phantom account pattern — the signer IS the account)
			if signerAddress != signer.Account {
				return ter.TefBAD_AUTH
			}
			// Phantom account — allowed
			continue
		}

		signerAccountRoot, parseErr := state.ParseAccountRoot(signerAccountData)
		if parseErr != nil {
			return ter.TefINTERNAL
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
			return ter.TefMASTER_DISABLED
		} else {
			// Signed with an unauthorized key
			return ter.TefBAD_AUTH
		}
	}
	return ter.TesSUCCESS
}

// checkBatchMultiSign verifies a multi-sign batch signer's nested Signers against
// the account's SignerList. This mirrors rippled's checkMultiSign.
// Reference: rippled Transactor::checkMultiSign in Transactor.cpp lines 742-911
func (e *Engine) checkBatchMultiSign(accountID [20]byte, txSigners []SignerInfo) ter.Result {
	signerListKey := keylet.SignerList(accountID)
	signerListData, err := e.view.Read(signerListKey)
	if err != nil || signerListData == nil {
		return ter.TefNOT_MULTI_SIGNING
	}

	signerList, parseErr := state.ParseSignerList(signerListData)
	if parseErr != nil {
		return ter.TefINTERNAL
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
			return ter.TefBAD_SIGNATURE
		}

		// Look up the signer in the authorized signers map
		authEntry, found := authorizedSigners[txSigner.Account]
		if !found {
			return ter.TefBAD_SIGNATURE
		}

		// Derive account from the signer's public key
		var signingAcctIDFromPubKey string
		if txSigner.SigningPubKey == "" {
			// In simulation/dry-run mode, empty pubkey maps to the signer account itself
			signingAcctIDFromPubKey = txSigner.Account
		} else {
			addr, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(txSigner.SigningPubKey)
			if addrErr != nil {
				return ter.TefBAD_SIGNATURE
			}
			signingAcctIDFromPubKey = addr
		}

		signerAccountKey := keylet.Account(txSignerAccountID)
		signerAccountData, readErr := e.view.Read(signerAccountKey)
		if readErr != nil {
			// Real storage failure — distinct from a missing account, which
			// view.read() signals as nil data. Never fold it into the phantom branch.
			return ter.TefINTERNAL
		}

		var acct signerAccountState
		if signerAccountData != nil {
			signerAccountRoot, parseErr := state.ParseAccountRoot(signerAccountData)
			if parseErr != nil {
				return ter.TefINTERNAL
			}
			acct = signerAccountState{
				found:      true,
				flags:      signerAccountRoot.Flags,
				regularKey: signerAccountRoot.RegularKey,
			}
		}

		if r := authorizeMultiSigner(txSigner.Account, signingAcctIDFromPubKey, acct); r != ter.TesSUCCESS {
			return r
		}

		// Signer is legitimate — add weight
		weightSum += uint32(authEntry.SignerWeight)
	}

	// Check quorum
	if weightSum < signerList.SignerQuorum {
		return ter.TefBAD_QUORUM
	}

	return ter.TesSUCCESS
}
