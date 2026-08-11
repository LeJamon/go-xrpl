package engine

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/invariants"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// preflight performs initial validation on the transaction.
// Mirrors rippled Transactor::preflight() which composes preflight0/preflight1/preflight2
// and the per-tx-type preflight. The blocks below are extracted helpers so this
// top-level function reads as a high-level pipeline.
func (e *Engine) preflight(tx txcore.Transaction) (result ter.Result) {
	// Any panic reachable from crafted transaction fields — most commonly an
	// IOUAmount / XRPLNumber arithmetic overflow on adversarial data — is
	// recovered and surfaced as tefEXCEPTION so it can never terminate the node.
	// A tef* result is never included in a ledger, so there is no consensus
	// impact. Mirrors rippled applySteps.cpp preflight() wrapping
	// invoke_preflight in try{...}catch(std::exception){ {tefEXCEPTION} }.
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("transaction preflight panic recovered, returning tefEXCEPTION",
				"txType", tx.TxType().String(), "panic", r)
			result = ter.TefEXCEPTION
		}
	}()

	common := tx.GetCommon()
	rules := e.rules()
	currentTxID, currentTxIDErr := txcore.ComputeCurrentTransactionHash(tx)

	// Structural preflight is ledger-state-independent, so its verdict is
	// memoised on the transaction and a re-preflight under the same rules skips
	// the repeat (see Common.preflightedRules). Signature verification stays out
	// of the memo and always runs below, so a multi-signed tx's view-dependent
	// signer-list check is never cached.
	if currentTxIDErr != nil || !common.PreflightVerified(rules, currentTxID) {
		if result := e.preflightStructure(tx, common); result != ter.TesSUCCESS {
			return result
		}
		if currentTxIDErr == nil {
			common.MarkPreflightVerified(rules, currentTxID)
		}
	}

	// preflight2 — cryptographic signature verification runs LAST, after the
	// type-specific checks, mirroring rippled where preflight2()'s checkValidity
	// is the final step of every tx's preflight(). A transaction that is both
	// malformed and mis-signed therefore surfaces its type-specific tem* code,
	// not the signature code.
	if result := e.verifySignatures(tx); result != ter.TesSUCCESS {
		return result
	}

	// preflightSigValidated — a per-type stage rippled runs AFTER preflight2's
	// signature verification (Transactor::invokePreflight). A check placed here
	// (EscrowFinish's CredentialIDs shape check) is therefore trumped by a
	// bad-signature temINVALID, not the reverse. Reached only once verifySignatures
	// succeeds, exactly as rippled reaches it only once preflight2 passes.
	if svp, ok := tx.(txcore.SigValidatedPreflighter); ok {
		if err := svp.PreflightSigValidated(); err != nil {
			return parseValidationError(err)
		}
	}

	return ter.TesSUCCESS
}

// preflightStructure runs the ledger-state-independent preflight checks in
// rippled's invokePreflight order: the amendment gate and T::checkExtraFeatures,
// then preflight1 (which invokes preflight0), then the per-type preflight body,
// then the preflight2 structural (multi-sign) checks. Signature verification
// itself runs separately in verifySignatures. This is a pure function of the
// transaction fields and the active rules, which is what makes its verdict safe
// to memoise (see Common.PreflightVerified).
// Reference: rippled Transactor.h Transactor::invokePreflight<T>.
func (e *Engine) preflightStructure(tx txcore.Transaction, common *txcore.Common) ter.Result {
	rules := e.rules()
	if tx.TxType() == txcore.TypeClawback {
		if err := txcore.ValidateTransactionTemplateAllowlist(tx); err != nil {
			return ter.TemMALFORMED
		}
	}

	// The transactions.macro amendment gate (rippled Permission::getTxFeature →
	// temDISABLED) is the FIRST check, before checkExtraFeatures and preflight1,
	// so a disabled tx type is rejected before any NetworkID/account/fee TER.
	for _, featureID := range tx.RequiredAmendments() {
		if !rules.Enabled(featureID) {
			return ter.TemDISABLED
		}
	}

	// T::checkExtraFeatures runs before preflight1's common checks (see
	// ExtraFeaturesChecker).
	if result := checkExtraFeatures(tx, rules); result != ter.TesSUCCESS {
		return result
	}

	// preflight1 (which itself runs preflight0).
	if result := e.preflight1(tx, common, rules); result != ter.TesSUCCESS {
		return result
	}

	// preflightUniversal — after preflight1 and before T::preflight, reject a
	// transaction carrying a non-canonical native or MPT amount in any field
	// (rippled Transactor::preflightUniversal, gated on fixCleanup3_2_0).
	if rules.FixCleanup3_2_0Enabled() && hasInvalidAmount(tx) {
		return ter.TemBAD_AMOUNT
	}

	// T::preflight — the tx-type-specific body.
	if result := runTypePreflight(tx, rules); result != ter.TesSUCCESS {
		return result
	}
	if outer, ok := tx.(txcore.BatchInnerPreflightRunner); ok {
		if err := outer.PreflightInnerTransactions(e.preflightInner); err != nil {
			return parseValidationError(err)
		}
	}

	// preflight2 structural stage — the multi-sign structural rules run after
	// T::preflight (rippled STTx::multiSignHelper reached via checkValidity) and
	// a violation is Validity::SigBad → temINVALID. The per-signer cryptographic
	// verification runs later in verifySignatures.
	if result := e.preflightMultiSignStructure(tx, common); result != ter.TesSUCCESS {
		return result
	}
	if result := e.preflightSponsorSignStructure(common); result != ter.TesSUCCESS {
		return result
	}
	if result := e.preflightBatchSignerStructure(tx); result != ter.TesSUCCESS {
		return result
	}

	return ter.TesSUCCESS
}

// preflight1 runs rippled Transactor::preflight1 (which invokes preflight0) in
// its exact order: delegate → preflight0 (NetworkID, flags mask) → account →
// fee → signing-key shape → ticket+AccountTxnID → the outer-only
// tfInnerBatchTxn rejection → Sponsor field validation.
// Reference: rippled Transactor.cpp preflight1/preflight0.
func (e *Engine) preflight1(tx txcore.Transaction, common *txcore.Common, rules *amendment.Rules) ter.Result {
	// The delegate check precedes preflight0 (and thus the NetworkID checks).
	if result := checkDelegate(tx, common, rules); result != ter.TesSUCCESS {
		return result
	}
	// preflight0: NetworkID canonicality then the flags mask.
	if result := e.preflight0(tx, common, rules); result != ter.TesSUCCESS {
		return result
	}
	// Zero/empty source account.
	if result := checkAccountPresent(common); result != ter.TesSUCCESS {
		return result
	}
	// Malformed fee — before the signing-key shape check.
	if result := e.validateFee(common); result != ter.TesSUCCESS {
		return result
	}
	// A non-empty SigningPubKey of an invalid key type.
	if result := checkSigningKeyShape(common); result != ter.TesSUCCESS {
		return result
	}
	// Sequence presence (go-xrpl defensive) and the ticket+AccountTxnID rule.
	if result := e.preflightSequence(common); result != ter.TesSUCCESS {
		return result
	}
	// The outer tfInnerBatchTxn rejection precedes the common Sponsor checks.
	if result := preflightInnerBatchFlag(common, rules); result != ter.TesSUCCESS {
		return result
	}
	if result := checkSponsorFields(tx, common, rules); result != ter.TesSUCCESS {
		return result
	}
	return ter.TesSUCCESS
}

// preflight0 mirrors rippled preflight0 for the regular (non-pseudo) path: the
// NetworkID canonicality check followed by the flags mask. The pseudo path, the
// pseudo/tfInnerBatchTxn guard, and the zero-txID guard live elsewhere
// (pseudoPreflight and ApplyWithContext respectively).
// Reference: rippled Transactor.cpp preflight0.
func (e *Engine) preflight0(tx txcore.Transaction, common *txcore.Common, rules *amendment.Rules) ter.Result {
	if result := e.validateNetworkID(common); result != ter.TesSUCCESS {
		return result
	}
	return checkFlagsMask(tx, common, rules)
}

// checkFlagsMask rejects a transaction whose flags intersect its type's
// invalid-flags mask with temINVALID_FLAG, mirroring rippled preflight0's
// `tx.getFlags() & T::getFlagsMask(ctx)`. A type opts in via FlagsMasker; a type
// that does not implement it gets no engine-level flag rejection (the universal
// mask would reject every valid type-specific flag). Reused by preflight0 and
// preflightInner so inner batch transactions get the same mask rippled applies
// via the per-inner invokePreflight.
func checkFlagsMask(tx txcore.Transaction, common *txcore.Common, rules *amendment.Rules) ter.Result {
	if fm, ok := tx.(txcore.FlagsMasker); ok {
		if common.GetFlags()&fm.GetFlagsMask(rules) != 0 {
			return ter.TemINVALID_FLAG
		}
	}
	return ter.TesSUCCESS
}

// checkExtraFeatures runs a type's ExtraFeaturesChecker (rippled
// T::checkExtraFeatures), which gates amendment-dependent tem*/temDISABLED
// rejections ahead of preflight1's common checks. Reused by the outer structure
// preflight and preflightInner.
func checkExtraFeatures(tx txcore.Transaction, rules *amendment.Rules) ter.Result {
	if efc, ok := tx.(txcore.ExtraFeaturesChecker); ok {
		if err := efc.CheckExtraFeatures(rules); err != nil {
			return parseValidationError(err)
		}
	}
	return ter.TesSUCCESS
}

// checkPreflightRules runs a type's RulesPreflighter (the amendment-gated tem*
// checks of rippled's T::preflight body) after Validate(). Reused by the outer
// structure preflight and preflightInner.
func checkPreflightRules(tx txcore.Transaction, rules *amendment.Rules) ter.Result {
	if rp, ok := tx.(txcore.RulesPreflighter); ok {
		if err := rp.PreflightRules(rules); err != nil {
			return parseValidationError(err)
		}
	}
	return ter.TesSUCCESS
}

func runTypePreflight(tx txcore.Transaction, rules *amendment.Rules) ter.Result {
	if batch, ok := tx.(txcore.BatchInnerPreflightRunner); ok {
		if err := batch.ValidateBatchOuter(); err != nil {
			return parseValidationError(err)
		}
		return checkPreflightRules(tx, rules)
	}
	if rp, ok := tx.(txcore.RulesAwarePreflighter); ok {
		if err := rp.PreflightWithRules(rules); err != nil {
			return parseValidationError(err)
		}
		return ter.TesSUCCESS
	}
	if err := tx.Validate(); err != nil {
		return parseValidationError(err)
	}
	return checkPreflightRules(tx, rules)
}

// preflightInner runs the structural checks for an inner batch tx. rippled routes
// inner txs through the SAME invokePreflight as standalone txs (Batch.cpp:338 →
// preflight(stx, tapBATCH)): the amendment gate, checkExtraFeatures, preflight0's
// flags mask, and the full T::preflight body all run — tapBATCH only makes
// preflight1/preflight2 skip the fee and signature checks. So this must run every
// per-type preflight seam (FlagsMasker, CheckExtraFeatures, RulesPreflighter,
// RulesAwarePreflighter), not just Validate(): a type that carries part of its
// preflight body in another seam would otherwise have that validation silently
// skipped for an inner tx. Any failure collapses to temINVALID_INNER_BATCH at the
// call site, so the intra-order here is immaterial.
// Fee/signature/multi-sign/inner-flag rejections stay skipped (inner txs carry
// Fee=0, no signature, no multi-signers, tfInnerBatchTxn set — all validated by
// Batch.Validate()).
func (e *Engine) preflightInner(innerTx txcore.Transaction) ter.Result {
	common := innerTx.GetCommon()
	rules := e.rules()
	if result := checkAccountPresent(common); result != ter.TesSUCCESS {
		return result
	}
	for _, featureID := range innerTx.RequiredAmendments() {
		if !rules.Enabled(featureID) {
			return ter.TemDISABLED
		}
	}
	if result := checkExtraFeatures(innerTx, rules); result != ter.TesSUCCESS {
		return result
	}
	if result := e.validateNetworkID(common); result != ter.TesSUCCESS {
		return result
	}
	if result := checkFlagsMask(innerTx, common, rules); result != ter.TesSUCCESS {
		return result
	}
	if result := checkSigningKeyShape(common); result != ter.TesSUCCESS {
		return result
	}
	usesTicket := (common.Sequence == nil || *common.Sequence == 0) && common.TicketSequence != nil
	if usesTicket && common.AccountTxnID != "" {
		return ter.TemINVALID
	}
	if result := checkDelegate(innerTx, common, rules); result != ter.TesSUCCESS {
		return result
	}
	if result := checkSponsorFields(innerTx, common, rules); result != ter.TesSUCCESS {
		return result
	}
	if result := runTypePreflight(innerTx, rules); result != ter.TesSUCCESS {
		return result
	}
	if svp, ok := innerTx.(txcore.SigValidatedPreflighter); ok {
		if err := svp.PreflightSigValidated(); err != nil {
			return parseValidationError(err)
		}
	}
	return ter.TesSUCCESS
}

// preflightInnerBatchFlag rejects a transaction reaching the outer preflight
// that carries tfInnerBatchTxn. That flag is only ever set by the Batch
// transactor on the inner transactions it applies (which flow through
// preflightInner, not here), so on the outer path it is always illegitimate. It
// follows fee and sequence validation, so a transaction that is both
// inner-flagged and malformed there surfaces the earlier code.
//
// Without BatchV1_1 the flag is undefined. With BatchV1_1 it is valid only on
// an inner transaction carrying a parent batch ID, so the standalone path is
// rejected as temINVALID_INNER_BATCH.
func preflightInnerBatchFlag(common *txcore.Common, rules *amendment.Rules) ter.Result {
	if common.Flags == nil || *common.Flags&txcore.TfInnerBatchTxn == 0 {
		return ter.TesSUCCESS
	}
	if !rules.Enabled(amendment.FeatureBatchV1_1) {
		return ter.TemINVALID_FLAG
	}
	return ter.TemINVALID_INNER_BATCH
}

// checkDelegate validates the sfDelegate field.
// Reference: rippled Transactor.cpp preflight1 (delegate block, before preflight0).
func checkDelegate(transaction txcore.Transaction, common *txcore.Common, rules *amendment.Rules) ter.Result {
	if common.Delegate == "" {
		return ter.TesSUCCESS
	}
	if !rules.Enabled(amendment.FeaturePermissionDelegationV1_1) {
		return ter.TemDISABLED
	}
	if common.Delegate == common.Account {
		return ter.TemBAD_SIGNER
	}
	if transaction.TxType() == txcore.TypeSponsorshipTransfer ||
		transaction.TxType() == txcore.TypeConfidentialMPTConvert {
		return ter.TemINVALID
	}
	return ter.TesSUCCESS
}

func checkSponsorFields(transaction txcore.Transaction, common *txcore.Common, rules *amendment.Rules) ter.Result {
	hasSponsor := common.Sponsor != "" || common.HasField("Sponsor")
	hasSponsorFlags := common.SponsorFlags != nil || common.HasField("SponsorFlags")
	hasSponsorSignature := common.SponsorSignature != nil || common.HasField("SponsorSignature")
	if !hasSponsor && !hasSponsorFlags && !hasSponsorSignature {
		return ter.TesSUCCESS
	}
	if !rules.Enabled(amendment.FeatureSponsor) {
		return ter.TemDISABLED
	}
	if hasSponsor != hasSponsorFlags {
		return ter.TemINVALID_FLAG
	}
	if hasSponsorSignature && (!hasSponsor || !hasSponsorFlags) {
		return ter.TemMALFORMED
	}
	if hasSponsorSignature && common.SponsorSignature == nil {
		return ter.TemMALFORMED
	}
	if hasSponsorFlags {
		if common.SponsorFlags == nil {
			return ter.TemINVALID_FLAG
		}
		flags := *common.SponsorFlags
		if flags == 0 || flags&txcore.SpfSponsorFlagMask != 0 {
			return ter.TemINVALID_FLAG
		}
		if flags&txcore.SpfSponsorReserve != 0 && !reserveSponsorAllowed(transaction.TxType()) {
			return ter.TemINVALID_FLAG
		}
	}
	if hasSponsor && common.Sponsor == common.Account {
		return ter.TemMALFORMED
	}
	return ter.TesSUCCESS
}

func reserveSponsorAllowed(txType txcore.Type) bool {
	switch txType {
	case txcore.TypeDelegateSet,
		txcore.TypeDepositPreauth,
		txcore.TypePayment,
		txcore.TypeSignerListSet,
		txcore.TypeCheckCancel,
		txcore.TypeCheckCash,
		txcore.TypeCheckCreate,
		txcore.TypeEscrowCancel,
		txcore.TypeEscrowCreate,
		txcore.TypeEscrowFinish,
		txcore.TypePaymentChannelClaim,
		txcore.TypePaymentChannelCreate,
		txcore.TypePaymentChannelFund,
		txcore.TypeClawback,
		txcore.TypeMPTokenAuthorize,
		txcore.TypeMPTokenIssuanceCreate,
		txcore.TypeMPTokenIssuanceDestroy,
		txcore.TypeMPTokenIssuanceSet,
		txcore.TypeTrustSet,
		txcore.TypeCredentialAccept,
		txcore.TypeCredentialCreate,
		txcore.TypeCredentialDelete,
		txcore.TypeAccountSet,
		txcore.TypeRegularKeySet,
		txcore.TypeSponsorshipTransfer:
		return true
	default:
		return false
	}
}

// checkAccountPresent rejects a zero/empty source account.
// Reference: rippled Transactor.cpp preflight1 (id == beast::zero → temBAD_SRC_ACCOUNT).
func checkAccountPresent(common *txcore.Common) ter.Result {
	if common.Account == "" {
		return ter.TemBAD_SRC_ACCOUNT
	}
	if common.TransactionType == "" {
		return ter.TemINVALID
	}
	return ter.TesSUCCESS
}

// checkSigningKeyShape rejects a non-empty SigningPubKey whose key type is
// invalid, regardless of whether crypto verification runs. rippled preflight1
// does this unconditionally (preflightCheckSigningKey → temBAD_SIGNATURE), so
// even paths that skip signature verification must still bounce a malformed key.
func checkSigningKeyShape(common *txcore.Common) ter.Result {
	if common.SigningPubKey == "" {
		return ter.TesSUCCESS
	}
	spk, decErr := hex.DecodeString(common.SigningPubKey)
	if decErr != nil || !txcore.IsValidPublicKey(spk) {
		return ter.TemBAD_SIGNATURE
	}
	return ter.TesSUCCESS
}

// preflightSequence enforces the ticket+AccountTxnID rule from rippled
// Transactor::preflight1, plus a go-xrpl-only defensive check that Sequence or
// TicketSequence is present (rippled relies on the soeREQUIRED sfSequence
// template field, which cannot be absent from a canonically-serialized tx).
func (e *Engine) preflightSequence(common *txcore.Common) ter.Result {
	if common.Sequence == nil && common.TicketSequence == nil {
		return ter.TemBAD_SEQUENCE
	}

	// An AccountTxnID constrains transaction ordering more than Sequence, while
	// Tickets relax it, so the combination is unsupported — but only when the
	// transaction actually uses a ticket. rippled gates this on
	// getSeqProxy().isTicket(), which is true iff the Sequence field is zero (or
	// absent) AND a TicketSequence is present; a tx with a non-zero Sequence
	// ignores its TicketSequence entirely, so AccountTxnID is fine.
	// Reference: rippled Transactor.cpp preflight1() + STTx::getSeqProxy.
	usesTicket := (common.Sequence == nil || *common.Sequence == 0) && common.TicketSequence != nil
	if usesTicket && common.AccountTxnID != "" {
		return ter.TemINVALID
	}

	return ter.TesSUCCESS
}

// preflightMultiSignStructure performs the structural multi-sign validation
// (bounds, sort, uniqueness, self-sign rejection). rippled runs these inside
// STTx::multiSignHelper, reached via checkValidity in preflight2 — AFTER the
// per-type preflight body — and a violation is Validity::SigBad → temINVALID
// (NOT temBAD_SIGNATURE, which the submission layer reports separately). It runs
// regardless of SkipSignatureVerification.
// Reference: rippled STTx.cpp multiSignHelper() + Transactor::preflight2.
func (e *Engine) preflightMultiSignStructure(tx txcore.Transaction, common *txcore.Common) ter.Result {
	if !sign.IsMultiSigned(tx) {
		return ter.TesSUCCESS
	}
	// The signer array must lie within the multi-signer bounds.
	if n := len(common.Signers); n < sign.MinMultiSigners || n > sign.MaxMultiSigners {
		return ter.TemINVALID
	}
	txAccountID, acctErr := state.DecodeAccountID(common.Account)
	if acctErr != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	var lastAccountID [20]byte // zero-initialized — less than any real ID
	for _, sw := range common.Signers {
		signerID, decErr := state.DecodeAccountID(sw.Signer.Account)
		if decErr != nil {
			return ter.TemINVALID
		}
		// The account owner may not multisign for themselves.
		if signerID == txAccountID {
			return ter.TemINVALID
		}
		// No duplicate signers allowed.
		if signerID == lastAccountID {
			return ter.TemINVALID
		}
		// Accounts must be in order by binary AccountID.
		if bytes.Compare(lastAccountID[:], signerID[:]) > 0 {
			return ter.TemINVALID
		}
		lastAccountID = signerID
	}
	return ter.TesSUCCESS
}

// preflightSponsorSignStructure enforces the signature-object shape even when
// cryptographic verification is disabled (simulation and the test harness).
// Nested multisigners are ordered and unique by binary AccountID, but unlike
// top-level Signers they are not compared with the transaction Account.
func (e *Engine) preflightSponsorSignStructure(common *txcore.Common) ter.Result {
	sponsor := common.SponsorSignature
	if sponsor == nil {
		return ter.TesSUCCESS
	}
	if sponsor.SigningPubKey != "" {
		if len(sponsor.Signers) != 0 {
			return ter.TemINVALID
		}
		return ter.TesSUCCESS
	}
	if len(sponsor.Signers) == 0 {
		if sponsor.TxnSignature != "" {
			return ter.TemINVALID
		}
		// An empty object is accepted only by dry-run/test configurations.
		// The crypto verifier rejects it on a normal submission.
		return ter.TesSUCCESS
	}
	if sponsor.TxnSignature != "" {
		return ter.TemINVALID
	}
	if n := len(sponsor.Signers); n < sign.MinMultiSigners || n > sign.MaxMultiSigners {
		return ter.TemINVALID
	}
	var lastAccountID [20]byte
	for _, sw := range sponsor.Signers {
		signerID, err := state.DecodeAccountID(sw.Signer.Account)
		if err != nil {
			return ter.TemINVALID
		}
		if signerID == lastAccountID || bytes.Compare(lastAccountID[:], signerID[:]) > 0 {
			return ter.TemINVALID
		}
		lastAccountID = signerID
	}
	return ter.TesSUCCESS
}

// preflightBatchSignerStructure enforces the rules-gated upper bound on each
// multi-signed BatchSigner's nested Signers array. rippled checks this inside
// multiSignHelper (called from Batch::preflight with ctx.rules); an out-of-range
// array there is a bad-signature validity result and surfaces as temINVALID. The
// crypto verification of those signers lives in Batch.Validate(), which has no
// rules access, so the rules-dependent size bound is enforced here in preflight.
func (e *Engine) preflightBatchSignerStructure(tx txcore.Transaction) ter.Result {
	bsp, ok := tx.(txcore.BatchSignerProvider)
	if !ok {
		return ter.TesSUCCESS
	}
	maxSigners := sign.MaxMultiSigners
	for _, signer := range bsp.GetBatchSigners() {
		// A single-signed BatchSigner has no nested array; multi-sign is keyed
		// off an empty SigningPubKey, matching Batch.verifyBatchSignatures.
		if signer.SigningPubKey != "" {
			continue
		}
		if n := len(signer.Signers); n < sign.MinMultiSigners || n > maxSigners {
			return ter.TemINVALID
		}
	}
	return ter.TesSUCCESS
}

// verifySignatures performs cryptographic signature verification (single or multi)
// when SkipSignatureVerification is false. Authorization checks (master/regular
// key) live in preclaim.
func (e *Engine) verifySignatures(tx txcore.Transaction) ter.Result {
	if e.config.SkipSignatureVerification {
		return ter.TesSUCCESS
	}
	if matches, err := txcore.CurrentFieldsMatchRaw(tx); err != nil || !matches {
		return ter.TemINVALID
	}
	txID, idErr := txcore.ComputeCurrentTransactionHash(tx)
	if idErr == nil && tx.GetCommon().SignatureVerified(txID) {
		return ter.TesSUCCESS
	}
	if idErr == nil && sigcache.Verified(txID) {
		return ter.TesSUCCESS
	}
	// Verify the outer single/multi-sign signature first, mirroring rippled's
	// preflight2 (checkValidity) which precedes the batch-signer check.
	if result := e.verifyOuterSignature(tx); result != ter.TesSUCCESS {
		return result
	}
	// After the top-level signature passes, verify a nested
	// sfCounterpartySignature if present, mirroring the counterparty arm of
	// rippled STTx::checkSign. A failure is a bad signature (checkValidity's
	// Validity::SigBad), which rippled maps to temINVALID.
	if cp := tx.GetCommon().CounterpartySignature; cp != nil {
		if err := sign.VerifyCounterpartySignature(tx, cp, true); err != nil {
			return ter.TemINVALID
		}
	}
	// SponsorSignature is checked after CounterpartySignature, matching
	// STTx::checkSign. Its contents are excluded from the signing projection,
	// but every signature still binds all ordinary transaction fields.
	if sponsor := tx.GetCommon().SponsorSignature; sponsor != nil {
		if err := sign.VerifySponsorSignature(tx, sponsor, true); err != nil {
			return ter.TemINVALID
		}
	}
	// Batch-signer signatures are verified over the batch signing digest, the same
	// stage rippled runs STTx::checkBatchSign (always RequireFullyCanonicalSig::yes).
	// Structural bounds run before hashing; coverage/order runs in
	// PreflightSigValidated. Only cryptographic verification is gated here so it honours
	// SkipSignatureVerification like every other signature.
	if bsv, ok := tx.(txcore.BatchSignatureVerifier); ok {
		if err := bsv.VerifyBatchSignatures(); err != nil {
			return ter.TemINVALID
		}
	}
	if idErr == nil {
		tx.GetCommon().MarkSignatureVerified(txID)
		sigcache.MarkVerified(txID)
	}
	return ter.TesSUCCESS
}

// verifyOuterSignature performs the cryptographic single/multi-sign verification
// of the transaction's own signature. Reference: rippled STTx::checkSingleSign /
// checkMultiSign via preflight2's checkValidity.
func (e *Engine) verifyOuterSignature(tx txcore.Transaction) ter.Result {
	// Full canonicality (low-S secp256k1) is unconditionally required.
	// Reference: rippled STTx::checkSingleSign/checkMultiSign (verify() defaults
	// to fullyCanonical).
	mustBeFullyCanonical := true
	if sign.IsMultiSigned(tx) {
		// Preflight verifies only the multi-sign structure (already checked in
		// preflightMultiSignStructure) and the per-signer cryptographic
		// signatures. The view-dependent signer-list authorization — quorum,
		// master/regular key, and list membership — is a preclaim check
		// (checkMultiSign), so that an under-quorum multi-signed tx whose
		// LastLedgerSequence has passed reports tefMAX_LEDGER (from preclaim's
		// checkPriorTxAndLastLedger) rather than tefBAD_QUORUM. This mirrors
		// rippled, where STTx::checkMultiSign (preflight2) is crypto-only and
		// Transactor::checkMultiSign (preclaim) does authorization. A crypto
		// failure is preflight2's Validity::SigBad → temINVALID.
		if err := sign.VerifyMultiSignatureCrypto(tx, mustBeFullyCanonical); err != nil {
			return ter.TemINVALID
		}
		return ter.TesSUCCESS
	}
	// Single-signed transaction — verify cryptographic signature validity.
	// The signing key authorization (master vs regular key) is checked in preclaim.
	// A failed crypto check is preflight2's `Validity::SigBad`, which rippled
	// maps to temINVALID (Transactor.cpp:198-201) — NOT temBAD_SIGNATURE. The
	// malformed-key-type case that does warrant temBAD_SIGNATURE is already
	// caught unconditionally in preflight1 (preflightCommon).
	//
	if err := sign.VerifySignature(tx, mustBeFullyCanonical); err != nil {
		return ter.TemINVALID
	}
	return ter.TesSUCCESS
}

// PrewarmSignature cryptographically verifies all signatures ahead of the
// open-ledger apply strand and caches a positive verdict on the transaction, so
// the in-strand signature check skips the repeat verification.
// This moves the dominant per-tx cost — ECDSA/EdDSA verification — off the
// apply mutex onto the ingress workers, where it runs concurrently, mirroring
// rippled caching SF_SIGGOOD in checkValidity before the apply strand (#1105).
//
// It returns a signature error to ingress callers and never caches a negative
// verdict. Authorization against ledger signer lists remains in preclaim.
var ErrInvalidSignature = errors.New("invalid transaction signature")

func PrewarmSignature(txn txcore.Transaction) error {
	if txn == nil {
		return nil
	}
	common := txn.GetCommon()
	if common == nil {
		return nil
	}
	txID, idErr := txcore.ComputeCurrentTransactionHash(txn)
	if idErr == nil && common.SignatureVerified(txID) {
		return nil
	}
	if reason := sign.CheckSTTxSignature(txn, nil, true); reason != "" {
		return fmt.Errorf("%w: %s", ErrInvalidSignature, reason)
	}
	if idErr != nil {
		return nil
	}
	common.MarkSignatureVerified(txID)
	// Publish to the tx-ID cache so the consensus build path (fresh object,
	// cold flag) skips the redundant whole-transaction signature verify.
	sigcache.MarkVerified(txID)
	return nil
}

// hasInvalidAmount reports whether the transaction carries a non-canonical
// native or MPT amount in any field, mirroring rippled's hasInvalidAmount scan
// of the serialized STTx. The transaction is normalised to its canonical JSON
// map (each amount rendered as a drops string or {value, ...} object) so the
// scan sees a value that go-xrpl's amount-capping binary codec would otherwise
// refuse to round-trip. A flatten/marshal failure yields false: the universal
// check never manufactures a rejection it cannot substantiate, leaving any real
// malformation to the per-type preflight.
func hasInvalidAmount(tx txcore.Transaction) bool {
	flat, err := tx.Flatten()
	if err != nil {
		return false
	}
	b, err := json.Marshal(flat)
	if err != nil {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return false
	}
	return invariants.HasInvalidAmount(m)
}

// parseValidationError maps a Validate() error to a TER result code.
// Validators that need a specific code return *ResultError via tx.Errorf;
// anything unstructured falls through to TemINVALID.
func parseValidationError(err error) ter.Result {
	var re *ter.ResultError
	if errors.As(err, &re) {
		return re.Code
	}
	return ter.TemINVALID
}

// validateNetworkID validates the NetworkID field according to rippled rules
// - Legacy networks (ID <= 1024) cannot have NetworkID in transactions
// - New networks (ID > 1024) require NetworkID and it must match
func (e *Engine) validateNetworkID(common *txcore.Common) ter.Result {
	nodeNetworkID := e.config.NetworkID
	txNetworkID := common.NetworkID

	if nodeNetworkID <= txcore.LegacyNetworkIDThreshold {
		// Legacy networks cannot specify NetworkID in transactions
		if txNetworkID != nil {
			return ter.TelNETWORK_ID_MAKES_TX_NON_CANONICAL
		}
	} else {
		// New networks require NetworkID to be present and match
		if txNetworkID == nil {
			return ter.TelREQUIRES_NETWORK_ID
		}
		if *txNetworkID != nodeNetworkID {
			return ter.TelWRONG_NETWORK
		}
	}

	return ter.TesSUCCESS
}

// validateFee validates the Fee field
func (e *Engine) validateFee(common *txcore.Common) ter.Result {
	// sfFee is a required field on every transaction (rippled TxFormats.cpp:
	// {sfFee, soeREQUIRED}); an STTx missing it fails template validation before
	// preflight ever runs. The engine must not invent a fee the signer never
	// authorized, so an absent Fee is rejected here rather than defaulted.
	if common.Fee == "" {
		return ter.TemBAD_FEE
	}

	// Parse fee as signed int first to detect negative values
	feeInt, err := strconv.ParseInt(common.Fee, 10, 64)
	if err != nil {
		return ter.TemBAD_FEE
	}

	// Fee cannot be negative
	if feeInt < 0 {
		return ter.TemBAD_FEE
	}

	fee := uint64(feeInt)

	// Fee=0 is allowed in preflight — rippled permits it here and checks the
	// minimum fee in preclaim (checkFee). SetRegularKey uses fee=0 for the
	// one-time free "password change". Other tx types that declare fee=0 will
	// be caught later by telINSUF_FEE_P in preclaim.

	// Fee cannot exceed maximum allowed fee
	maxFee := e.config.MaxFee
	if maxFee == 0 {
		maxFee = txcore.DefaultMaxFee
	}
	if fee > maxFee {
		return ter.TemBAD_FEE
	}

	return ter.TesSUCCESS
}
