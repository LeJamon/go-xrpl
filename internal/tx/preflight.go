package tx

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

// preflight performs initial validation on the transaction.
// Mirrors rippled Transactor::preflight() which composes preflight0/preflight1/preflight2
// and the per-tx-type preflight. The blocks below are extracted helpers so this
// top-level function reads as a high-level pipeline.
func (e *Engine) preflight(tx Transaction) Result {
	common := tx.GetCommon()

	// preflight0: trivial common-field presence + amendment + flag checks.
	if result := e.preflightCommonFields(tx, common); result != TesSUCCESS {
		return result
	}

	// preflight1 — fee, sequence, memos, structural multi-sign + signature checks.
	if result := e.validateFee(common); result != TesSUCCESS {
		return result
	}
	if result := e.preflightSequence(common); result != TesSUCCESS {
		return result
	}
	if result := e.validateMemos(common); result != TesSUCCESS {
		return result
	}
	if result := e.preflightMultiSignStructure(tx, common); result != TesSUCCESS {
		return result
	}
	if result := e.preflightBatchSignerStructure(tx); result != TesSUCCESS {
		return result
	}

	// tx-type-specific validation (the per-type preflight body).
	if err := tx.Validate(); err != nil {
		return parseValidationError(err)
	}

	// Rules-dependent preflight checks for tx types that need amendment-gated
	// tem* validation alongside their rules-free Validate() body.
	if rp, ok := tx.(RulesPreflighter); ok {
		if err := rp.PreflightRules(e.rules()); err != nil {
			return parseValidationError(err)
		}
	}

	// preflight2 — cryptographic signature verification runs LAST, after the
	// type-specific checks, mirroring rippled where preflight2()'s checkValidity
	// is the final step of every tx's preflight(). A transaction that is both
	// malformed and mis-signed therefore surfaces its type-specific tem* code,
	// not the signature code.
	if result := e.verifySignatures(tx); result != TesSUCCESS {
		return result
	}

	// Reference: rippled Batch.cpp:303-312.
	if outer, ok := tx.(BatchOuter); ok {
		for _, inner := range outer.InnerTransactions() {
			if inner == nil {
				return TemINVALID_INNER_BATCH
			}
			if r := e.preflightInner(inner); r != TesSUCCESS {
				return TemINVALID_INNER_BATCH
			}
		}
	}

	return TesSUCCESS
}

// BatchOuter is implemented by transaction types whose inner transactions
// each need to pass preflight as part of the outer's preflight pipeline.
// Reference: rippled Batch.cpp preflight() — `ripple::preflight(..., tapBATCH)`
// per inner STTx; any failure → temINVALID_INNER_BATCH on the outer.
type BatchOuter interface {
	InnerTransactions() []Transaction
}

// Reference: rippled preflight(stx, tapBATCH) invoked from Batch.cpp:303.
// Fee/signature/multi-sign/inner-flag rejections are skipped here because
// inner txs have Fee=0, no signature, no multi-signers, and tfInnerBatchTxn
// set; the corresponding presence checks live in Batch.Validate().
func (e *Engine) preflightInner(innerTx Transaction) Result {
	if result := e.preflightCommon(innerTx, innerTx.GetCommon()); result != TesSUCCESS {
		return result
	}
	if err := innerTx.Validate(); err != nil {
		return parseValidationError(err)
	}
	return TesSUCCESS
}

// Reference: rippled Transactor.cpp preflight0/early preflight1, plus the
// outer-only tfInnerBatchTxn rejection on directly-submitted transactions.
func (e *Engine) preflightCommonFields(tx Transaction, common *Common) Result {
	if result := e.preflightCommon(tx, common); result != TesSUCCESS {
		return result
	}

	// tfInnerBatchTxn must never appear on a directly-submitted transaction.
	// Reference: rippled Transactor.cpp preflight0().
	if common.Flags != nil && *common.Flags&TfInnerBatchTxn != 0 {
		return TemINVALID_FLAG
	}

	return TesSUCCESS
}

// Shared between outer (preflightCommonFields) and inner (preflightInner).
// The tfInnerBatchTxn rejection lives only in the outer path because inner
// txs are required to carry that flag.
func (e *Engine) preflightCommon(tx Transaction, common *Common) Result {
	if common.Account == "" {
		return TemBAD_SRC_ACCOUNT
	}
	if common.TransactionType == "" {
		return TemINVALID
	}

	if result := e.validateNetworkID(common); result != TesSUCCESS {
		return result
	}

	for _, featureID := range tx.RequiredAmendments() {
		if !e.rules().Enabled(featureID) {
			return TemDISABLED
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
		if decErr != nil || !IsValidPublicKey(spk) {
			return TemBAD_SIGNATURE
		}
	}

	// Reference: rippled Transactor.cpp preflight1() line 92.
	if common.TicketSequence != nil && !e.rules().Enabled(amendment.FeatureTicketBatch) {
		return TemMALFORMED
	}

	// Reference: rippled Transactor.cpp preflight1() lines 101-108.
	if common.Delegate != "" {
		if !e.rules().Enabled(amendment.FeaturePermissionDelegation) {
			return TemDISABLED
		}
		if common.Delegate == common.Account {
			return TemBAD_SIGNER
		}
	}

	return TesSUCCESS
}

// preflightSequence enforces the Sequence/TicketSequence/AccountTxnID rules
// from rippled Transactor::preflight1() lines 142-153.
func (e *Engine) preflightSequence(common *Common) Result {
	// Sequence must be present (unless using tickets)
	if common.Sequence == nil && common.TicketSequence == nil {
		return TemBAD_SEQUENCE
	}

	// TicketSequence + AccountTxnID is invalid
	// Reference: rippled Transactor.cpp preflight1() line 153
	if common.TicketSequence != nil && common.AccountTxnID != "" {
		return TemINVALID
	}

	// SourceTag validation - if present, it's already a uint32 via JSON parsing
	// No additional validation needed as the type system ensures it's valid
	return TesSUCCESS
}

// preflightMultiSignStructure performs the structural multi-sign validation
// (sort, uniqueness, self-sign rejection) that runs regardless of
// SkipSignatureVerification.
// Reference: rippled STTx.cpp multiSignHelper() lines 468-485
func (e *Engine) preflightMultiSignStructure(tx Transaction, common *Common) Result {
	if !IsMultiSigned(tx) {
		return TesSUCCESS
	}
	// The signer array must lie within the rules-gated bounds. An out-of-range
	// array is "Invalid Signers array size" in rippled's multiSignHelper, which
	// surfaces as temBAD_SIGNATURE at the verification call site.
	if n := len(common.Signers); n < minMultiSigners || n > MaxMultiSigners(e.rules()) {
		return TemBAD_SIGNATURE
	}
	txAccountID, acctErr := state.DecodeAccountID(common.Account)
	if acctErr != nil {
		return TemBAD_SRC_ACCOUNT
	}
	var lastAccountID [20]byte // zero-initialized — less than any real ID
	for _, sw := range common.Signers {
		signerID, decErr := state.DecodeAccountID(sw.Signer.Account)
		if decErr != nil {
			return TemBAD_SIGNATURE
		}
		// The account owner may not multisign for themselves.
		if signerID == txAccountID {
			return TemBAD_SIGNATURE
		}
		// No duplicate signers allowed.
		if signerID == lastAccountID {
			return TemBAD_SIGNATURE
		}
		// Accounts must be in order by binary AccountID.
		if bytes.Compare(lastAccountID[:], signerID[:]) > 0 {
			return TemBAD_SIGNATURE
		}
		lastAccountID = signerID
	}
	return TesSUCCESS
}

// preflightBatchSignerStructure enforces the rules-gated upper bound on each
// multi-signed BatchSigner's nested Signers array. rippled checks this inside
// multiSignHelper (called from Batch::preflight with ctx.rules); an out-of-range
// array there surfaces as temBAD_SIGNATURE at the checkBatchSign call site. The
// crypto verification of those signers lives in Batch.Validate(), which has no
// rules access, so the rules-dependent size bound is enforced here in preflight.
func (e *Engine) preflightBatchSignerStructure(tx Transaction) Result {
	bsp, ok := tx.(BatchSignerProvider)
	if !ok {
		return TesSUCCESS
	}
	maxSigners := MaxMultiSigners(e.rules())
	for _, signer := range bsp.GetBatchSigners() {
		// A single-signed BatchSigner has no nested array; multi-sign is keyed
		// off an empty SigningPubKey, matching Batch.verifyBatchSignatures.
		if signer.SigningPubKey != "" {
			continue
		}
		if n := len(signer.Signers); n < minMultiSigners || n > maxSigners {
			return TemBAD_SIGNATURE
		}
	}
	return TesSUCCESS
}

// verifySignatures performs cryptographic signature verification (single or multi)
// when SkipSignatureVerification is false. Authorization checks (master/regular
// key) live in preclaim.
func (e *Engine) verifySignatures(tx Transaction) Result {
	if e.config.SkipSignatureVerification {
		return TesSUCCESS
	}
	// Verify the outer single/multi-sign signature first, mirroring rippled's
	// preflight2 (checkValidity) which precedes the batch-signer check.
	if result := e.verifyOuterSignature(tx); result != TesSUCCESS {
		return result
	}
	// Batch-signer signatures are verified over the batch signing digest, the same
	// stage rippled runs STTx::checkBatchSign (always RequireFullyCanonicalSig::yes).
	// The structural/coverage checks on BatchSigners run unconditionally in Validate;
	// only the cryptographic verification is gated here so it honours
	// SkipSignatureVerification like every other signature.
	if bsv, ok := tx.(BatchSignatureVerifier); ok {
		if err := bsv.VerifyBatchSignatures(); err != nil {
			return TemBAD_SIGNATURE
		}
	}
	return TesSUCCESS
}

// verifyOuterSignature performs the cryptographic single/multi-sign verification
// of the transaction's own signature. Reference: rippled STTx::checkSingleSign /
// checkMultiSign via preflight2's checkValidity.
func (e *Engine) verifyOuterSignature(tx Transaction) Result {
	// Full canonicality (low-S secp256k1) is required when RequireFullyCanonicalSig
	// is enabled, or — independent of the amendment — when the transaction opts in
	// via the tfFullyCanonicalSig flag.
	// Reference: rippled apply.cpp:78-84 + STTx::checkSingleSign/checkMultiSign.
	mustBeFullyCanonical := e.rules().RequireFullyCanonicalSigEnabled() ||
		(tx.GetCommon().GetFlags()&TfFullyCanonicalSig) != 0
	if IsMultiSigned(tx) {
		// Multi-signed transactions require signer list lookup
		lookup := &engineSignerListLookup{view: e.view}
		if err := VerifyMultiSignature(tx, lookup, mustBeFullyCanonical); err != nil {
			// The typed signer-verification errors carry their own Result code
			// (ErrNotMultiSigning, ErrBadQuorum, ErrBadSignature, ErrMasterDisabled,
			// and ErrInternalLookup for a storage/parse failure); honour it.
			if re, ok := AsResultError(err); ok {
				return re.Code
			}
			// The malformed-signers sentinels are plain errors, all temBAD_SIGNATURE.
			if errors.Is(err, ErrNoSigners) ||
				errors.Is(err, ErrDuplicateSigner) ||
				errors.Is(err, ErrSignersNotSorted) {
				return TemBAD_SIGNATURE
			}
			// Anything else (e.g. a wrapped serialization failure) is a bad sig.
			return TefBAD_SIGNATURE
		}
		return TesSUCCESS
	}
	// Single-signed transaction — verify cryptographic signature validity.
	// The signing key authorization (master vs regular key) is checked in preclaim.
	// A failed crypto check is preflight2's `Validity::SigBad`, which rippled
	// maps to temINVALID (Transactor.cpp:198-201) — NOT temBAD_SIGNATURE. The
	// malformed-key-type case that does warrant temBAD_SIGNATURE is already
	// caught unconditionally in preflight1 (preflightCommon).
	if err := VerifySignature(tx, mustBeFullyCanonical); err != nil {
		return TemINVALID
	}
	return TesSUCCESS
}

// parseValidationError maps a Validate() error to a TER result code.
// Validators that need a specific code return *ResultError via tx.Errorf;
// anything unstructured falls through to TemINVALID.
func parseValidationError(err error) Result {
	var re *ResultError
	if errors.As(err, &re) {
		return re.Code
	}
	return TemINVALID
}

// validateNetworkID validates the NetworkID field according to rippled rules
// - Legacy networks (ID <= 1024) cannot have NetworkID in transactions
// - New networks (ID > 1024) require NetworkID and it must match
func (e *Engine) validateNetworkID(common *Common) Result {
	nodeNetworkID := e.config.NetworkID
	txNetworkID := common.NetworkID

	if nodeNetworkID <= LegacyNetworkIDThreshold {
		// Legacy networks cannot specify NetworkID in transactions
		if txNetworkID != nil {
			return TelNETWORK_ID_MAKES_TX_NON_CANONICAL
		}
	} else {
		// New networks require NetworkID to be present and match
		if txNetworkID == nil {
			return TelREQUIRES_NETWORK_ID
		}
		if *txNetworkID != nodeNetworkID {
			return TelWRONG_NETWORK
		}
	}

	return TesSUCCESS
}

// validateFee validates the Fee field
func (e *Engine) validateFee(common *Common) Result {
	// sfFee is a required field on every transaction (rippled TxFormats.cpp:
	// {sfFee, soeREQUIRED}); an STTx missing it fails template validation before
	// preflight ever runs. The engine must not invent a fee the signer never
	// authorized, so an absent Fee is rejected here rather than defaulted.
	if common.Fee == "" {
		return TemBAD_FEE
	}

	// Parse fee as signed int first to detect negative values
	feeInt, err := strconv.ParseInt(common.Fee, 10, 64)
	if err != nil {
		return TemBAD_FEE
	}

	// Fee cannot be negative
	if feeInt < 0 {
		return TemBAD_FEE
	}

	fee := uint64(feeInt)

	// Fee=0 is allowed in preflight — rippled permits it here and checks the
	// minimum fee in preclaim (checkFee). SetRegularKey uses fee=0 for the
	// one-time free "password change". Other tx types that declare fee=0 will
	// be caught later by telINSUF_FEE_P in preclaim.

	// Fee cannot exceed maximum allowed fee
	maxFee := e.config.MaxFee
	if maxFee == 0 {
		maxFee = DefaultMaxFee
	}
	if fee > maxFee {
		return TemBAD_FEE
	}

	return TesSUCCESS
}

// validateMemos validates the Memos array according to rippled rules
func (e *Engine) validateMemos(common *Common) Result {
	if len(common.Memos) == 0 {
		return TesSUCCESS
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
				return TemINVALID
			}
			// MemoType max size is 256 bytes (decoded)
			if len(memoTypeBytes) > MaxMemoTypeSize {
				return TemINVALID
			}
			totalSize += len(memoTypeBytes)

			// MemoType characters (when decoded) must be valid URL characters per RFC 3986
			if !isValidURLBytes(memoTypeBytes) {
				return TemINVALID
			}
		}

		// Validate MemoData if present
		if memo.MemoData != "" {
			// MemoData must be a valid hex string
			memoDataBytes, err := hex.DecodeString(memo.MemoData)
			if err != nil {
				return TemINVALID
			}
			// MemoData max size is 1024 bytes (decoded)
			if len(memoDataBytes) > MaxMemoDataSize {
				return TemINVALID
			}
			totalSize += len(memoDataBytes)
			// Note: MemoData can contain any data, no character restrictions
		}

		// Validate MemoFormat if present
		if memo.MemoFormat != "" {
			// MemoFormat must be a valid hex string
			memoFormatBytes, err := hex.DecodeString(memo.MemoFormat)
			if err != nil {
				return TemINVALID
			}
			totalSize += len(memoFormatBytes)

			// MemoFormat characters (when decoded) must be valid URL characters per RFC 3986
			if !isValidURLBytes(memoFormatBytes) {
				return TemINVALID
			}
		}
	}

	// Total memo size check
	if totalSize > MaxMemoSize {
		return TemINVALID
	}

	return TesSUCCESS
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
