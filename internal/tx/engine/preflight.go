package engine

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/sigcache"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// preflight performs initial validation on the transaction.
// Mirrors rippled Transactor::preflight() which composes preflight0/preflight1/preflight2
// and the per-tx-type preflight. The blocks below are extracted helpers so this
// top-level function reads as a high-level pipeline.
func (e *Engine) preflight(tx txcore.Transaction) ter.Result {
	common := tx.GetCommon()
	rules := e.rules()

	// Structural preflight is ledger-state-independent, so its verdict is
	// memoised on the transaction and a re-preflight under the same rules skips
	// the repeat (see Common.preflightedRules). Signature verification stays out
	// of the memo and always runs below, so a multi-signed tx's view-dependent
	// signer-list check is never cached.
	if !common.PreflightVerified(rules) {
		if result := e.preflightStructure(tx, common); result != ter.TesSUCCESS {
			return result
		}
		common.MarkPreflightVerified(rules)
	}

	// preflight2 — cryptographic signature verification runs LAST, after the
	// type-specific checks, mirroring rippled where preflight2()'s checkValidity
	// is the final step of every tx's preflight(). A transaction that is both
	// malformed and mis-signed therefore surfaces its type-specific tem* code,
	// not the signature code.
	if result := e.verifySignatures(tx); result != ter.TesSUCCESS {
		return result
	}

	// Reference: rippled Batch.cpp:303-312.
	if outer, ok := tx.(BatchOuter); ok {
		for _, inner := range outer.InnerTransactions() {
			if inner == nil {
				return ter.TemINVALID_INNER_BATCH
			}
			if r := e.preflightInner(inner); r != ter.TesSUCCESS {
				return ter.TemINVALID_INNER_BATCH
			}
		}
	}

	return ter.TesSUCCESS
}

// preflightStructure runs the ledger-state-independent preflight checks
// (preflight0/1 minus signature verification, plus the per-type Validate and
// rules-gated checks). It is a pure function of the transaction fields and the
// active rules, which is what makes its verdict safe to memoise (see
// Common.PreflightVerified).
func (e *Engine) preflightStructure(tx txcore.Transaction, common *txcore.Common) ter.Result {
	// preflight0: trivial common-field presence + amendment + flag checks.
	if result := e.preflightCommonFields(tx, common); result != ter.TesSUCCESS {
		return result
	}

	// preflight1 — fee, sequence, memos, structural multi-sign checks.
	if result := e.validateFee(common); result != ter.TesSUCCESS {
		return result
	}
	if result := e.preflightSequence(common); result != ter.TesSUCCESS {
		return result
	}
	if result := e.validateMemos(common); result != ter.TesSUCCESS {
		return result
	}
	if result := e.preflightMultiSignStructure(tx, common); result != ter.TesSUCCESS {
		return result
	}
	if result := e.preflightBatchSignerStructure(tx); result != ter.TesSUCCESS {
		return result
	}

	// tx-type-specific validation (the per-type preflight body).
	if err := tx.Validate(); err != nil {
		return parseValidationError(err)
	}

	// Rules-dependent preflight checks for tx types that need amendment-gated
	// tem* validation alongside their rules-free Validate() body.
	if rp, ok := tx.(txcore.RulesPreflighter); ok {
		if err := rp.PreflightRules(e.rules()); err != nil {
			return parseValidationError(err)
		}
	}

	return ter.TesSUCCESS
}

// BatchOuter is implemented by transaction types whose inner transactions
// each need to pass preflight as part of the outer's preflight pipeline.
// Reference: rippled Batch.cpp preflight() — `ripple::preflight(..., tapBATCH)`
// per inner STTx; any failure → temINVALID_INNER_BATCH on the outer.
type BatchOuter interface {
	InnerTransactions() []txcore.Transaction
}

// Reference: rippled preflight(stx, tapBATCH) invoked from Batch.cpp:303.
// Fee/signature/multi-sign/inner-flag rejections are skipped here because
// inner txs have Fee=0, no signature, no multi-signers, and tfInnerBatchTxn
// set; the corresponding presence checks live in Batch.Validate().
func (e *Engine) preflightInner(innerTx txcore.Transaction) ter.Result {
	if result := e.preflightCommon(innerTx, innerTx.GetCommon()); result != ter.TesSUCCESS {
		return result
	}
	if err := innerTx.Validate(); err != nil {
		return parseValidationError(err)
	}
	return ter.TesSUCCESS
}

// Reference: rippled Transactor.cpp preflight0/early preflight1, plus the
// outer-only tfInnerBatchTxn rejection on directly-submitted transactions.
func (e *Engine) preflightCommonFields(tx txcore.Transaction, common *txcore.Common) ter.Result {
	if result := e.preflightCommon(tx, common); result != ter.TesSUCCESS {
		return result
	}

	// tfInnerBatchTxn must never appear on a directly-submitted transaction.
	// Reference: rippled Transactor.cpp preflight0().
	if common.Flags != nil && *common.Flags&txcore.TfInnerBatchTxn != 0 {
		return ter.TemINVALID_FLAG
	}

	return ter.TesSUCCESS
}

// Shared between outer (preflightCommonFields) and inner (preflightInner).
// The tfInnerBatchTxn rejection lives only in the outer path because inner
// txs are required to carry that flag.
func (e *Engine) preflightCommon(tx txcore.Transaction, common *txcore.Common) ter.Result {
	if common.Account == "" {
		return ter.TemBAD_SRC_ACCOUNT
	}
	if common.TransactionType == "" {
		return ter.TemINVALID
	}

	if result := e.validateNetworkID(common); result != ter.TesSUCCESS {
		return result
	}

	for _, featureID := range tx.RequiredAmendments() {
		if !e.rules().Enabled(featureID) {
			return ter.TemDISABLED
		}
	}

	// Reject a non-empty SigningPubKey whose key type is invalid, regardless of
	// whether crypto verification runs. rippled preflight1 does this
	// unconditionally (Transactor.cpp:129-135 — `!spk.empty() &&
	// !publicKeyType(makeSlice(spk))` → temBAD_SIGNATURE), so even paths that
	// skip signature verification (the standalone RPC ingress sets
	// SkipSignatureVerification) must still bounce a malformed key here.
	if common.SigningPubKey != "" {
		spk, decErr := hex.DecodeString(common.SigningPubKey)
		if decErr != nil || !txcore.IsValidPublicKey(spk) {
			return ter.TemBAD_SIGNATURE
		}
	}

	// Reference: rippled Transactor.cpp preflight1() line 92.
	if common.TicketSequence != nil && !e.rules().Enabled(amendment.FeatureTicketBatch) {
		return ter.TemMALFORMED
	}

	// Reference: rippled Transactor.cpp preflight1() lines 101-108.
	if common.Delegate != "" {
		if !e.rules().Enabled(amendment.FeaturePermissionDelegation) {
			return ter.TemDISABLED
		}
		if common.Delegate == common.Account {
			return ter.TemBAD_SIGNER
		}
	}

	return ter.TesSUCCESS
}

// preflightSequence enforces the Sequence/TicketSequence/AccountTxnID rules
// from rippled Transactor::preflight1() lines 142-153.
func (e *Engine) preflightSequence(common *txcore.Common) ter.Result {
	// Sequence must be present (unless using tickets)
	if common.Sequence == nil && common.TicketSequence == nil {
		return ter.TemBAD_SEQUENCE
	}

	// TicketSequence + AccountTxnID is invalid
	// Reference: rippled Transactor.cpp preflight1() line 153
	if common.TicketSequence != nil && common.AccountTxnID != "" {
		return ter.TemINVALID
	}

	// SourceTag validation - if present, it's already a uint32 via JSON parsing
	// No additional validation needed as the type system ensures it's valid
	return ter.TesSUCCESS
}

// preflightMultiSignStructure performs the structural multi-sign validation
// (sort, uniqueness, self-sign rejection) that runs regardless of
// SkipSignatureVerification.
// Reference: rippled STTx.cpp multiSignHelper() lines 468-485
func (e *Engine) preflightMultiSignStructure(tx txcore.Transaction, common *txcore.Common) ter.Result {
	if !sign.IsMultiSigned(tx) {
		return ter.TesSUCCESS
	}
	// The signer array must lie within the rules-gated bounds. An out-of-range
	// array is "Invalid Signers array size" in rippled's multiSignHelper, which
	// surfaces as temBAD_SIGNATURE at the verification call site.
	if n := len(common.Signers); n < sign.MinMultiSigners || n > sign.MaxMultiSigners(e.rules()) {
		return ter.TemBAD_SIGNATURE
	}
	txAccountID, acctErr := state.DecodeAccountID(common.Account)
	if acctErr != nil {
		return ter.TemBAD_SRC_ACCOUNT
	}
	var lastAccountID [20]byte // zero-initialized — less than any real ID
	for _, sw := range common.Signers {
		signerID, decErr := state.DecodeAccountID(sw.Signer.Account)
		if decErr != nil {
			return ter.TemBAD_SIGNATURE
		}
		// The account owner may not multisign for themselves.
		if signerID == txAccountID {
			return ter.TemBAD_SIGNATURE
		}
		// No duplicate signers allowed.
		if signerID == lastAccountID {
			return ter.TemBAD_SIGNATURE
		}
		// Accounts must be in order by binary AccountID.
		if bytes.Compare(lastAccountID[:], signerID[:]) > 0 {
			return ter.TemBAD_SIGNATURE
		}
		lastAccountID = signerID
	}
	return ter.TesSUCCESS
}

// preflightBatchSignerStructure enforces the rules-gated upper bound on each
// multi-signed BatchSigner's nested Signers array. rippled checks this inside
// multiSignHelper (called from Batch::preflight with ctx.rules); an out-of-range
// array there surfaces as temBAD_SIGNATURE at the checkBatchSign call site. The
// crypto verification of those signers lives in Batch.Validate(), which has no
// rules access, so the rules-dependent size bound is enforced here in preflight.
func (e *Engine) preflightBatchSignerStructure(tx txcore.Transaction) ter.Result {
	bsp, ok := tx.(txcore.BatchSignerProvider)
	if !ok {
		return ter.TesSUCCESS
	}
	maxSigners := sign.MaxMultiSigners(e.rules())
	for _, signer := range bsp.GetBatchSigners() {
		// A single-signed BatchSigner has no nested array; multi-sign is keyed
		// off an empty SigningPubKey, matching Batch.verifyBatchSignatures.
		if signer.SigningPubKey != "" {
			continue
		}
		if n := len(signer.Signers); n < sign.MinMultiSigners || n > maxSigners {
			return ter.TemBAD_SIGNATURE
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
	// Verify the outer single/multi-sign signature first, mirroring rippled's
	// preflight2 (checkValidity) which precedes the batch-signer check.
	if result := e.verifyOuterSignature(tx); result != ter.TesSUCCESS {
		return result
	}
	// Batch-signer signatures are verified over the batch signing digest, the same
	// stage rippled runs STTx::checkBatchSign (always RequireFullyCanonicalSig::yes).
	// The structural/coverage checks on BatchSigners run unconditionally in Validate;
	// only the cryptographic verification is gated here so it honours
	// SkipSignatureVerification like every other signature.
	if bsv, ok := tx.(txcore.BatchSignatureVerifier); ok {
		if err := bsv.VerifyBatchSignatures(); err != nil {
			return ter.TemBAD_SIGNATURE
		}
	}
	return ter.TesSUCCESS
}

// verifyOuterSignature performs the cryptographic single/multi-sign verification
// of the transaction's own signature. Reference: rippled STTx::checkSingleSign /
// checkMultiSign via preflight2's checkValidity.
func (e *Engine) verifyOuterSignature(tx txcore.Transaction) ter.Result {
	// Full canonicality (low-S secp256k1) is required when RequireFullyCanonicalSig
	// is enabled, or — independent of the amendment — when the transaction opts in
	// via the tfFullyCanonicalSig flag.
	// Reference: rippled apply.cpp:78-84 + STTx::checkSingleSign/checkMultiSign.
	mustBeFullyCanonical := e.rules().RequireFullyCanonicalSigEnabled() ||
		(tx.GetCommon().GetFlags()&txcore.TfFullyCanonicalSig) != 0
	if sign.IsMultiSigned(tx) {
		// Multi-signed transactions require signer list lookup
		lookup := &sign.EngineSignerListLookup{View: e.view}
		if err := sign.VerifyMultiSignature(tx, lookup, mustBeFullyCanonical); err != nil {
			// The typed signer-verification errors carry their own Result code
			// (ErrNotMultiSigning, ErrBadQuorum, ErrBadSignature, ErrMasterDisabled,
			// and ErrInternalLookup for a storage/parse failure); honour it.
			if re, ok := ter.AsResultError(err); ok {
				return re.Code
			}
			// The malformed-signers sentinels are plain errors, all temBAD_SIGNATURE.
			if errors.Is(err, sign.ErrNoSigners) ||
				errors.Is(err, sign.ErrDuplicateSigner) ||
				errors.Is(err, sign.ErrSignersNotSorted) {
				return ter.TemBAD_SIGNATURE
			}
			// Anything else (e.g. a wrapped serialization failure) is a bad sig.
			return ter.TefBAD_SIGNATURE
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
	// A verdict cached off-strand (PrewarmSignature) means this same signature
	// was already verified under the same rules, so the verify is skipped here to
	// keep it off the open-ledger apply mutex (issue #1105). Only positive
	// verdicts are cached, so a cold cache still runs the full verify below.
	if tx.GetCommon().SignatureVerified() {
		return ter.TesSUCCESS
	}
	// tx-ID-keyed verified-good cache (rippled SF_SIGGOOD analog): the object
	// SignatureVerified flag is cold after the consensus build re-parses the
	// agreed tx set, but the tx ID survives, so a hit skips the redundant
	// re-verify. Positive-only — a miss still runs the full verify below.
	txID, idErr := txcore.ComputeTransactionHash(tx)
	if idErr == nil && sigcache.Verified(txID) {
		return ter.TesSUCCESS
	}
	if err := sign.VerifySignature(tx, mustBeFullyCanonical); err != nil {
		return ter.TemINVALID
	}
	if idErr == nil {
		sigcache.MarkVerified(txID)
	}
	return ter.TesSUCCESS
}

// PrewarmSignature cryptographically verifies a single-signed transaction's
// signature ahead of the open-ledger apply strand and caches a positive verdict
// on the transaction, so the in-strand signature check skips the repeat verify.
// This moves the dominant per-tx cost — ECDSA/EdDSA verification — off the
// apply mutex onto the ingress workers, where it runs concurrently, mirroring
// rippled caching SF_SIGGOOD in checkValidity before the apply strand (#1105).
//
// It never rejects and never caches a negative verdict: multi-signed, unsigned,
// and bad-signature transactions leave the cache cold so the in-strand preflight
// runs unchanged and reports the canonical, ordered result. Multi-signed
// transactions stay on the in-strand path because go-xrpl interleaves their
// crypto check with ledger-state signer-list authorization, which must observe
// the apply view.
//
// rules supplies the parent ledger's amendment state so the canonicality
// requirement matches the in-strand check; a nil rules honours only the per-tx
// tfFullyCanonicalSig flag.
func PrewarmSignature(txn txcore.Transaction, rules *amendment.Rules) {
	if txn == nil {
		return
	}
	common := txn.GetCommon()
	if common == nil || common.SignatureVerified() {
		return
	}
	// Only single-signed transactions are verified off-strand; an empty
	// SigningPubKey marks a multi-signed or unsigned (inner-batch) transaction.
	if common.SigningPubKey == "" {
		return
	}
	mustBeFullyCanonical := (rules != nil && rules.RequireFullyCanonicalSigEnabled()) ||
		(common.GetFlags()&txcore.TfFullyCanonicalSig) != 0
	if sign.VerifySignature(txn, mustBeFullyCanonical) == nil {
		common.MarkSignatureVerified()
		// Publish to the tx-ID cache so the consensus build path (fresh object,
		// cold flag) skips the redundant verify.
		if txID, err := txcore.ComputeTransactionHash(txn); err == nil {
			sigcache.MarkVerified(txID)
		}
	}
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

// validateMemos validates the Memos array according to rippled rules
func (e *Engine) validateMemos(common *txcore.Common) ter.Result {
	if len(common.Memos) == 0 {
		return ter.TesSUCCESS
	}

	// Calculate total serialized size of memos
	totalSize := 0

	for _, memoWrapper := range common.Memos {
		memo := memoWrapper.Memo

		// Validate MemoType if present
		if memo.MemoType != "" {
			// MemoType must be a valid hex string
			memoTypeBytes, err := hex.DecodeString(memo.MemoType)
			if err != nil {
				return ter.TemINVALID
			}
			// MemoType max size is 256 bytes (decoded)
			if len(memoTypeBytes) > txcore.MaxMemoTypeSize {
				return ter.TemINVALID
			}
			totalSize += len(memoTypeBytes)

			// MemoType characters (when decoded) must be valid URL characters per RFC 3986
			if !isValidURLBytes(memoTypeBytes) {
				return ter.TemINVALID
			}
		}

		// Validate MemoData if present
		if memo.MemoData != "" {
			// MemoData must be a valid hex string
			memoDataBytes, err := hex.DecodeString(memo.MemoData)
			if err != nil {
				return ter.TemINVALID
			}
			// MemoData max size is 1024 bytes (decoded)
			if len(memoDataBytes) > txcore.MaxMemoDataSize {
				return ter.TemINVALID
			}
			totalSize += len(memoDataBytes)
			// Note: MemoData can contain any data, no character restrictions
		}

		// Validate MemoFormat if present
		if memo.MemoFormat != "" {
			// MemoFormat must be a valid hex string
			memoFormatBytes, err := hex.DecodeString(memo.MemoFormat)
			if err != nil {
				return ter.TemINVALID
			}
			totalSize += len(memoFormatBytes)

			// MemoFormat characters (when decoded) must be valid URL characters per RFC 3986
			if !isValidURLBytes(memoFormatBytes) {
				return ter.TemINVALID
			}
		}
	}

	// Total memo size check
	if totalSize > txcore.MaxMemoSize {
		return ter.TemINVALID
	}

	return ter.TesSUCCESS
}

// isValidURLBytes checks if the bytes contain only characters allowed in URLs per RFC 3986
// Allowed: alphanumerics and -._~:/?#[]@!$&'()*+,;=%
func isValidURLBytes(data []byte) bool {
	for _, b := range data {
		if !isURLChar(b) {
			return false
		}
	}
	return true
}

// isURLChar returns true if the byte is a valid URL character per RFC 3986
func isURLChar(c byte) bool {
	// Alphanumerics
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return true
	}
	// Special characters allowed in URLs: -._~:/?#[]@!$&'()*+,;=%
	switch c {
	case '-', '.', '_', '~', ':', '/', '?', '#', '[', ']', '@', '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=', '%':
		return true
	}
	return false
}
