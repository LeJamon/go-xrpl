package tx

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
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
	if result := e.verifySignatures(tx); result != TesSUCCESS {
		return result
	}

	// preflight2 — tx-type-specific validation
	if err := tx.Validate(); err != nil {
		// Try to extract a specific TER code from the error message
		// Many Validate() implementations include the TER code as a prefix (e.g., "temREDUNDANT: message")
		return parseValidationError(err)
	}

	return TesSUCCESS
}

// preflightCommonFields handles the trivial common-field, amendment, and flag
// checks that rippled performs in preflight0/early preflight1.
func (e *Engine) preflightCommonFields(tx Transaction, common *Common) Result {
	// Account is required
	if common.Account == "" {
		return TemBAD_SRC_ACCOUNT
	}

	// TransactionType is required
	if common.TransactionType == "" {
		return TemINVALID
	}

	// NetworkID validation (matching rippled's preflight0)
	if result := e.validateNetworkID(common); result != TesSUCCESS {
		return result
	}

	// Amendment check - verify all required amendments are enabled
	// Reference: rippled checks this in each transaction's preflight() method
	for _, featureID := range tx.RequiredAmendments() {
		if !e.rules().Enabled(featureID) {
			return TemDISABLED
		}
	}

	// TicketSequence with disabled TicketBatch feature → temMALFORMED
	// Reference: rippled Transactor.cpp preflight1() line 92
	if common.TicketSequence != nil && !e.rules().Enabled(amendment.FeatureTicketBatch) {
		return TemMALFORMED
	}

	// Delegate field validation
	// Reference: rippled Transactor.cpp preflight1() lines 101-108
	if common.Delegate != "" {
		if !e.rules().Enabled(amendment.FeaturePermissionDelegation) {
			return TemDISABLED
		}
		if common.Delegate == common.Account {
			return TemBAD_SIGNER
		}
	}

	// tfInnerBatchTxn flag validation
	// Reference: rippled Transactor.cpp preflight0() - tfInnerBatchTxn can only be set
	// when processing inner batch transactions, never on directly submitted transactions.
	if common.Flags != nil && *common.Flags&TfInnerBatchTxn != 0 {
		return TemINVALID_FLAG
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

// verifySignatures performs cryptographic signature verification (single or multi)
// when SkipSignatureVerification is false. Authorization checks (master/regular
// key) live in preclaim.
func (e *Engine) verifySignatures(tx Transaction) Result {
	if e.config.SkipSignatureVerification {
		return TesSUCCESS
	}
	if IsMultiSigned(tx) {
		// Multi-signed transactions require signer list lookup
		lookup := &engineSignerListLookup{view: e.view}
		if err := VerifyMultiSignature(tx, lookup); err != nil {
			switch err {
			case ErrNotMultiSigning:
				return TefNOT_MULTI_SIGNING
			case ErrBadQuorum:
				return TefBAD_QUORUM
			case ErrBadSignature:
				return TefBAD_SIGNATURE
			case ErrMasterDisabled:
				return TefMASTER_DISABLED
			case ErrNoSigners:
				return TemBAD_SIGNATURE
			case ErrDuplicateSigner:
				return TemBAD_SIGNATURE
			case ErrSignersNotSorted:
				return TemBAD_SIGNATURE
			default:
				return TefBAD_SIGNATURE
			}
		}
		return TesSUCCESS
	}
	// Single-signed transaction — verify cryptographic signature validity.
	// The signing key authorization (master vs regular key) is checked in preclaim.
	if err := VerifySignature(tx); err != nil {
		return TemBAD_SIGNATURE
	}
	return TesSUCCESS
}

// parseValidationError extracts a TER result code from a validation error message.
// If the error message starts with a valid TER code prefix (e.g., "temREDUNDANT:"),
// it returns the corresponding Result. Otherwise, it returns TemINVALID.
func parseValidationError(err error) Result {
	// Fast path: structured ResultError carries the code directly
	var re *ResultError
	if errors.As(err, &re) {
		return re.Code
	}

	// Legacy fallback: string-prefix matching for unmigrated callers
	msg := err.Error()

	// Check for known TER code prefixes
	// Common tem (malformed) codes
	terCodes := map[string]Result{
		"temMALFORMED":                TemMALFORMED,
		"temBAD_AMOUNT":               TemBAD_AMOUNT,
		"temBAD_CURRENCY":             TemBAD_CURRENCY,
		"temBAD_EXPIRATION":           TemBAD_EXPIRATION,
		"temBAD_FEE":                  TemBAD_FEE,
		"temBAD_ISSUER":               TemBAD_ISSUER,
		"temBAD_LIMIT":                TemBAD_LIMIT,
		"temBAD_OFFER":                TemBAD_OFFER,
		"temBAD_PATH":                 TemBAD_PATH,
		"temBAD_PATH_LOOP":            TemBAD_PATH_LOOP,
		"temBAD_REGKEY":               TemBAD_REGKEY,
		"temBAD_SEQUENCE":             TemBAD_SEQUENCE,
		"temBAD_SIGNATURE":            TemBAD_SIGNATURE,
		"temBAD_SRC_ACCOUNT":          TemBAD_SRC_ACCOUNT,
		"temBAD_TRANSFER_RATE":        TemBAD_TRANSFER_RATE,
		"temDST_IS_SRC":               TemDST_IS_SRC,
		"temDST_NEEDED":               TemDST_NEEDED,
		"temINVALID":                  TemINVALID,
		"temINVALID_FLAG":             TemINVALID_FLAG,
		"temREDUNDANT":                TemREDUNDANT,
		"temRIPPLE_EMPTY":             TemRIPPLE_EMPTY,
		"temDISABLED":                 TemDISABLED,
		"temBAD_SIGNER":               TemBAD_SIGNER,
		"temBAD_QUORUM":               TemBAD_QUORUM,
		"temBAD_WEIGHT":               TemBAD_WEIGHT,
		"temBAD_TICK_SIZE":            TemBAD_TICK_SIZE,
		"temINVALID_ACCOUNT_ID":       TemINVALID_ACCOUNT_ID,
		"temUNCERTAIN":                TemUNCERTAIN,
		"temUNKNOWN":                  TemUNKNOWN,
		"temSEQ_AND_TICKET":           TemSEQ_AND_TICKET,
		"temBAD_SEND_XRP_MAX":         TemBAD_SEND_XRP_MAX,
		"temBAD_SEND_XRP_PARTIAL":     TemBAD_SEND_XRP_PARTIAL,
		"temBAD_SEND_XRP_PATHS":       TemBAD_SEND_XRP_PATHS,
		"temBAD_SEND_XRP_LIMIT":       TemBAD_SEND_XRP_LIMIT,
		"temBAD_SEND_XRP_NO_DIRECT":   TemBAD_SEND_XRP_NO_DIRECT,
		"temCANNOT_PREAUTH_SELF":      TemCAN_NOT_PREAUTH_SELF,
		"temCAN_NOT_PREAUTH_SELF":     TemCAN_NOT_PREAUTH_SELF,
		"temEMPTY_DID":                TemEMPTY_DID,
		"temARRAY_EMPTY":              TemARRAY_EMPTY,
		"temARRAY_TOO_LARGE":          TemARRAY_TOO_LARGE,
		"temBAD_AMM_TOKENS":           TemBAD_AMM_TOKENS,
		"temBAD_TRANSFER_FEE":         TemBAD_TRANSFER_FEE,
		"temBAD_NFTOKEN_TRANSFER_FEE": TemBAD_NFTOKEN_TRANSFER_FEE,
		"temINVALID_COUNT":            TemINVALID_COUNT,
		// tef (failure) codes
		"tefINVALID_LEDGER_FIX_TYPE": TefINVALID_LEDGER_FIX_TYPE,
		// tel (local) codes
		"telBAD_DOMAIN":     TelBAD_DOMAIN,
		"telBAD_PUBLIC_KEY": TelBAD_PUBLIC_KEY,
	}

	// Check if the message starts with any known TER code
	for code, result := range terCodes {
		if len(msg) >= len(code) && msg[:len(code)] == code {
			// Check that it's followed by a colon, space, or is the entire message
			if len(msg) == len(code) || msg[len(code)] == ':' || msg[len(code)] == ' ' {
				return result
			}
		}
	}

	// Default to temINVALID
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
	if common.Fee == "" {
		return TesSUCCESS // Fee will be checked later if needed
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
