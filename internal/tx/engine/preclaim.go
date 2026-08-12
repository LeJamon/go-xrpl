package engine

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// preclaim validates the transaction against the current ledger state.
// Mirrors rippled's invoke_preclaim pipeline (applySteps.cpp, PR #6192):
//
//	checkSeqProxy → checkPriorTxAndLastLedger → checkSponsor →
//	checkPermission → checkSign (+ checkBatchSign) → checkFee →
//	tx-type preclaim.
//
// Delegated permission is established before signature and fee validation.
// The signature stage precedes the fee check so that no fee-charging TER is
// returned before the signature has been verified.
func (e *Engine) preclaim(tx txcore.Transaction, txHash [32]byte) (result ter.Result) {
	// Any panic reachable from adversarial ledger state — most commonly an
	// IOUAmount / XRPLNumber arithmetic overflow while reading a crafted balance
	// or amount — is recovered and surfaced as tefEXCEPTION so it can never
	// terminate the node. The ledger-mutation invariant violations that rippled
	// converted to catchable exceptions in 3.1.2 (ApplyStateTable / ApplyView
	// directory ops) are reachable only from preclaim/doApply contexts; doApply
	// is already covered by invokeApply. Mirrors rippled applySteps.cpp
	// preclaim() wrapping invoke_preclaim in try{...}catch(std::exception){
	// tefEXCEPTION }.
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("transaction preclaim panic recovered, returning tefEXCEPTION",
				"txHash", hex.EncodeToString(txHash[:]), "panic", r)
			result = ter.TefEXCEPTION
		}
	}()

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
	if result := e.checkSponsor(common); result != ter.TesSUCCESS {
		return result
	}
	if result := e.checkPermission(tx, common, accountID); result != ter.TesSUCCESS {
		return result
	}

	// The signature is verified before the fee check so that a transaction that
	// fails both reports the signature failure. No fee-charging TER
	// (terINSUF_FEE_B, ...) may precede the signature check, which would risk
	// charging a fee on an unauthorized transaction.
	// Reference: rippled applySteps.cpp invoke_preclaim (PR #6192).
	if result := e.checkSign(tx, common); result != ter.TesSUCCESS {
		return result
	}
	// checkBatchSign is part of the signature stage (rippled Batch::checkSign =
	// Transactor::checkSign + Transactor::checkBatchSign), so it moves ahead of
	// checkFee together with checkSign. It verifies each BatchSigner is
	// authorized to act as their account and runs even under
	// SkipSignatureVerification because it checks authorization (account
	// existence, master/regular key), not crypto.
	if bsp, ok := tx.(txcore.BatchSignerProvider); ok {
		if result := e.checkBatchSign(bsp.GetBatchSigners()); result != ter.TesSUCCESS {
			return result
		}
	}

	if result := e.checkFee(tx, common, account); result != ter.TesSUCCESS {
		return result
	}

	// Transaction-specific preclaim checks.
	// These run after all common preclaim checks and are subject to the
	// TapRETRY gate in Apply(). tec results from preclaim are NOT applied
	// when TapRETRY is set (likelyToClaimFee = false), matching rippled's
	// PreclaimResult semantics.
	// Reference: rippled applySteps.h — invoke_preclaim dispatches to
	// the transaction type's static preclaim() method.
	if preclaimer, ok := tx.(txcore.Preclaimer); ok {
		// Wrap the base view so Rules() reports the engine's rules: the base
		// ledger returns nil, which would silently disable rules-gated reads
		// (e.g. accountFunds' frozen-LP-token check) during preclaim.
		preclaimView := rulesView{LedgerView: e.view, rules: e.config.RequireRules()}
		if result := preclaimer.Preclaim(preclaimView, e.config); result != ter.TesSUCCESS {
			return result
		}
	}

	return ter.TesSUCCESS
}

func (e *Engine) preclaimInner(tx txcore.Transaction, txHash [32]byte) (result ter.Result) {
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("batch inner preclaim panic recovered, returning tefEXCEPTION",
				"txHash", hex.EncodeToString(txHash[:]), "panic", r)
			result = ter.TefEXCEPTION
		}
	}()

	common := tx.GetCommon()
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
	if result := e.checkSponsor(common); result != ter.TesSUCCESS {
		return result
	}
	if result := e.checkPermission(tx, common, accountID); result != ter.TesSUCCESS {
		return result
	}
	if result := e.checkPseudoAccountSign(common); result != ter.TesSUCCESS {
		return result
	}
	if preclaimer, ok := tx.(txcore.Preclaimer); ok {
		preclaimView := rulesView{LedgerView: e.view, rules: e.config.RequireRules()}
		return preclaimer.Preclaim(preclaimView, e.config)
	}
	return ter.TesSUCCESS
}

// preclaimLoadAccount decodes the source account and reads + parses its SLE.
// Returns the decoded accountID, the parsed AccountRoot, and a TER result.
func (e *Engine) preclaimLoadAccount(common *txcore.Common) ([20]byte, *state.AccountRoot, ter.Result) {
	accountID, err := state.DecodeAccountID(common.Account)
	if err != nil {
		return [20]byte{}, nil, ter.TemBAD_SRC_ACCOUNT
	}

	account, err := txcore.ReadAccountRoot(e.view, accountID)
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
func (e *Engine) checkSeqProxy(common *txcore.Common, accountID [20]byte, account *state.AccountRoot) ter.Result {
	// Check for both Sequence (non-zero) and TicketSequence set → temSEQ_AND_TICKET
	// Reference: rippled Transactor::checkSeqProxy in Transactor.cpp line 375
	if common.Sequence != nil && *common.Sequence != 0 && common.TicketSequence != nil {
		return ter.TemSEQ_AND_TICKET
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
func (e *Engine) checkPriorTxAndLastLedger(common *txcore.Common, account *state.AccountRoot, txHash [32]byte) ter.Result {
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
	exists, err := e.view.TxExists(txHash)
	if err != nil {
		return ter.TefEXCEPTION
	}
	if exists {
		return ter.TefALREADY
	}
	return ter.TesSUCCESS
}

// checkFee enforces fee adequacy and that the selected source, delegate,
// pre-funded sponsorship, or co-signed sponsor can afford the fee.
// Reference: rippled Transactor::checkFee in Transactor.cpp.
func (e *Engine) checkFee(tx txcore.Transaction, common *txcore.Common, account *state.AccountRoot) ter.Result {
	fee := e.calculateFee(tx)
	baseFeeForTx := e.preclaimBaseFee(tx)

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
			e.config.FeeTrack.LoadFactor() > feetrack.LoadBase) {
		if r := e.enforceFeeFloor(fee, baseFeeForTx); r != ter.TesSUCCESS {
			return r
		}
	}

	// Reference: rippled Transactor::checkFee line 292-293:
	//   if (feePaid == beast::zero) return tesSUCCESS;
	if fee == 0 {
		return ter.TesSUCCESS
	}

	payer, result := e.getFeePayer(common)
	if result != ter.TesSUCCESS {
		return result
	}
	_, maxSpendable, result := e.feePayerBalanceAndSpendable(payer, account)
	if result != ter.TesSUCCESS {
		return result
	}
	if maxSpendable < fee {
		// Reference: rippled Transactor::checkFee lines 304-316. Only a closed
		// ledger with a non-zero balance below the fee yields a deterministic
		// claimed-fee result; on any open view (open ledger, queued retry, or
		// load-fee enforcement — rippled's ctx.view.open()) it is retryable.
		if maxSpendable > 0 && !e.config.IsViewOpen() {
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
	unlimited := e.config.ApplyFlags&txcore.TapUNLIMITED != 0
	feeDue, scaleErr := feetrack.ScaleFeeLoad(baseFeeForTx, e.config.FeeTrack, unlimited)
	if scaleErr != nil {
		return ter.TelINSUF_FEE_P
	}
	if fee < feeDue {
		return ter.TelINSUF_FEE_P
	}
	return ter.TesSUCCESS
}

func (e *Engine) preclaimBaseFee(tx txcore.Transaction) uint64 {
	return sign.CalculateBaseFee(tx, e.view, e.config)
}

// checkPermission validates that, when sfDelegate is set, the delegate SLE
// grants permission for this transaction type.
// Reference: rippled Transactor::checkPermission in Transactor.cpp lines 213-227
// and DelegateUtils.cpp checkTxPermission().
func (e *Engine) checkPermission(tx txcore.Transaction, common *txcore.Common, accountID [20]byte) ter.Result {
	if common.Delegate == "" {
		return ter.TesSUCCESS
	}
	delegateID, _ := state.DecodeAccountID(common.Delegate)
	delegateKeylet := keylet.Delegate(accountID, delegateID)
	delegateData, readErr := e.view.Read(delegateKeylet)
	if readErr != nil || delegateData == nil {
		return ter.TerNO_DELEGATE_PERMISSION
	}
	delegateEntry, parseErr := state.ParseDelegate(delegateData)
	if parseErr != nil {
		return ter.TerNO_DELEGATE_PERMISSION
	}
	// A transaction-level grant (permissionValue == txType + 1) authorizes
	// every action of this transaction type.
	if delegateEntry.HasTxPermission(uint32(tx.TxType())) {
		return ter.TesSUCCESS
	}
	// Otherwise a granular permission may still authorize a specific slice of
	// the transaction's behaviour. Only permissions for this transaction type
	// contribute to the union of allowed flags and fields.
	heldPermissions := txcore.GranularPermissionsFor(tx.TxType(), delegateEntry.Permissions)
	if !txcore.CheckGranularPermissionTemplate(tx, heldPermissions) {
		return ter.TerNO_DELEGATE_PERMISSION
	}
	// Transaction types with extra granular semantics evaluate them only after
	// the shared permission template succeeds.
	if checker, ok := tx.(txcore.DelegatePermissionChecker); ok {
		return checker.CheckDelegatePermission(txcore.DelegatePermissionContext{
			View:        e.view,
			Rules:       e.config.RequireRules(),
			Permissions: heldPermissions,
		})
	}
	return ter.TerNO_DELEGATE_PERMISSION
}

// checkSponsor validates the common sponsorship permission before signature
// authorization and fee selection. A co-signature bypasses the relationship
// SLE, but never the sponsor-account existence check.
func (e *Engine) checkSponsor(common *txcore.Common) ter.Result {
	if common.Sponsor == "" {
		if common.HasField("Sponsor") {
			return ter.TerNO_ACCOUNT
		}
		return ter.TesSUCCESS
	}
	if common.Delegate != "" && common.SponsorFlags != nil &&
		*common.SponsorFlags&txcore.SpfSponsorReserve != 0 {
		return ter.TemINVALID
	}

	sponsorID, err := state.DecodeAccountID(common.Sponsor)
	if err != nil {
		return ter.TerNO_ACCOUNT
	}
	sponsor, readErr := txcore.ReadAccountRoot(e.view, sponsorID)
	if readErr != nil {
		return ter.TefINTERNAL
	}
	if sponsor == nil {
		return ter.TerNO_ACCOUNT
	}
	if common.SponsorSignature != nil {
		return ter.TesSUCCESS
	}

	initiator := common.Account
	if common.Delegate != "" {
		initiator = common.Delegate
	}
	initiatorID, err := state.DecodeAccountID(initiator)
	if err != nil {
		return ter.TerNO_PERMISSION
	}
	data, readErr := e.view.Read(keylet.Sponsorship(sponsorID, initiatorID))
	if readErr != nil {
		return ter.TefINTERNAL
	}
	if data == nil {
		return ter.TerNO_PERMISSION
	}
	sponsorship, parseErr := state.ParseSponsorship(data)
	if parseErr != nil {
		return ter.TefINTERNAL
	}
	flags := uint32(0)
	if common.SponsorFlags != nil {
		flags = *common.SponsorFlags
	}
	if flags&txcore.SpfSponsorFee != 0 &&
		sponsorship.Flags&entry.LsfSponsorshipRequireSignForFee != 0 {
		return ter.TerNO_PERMISSION
	}
	if flags&txcore.SpfSponsorReserve != 0 &&
		sponsorship.Flags&entry.LsfSponsorshipRequireSignForReserve != 0 {
		return ter.TerNO_PERMISSION
	}
	return ter.TesSUCCESS
}

// checkSign performs sponsor authorization first, then the transaction's
// ordinary single- or multi-sign authorization.
// Reference: rippled Transactor::checkSign in Transactor.cpp.
// When a delegate is present, the idAccount for signature checking is the
// delegate. Reference: rippled line 602:
//
//	auto const idAccount = ctx.tx[~sfDelegate].value_or(ctx.tx[sfAccount]);
func (e *Engine) checkSign(tx txcore.Transaction, common *txcore.Common) ter.Result {
	if sponsor := common.SponsorSignature; sponsor != nil {
		// Dry-run/test mode permits an empty signature object. Real submissions
		// have already failed crypto verification in preflight.
		if !(e.config.SkipSignatureVerification &&
			sponsor.SigningPubKey == "" && len(sponsor.Signers) == 0) {
			if result := e.rejectPseudoAccount(common.Sponsor); result != ter.TesSUCCESS {
				return result
			}
			if len(sponsor.Signers) > 0 {
				if result := e.checkMultiSignForAccount(common.Sponsor, sponsor.Signers); result != ter.TesSUCCESS {
					return result
				}
			} else if result := e.checkSingleSignForAccount(common.Sponsor, sponsor.SigningPubKey); result != ter.TesSUCCESS {
				return result
			}
		}
	}
	if result := e.checkPseudoAccountSign(common); result != ter.TesSUCCESS {
		return result
	}
	if sign.IsMultiSigned(tx) {
		return e.checkMultiSign(common)
	}
	if common.SigningPubKey != "" {
		return e.checkSingleSign(common)
	}
	return ter.TesSUCCESS
}

func (e *Engine) checkPseudoAccountSign(common *txcore.Common) ter.Result {
	idAccount := common.Account
	if common.Delegate != "" {
		idAccount = common.Delegate
	}
	return e.checkPseudoAccount(idAccount)
}

func (e *Engine) checkPseudoAccount(idAccount string) ter.Result {
	if !e.rules().Enabled(amendment.FeatureLendingProtocol) &&
		!e.rules().Enabled(amendment.FeatureBatchV1_1) {
		return ter.TesSUCCESS
	}
	return e.rejectPseudoAccount(idAccount)
}

func (e *Engine) rejectPseudoAccount(idAccount string) ter.Result {
	if idAccountID, err := state.DecodeAccountID(idAccount); err == nil {
		if data, rerr := e.view.Read(keylet.Account(idAccountID)); rerr == nil && data != nil {
			if ar, perr := state.ParseAccountRoot(data); perr == nil && ar.IsPseudoAccount() {
				return ter.TefBAD_AUTH
			}
		}
	}
	return ter.TesSUCCESS
}

// checkMultiSign verifies the multi-sign signers against the idAccount's
// SignerList and quorum.
// Reference: rippled Transactor::checkMultiSign in Transactor.cpp lines 743-911.
func (e *Engine) checkMultiSign(common *txcore.Common) ter.Result {
	// Multi-signed transaction: always check signer authorization and quorum.
	// This runs regardless of SkipSignatureVerification because quorum and
	// signer authorization (master key disabled, regular key, phantom accounts)
	// are ledger-state checks, not cryptographic checks.
	idAccount := common.Account
	if common.Delegate != "" {
		idAccount = common.Delegate
	}
	return e.checkMultiSignForAccount(idAccount, common.Signers)
}

func (e *Engine) checkMultiSignForAccount(idAccount string, signers []txcore.SignerWrapper) ter.Result {
	idAccountID, idErr := state.DecodeAccountID(idAccount)
	if idErr != nil {
		return ter.TefBAD_SIGNATURE
	}
	// Convert tx Signers to SignerInfo for checkBatchMultiSign
	txSigners := make([]txcore.SignerInfo, len(signers))
	for i, sw := range signers {
		txSigners[i] = txcore.SignerInfo{
			Account:       sw.Signer.Account,
			SigningPubKey: sw.Signer.SigningPubKey,
		}
	}
	return e.checkBatchMultiSign(idAccountID, txSigners)
}

// checkSingleSign validates a single-signed transaction's signing key against
// the idAccount's master/regular key configuration.
// Reference: rippled Transactor::checkSingleSign in Transactor.cpp lines 682-740.
func (e *Engine) checkSingleSign(common *txcore.Common) ter.Result {
	idAccount := common.Account
	if common.Delegate != "" {
		idAccount = common.Delegate
	}
	return e.checkSingleSignForAccount(idAccount, common.SigningPubKey)
}

func (e *Engine) checkSingleSignForAccount(idAccount, signingPubKey string) ter.Result {
	// Single-signed transaction: check signing key authorization.
	// This runs regardless of SkipSignatureVerification because authorization
	// (master key disabled, regular key) is a ledger-state check, not a
	// cryptographic check. The actual signature verification is done in
	// Validate() and gated by SkipSignatureVerification.
	signerAddress, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(signingPubKey)
	if addrErr != nil {
		return ter.TefBAD_AUTH
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

	// Check regular key first, then master. This allows the master key to serve
	// as a regular key even when master signing is disabled (e.g., regkey(alice,
	// alice) + disable master).
	// Reference: rippled Transactor::checkSingleSign.
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

// checkBatchSign verifies that each batch signer is authorized to sign for their account.
// For single-sign signers (SigningPubKey non-empty): derives account from pubkey, checks authorization.
// For multi-sign signers (SigningPubKey empty): checks signer list exists and quorum is met.
// Reference: rippled Transactor::checkBatchSign in Transactor.cpp lines 635-679
func (e *Engine) checkBatchSign(signers []txcore.BatchSignerInfo) ter.Result {
	for _, signer := range signers {
		signerAccountID, err := state.DecodeAccountID(signer.Account)
		if err != nil {
			return ter.TefBAD_AUTH
		}
		if result := e.rejectPseudoAccount(signer.Account); result != ter.TesSUCCESS {
			return result
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
func (e *Engine) checkBatchMultiSign(accountID [20]byte, txSigners []txcore.SignerInfo) ter.Result {
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

		var acct sign.SignerAccountState
		if signerAccountData != nil {
			signerAccountRoot, parseErr := state.ParseAccountRoot(signerAccountData)
			if parseErr != nil {
				return ter.TefINTERNAL
			}
			acct = sign.NewSignerAccountState(true, signerAccountRoot.Flags, signerAccountRoot.RegularKey)
		}

		if r := sign.AuthorizeMultiSigner(txSigner.Account, signingAcctIDFromPubKey, acct); r != ter.TesSUCCESS {
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
