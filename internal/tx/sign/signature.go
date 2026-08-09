package sign

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	txcore "github.com/LeJamon/go-xrpl/internal/tx"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// Bounds on the size of a multi-signer array, mirroring rippled
// STTx::minMultiSigners / STTx::kMaxMultiSigners. It governs both the regular
// Signers array and a Batch signer's nested Signers array.
const (
	MinMultiSigners = 1
	MaxMultiSigners = 32
)

// Signature verification errors
var (
	ErrMissingSignature  = errors.New("transaction is not signed")
	ErrMissingPublicKey  = errors.New("signing public key is missing")
	ErrInvalidSignature  = errors.New("signature is invalid")
	ErrPublicKeyMismatch = errors.New("public key does not match account")
	ErrUnknownKeyType    = errors.New("unknown public key type")
)

// Multi-signature specific errors (matching rippled error codes)
var (
	// ErrNotMultiSigning is returned when the account has no signer list (tefNOT_MULTI_SIGNING)
	ErrNotMultiSigning = ter.Errorf(ter.TefNOT_MULTI_SIGNING, "account is not configured for multi-signing")

	// ErrBadQuorum is returned when signers fail to meet the quorum (tefBAD_QUORUM)
	ErrBadQuorum = ter.Errorf(ter.TefBAD_QUORUM, "signers failed to meet quorum")

	// ErrBadSignature is returned when a multi-sig signature is invalid (tefBAD_SIGNATURE)
	ErrBadSignature = ter.Errorf(ter.TefBAD_SIGNATURE, "invalid signer or signature")

	// ErrMasterDisabled is returned when trying to sign with a disabled master key (tefMASTER_DISABLED)
	ErrMasterDisabled = ter.Errorf(ter.TefMASTER_DISABLED, "master key is disabled for this signer")

	// ErrNoSigners is returned when Signers array is empty
	ErrNoSigners = errors.New("multi-signed transaction has no signers")

	// ErrDuplicateSigner is returned when duplicate signers are found
	ErrDuplicateSigner = errors.New("duplicate signer in transaction")

	// ErrSignersNotSorted is returned when signers are not sorted by account
	ErrSignersNotSorted = errors.New("signers must be sorted by account")
)

// SignerListLookup is the interface for looking up an account's signer list and
// the account state needed to authorize its signers. The engine provides
// engineSignerListLookup (signer_lookup.go) backed by its ledger view; tests
// supply their own stub.
type SignerListLookup interface {
	// GetSignerList returns the signer list for an account
	// Returns nil, nil if the account has no signer list
	// Returns nil, error if there was an error looking up the signer list
	GetSignerList(account string) (*state.SignerListInfo, error)

	// GetAccountInfo returns account information needed for signer validation:
	// the account's flags (to check if the master key is disabled) and regular key.
	// Returns ErrAccountNotFound (errors.Is) when the account is genuinely absent
	// from the ledger. Any other error is a real storage/parse failure and must be
	// treated as an internal error by callers, not as a missing account.
	GetAccountInfo(account string) (flags uint32, regularKey string, err error)
}

// ErrInternalLookup wraps a storage/parse failure encountered during signer
// authorization so that VerifyMultiSignature can map it to tefINTERNAL. It is
// distinct from ErrBadSignature (an unauthorized signer) and from the
// not-found case, which is the legitimate phantom-account branch.
var ErrInternalLookup = ter.Errorf(ter.TefINTERNAL, "internal error during signer lookup")

// IsMultiSigned returns true if the transaction is multi-signed
// A transaction is multi-signed if it has a Signers array and an empty SigningPubKey
func IsMultiSigned(tx txcore.Transaction) bool {
	common := tx.GetCommon()
	return len(common.Signers) > 0 && common.SigningPubKey == ""
}

// CheckSTTxSignature mirrors the signature portion of rippled's checkValidity.
// It returns rippled's failure reason, or an empty string when the outer and
// optional counterparty signatures are valid.
func CheckSTTxSignature(transaction txcore.Transaction, rules *amendment.Rules, checkSigs bool) string {
	common := transaction.GetCommon()
	if !checkSigs {
		return ""
	}

	var reason string
	if common.SigningPubKey == "" {
		reason = checkSTTxMultiSign(transaction)
	} else {
		if common.HasField("Signers") {
			return "Cannot both single- and multi-sign."
		}
		if err := VerifySignature(transaction, true); err != nil {
			return "Invalid signature."
		}
	}
	if reason != "" {
		return reason
	}

	if common.CounterpartySignature != nil {
		if reason := checkSTTxCounterpartySignature(transaction, common.CounterpartySignature); reason != "" {
			return reason
		}
	}
	return ""
}

func checkSTTxCounterpartySignature(transaction txcore.Transaction, cp *txcore.CounterpartySignature) string {
	hasSigners := len(cp.Signers) > 0
	hasTxnSignature := cp.TxnSignature != ""
	if raw := transaction.GetRawBytes(); len(raw) > 0 {
		if fields, err := binarycodec.DecodeBytes(raw); err == nil {
			if object, ok := fields["CounterpartySignature"].(map[string]any); ok {
				_, hasSigners = object["Signers"]
				_, hasTxnSignature = object["TxnSignature"]
			}
		}
	}

	if cp.SigningPubKey != "" {
		if hasSigners {
			return counterpartyPrefix + "Cannot both single- and multi-sign."
		}
		if err := VerifyCounterpartySignature(transaction, cp, true); err != nil {
			return err.Error()
		}
		return ""
	}
	if !hasSigners {
		return counterpartyPrefix + "Empty SigningPubKey."
	}
	if hasTxnSignature {
		return counterpartyPrefix + "Cannot both single- and multi-sign."
	}
	if len(cp.Signers) < MinMultiSigners || len(cp.Signers) > MaxMultiSigners {
		return counterpartyPrefix + "Invalid Signers array size."
	}
	if err := VerifyCounterpartySignature(transaction, cp, true); err != nil {
		return err.Error()
	}
	return ""
}

func checkSTTxMultiSign(transaction txcore.Transaction) string {
	common := transaction.GetCommon()
	if !common.HasField("Signers") {
		return "Empty SigningPubKey."
	}
	if common.HasField("TxnSignature") {
		return "Cannot both single- and multi-sign."
	}
	if len(common.Signers) < MinMultiSigners || len(common.Signers) > MaxMultiSigners {
		return "Invalid Signers array size."
	}

	feePayer := common.Account
	if common.Delegate != "" {
		feePayer = common.Delegate
	}
	feePayerID, err := state.DecodeAccountID(feePayer)
	if err != nil {
		return "Invalid multisigner."
	}
	txMap, err := flattenForSigning(transaction)
	if err != nil {
		return "Internal signature check failure."
	}

	var previous [20]byte
	for _, wrapper := range common.Signers {
		signer := wrapper.Signer
		signerID, err := state.DecodeAccountID(signer.Account)
		if err != nil {
			return "Invalid multisigner."
		}
		if signerID == feePayerID {
			return "Invalid multisigner."
		}
		if signerID == previous {
			return "Duplicate Signers not allowed."
		}
		if bytes.Compare(previous[:], signerID[:]) > 0 {
			return "Unsorted Signers array."
		}
		previous = signerID

		payload, err := binarycodec.EncodeForMultisigning(copyMap(txMap), signer.Account)
		if err != nil || !verifySignatureForKey(payload, signer.SigningPubKey, signer.TxnSignature, true) {
			return "Invalid signature on account " + signer.Account + "."
		}
	}
	return ""
}

// VerifySignature verifies that a transaction is properly signed.
// Returns nil if the signature is valid, or an error describing the problem.
// For multi-signed transactions, use VerifyMultiSignature instead.
//
// mustBeFullyCanonical requires secp256k1 signatures to be low-S. The caller
// derives it from the RequireFullyCanonicalSig amendment and the per-tx
// tfFullyCanonicalSig flag (rippled apply.cpp:78-84 + STTx::checkSingleSign).
func VerifySignature(tx txcore.Transaction, mustBeFullyCanonical bool) error {
	common := tx.GetCommon()

	// Check if this is a multi-signed transaction
	if IsMultiSigned(tx) {
		// Multi-signed transactions cannot be verified without a signer list lookup
		// Use VerifyMultiSignature with a SignerListLookup instead
		return errors.New("multi-signed transaction: use VerifyMultiSignature with a SignerListLookup")
	}

	// Check that we have a signature
	if common.TxnSignature == "" {
		return ErrMissingSignature
	}

	// Check that we have a public key
	if common.SigningPubKey == "" {
		return ErrMissingPublicKey
	}

	// Note: We do NOT check whether the public key matches the account here.
	// That check (master key vs regular key) is done in preclaim where the
	// ledger state is available. This matches rippled's preflight1 which only
	// verifies the cryptographic signature validity.

	// Get the message that was signed
	signingPayload, err := getSigningPayload(tx)
	if err != nil {
		return fmt.Errorf("failed to get signing payload: %w", err)
	}

	// Verify the signature based on the key type
	valid := verifySignatureForKey(signingPayload, common.SigningPubKey, common.TxnSignature, mustBeFullyCanonical)
	if !valid {
		return ErrInvalidSignature
	}

	return nil
}

// VerifyMultiSignature verifies a multi-signed transaction
// It performs the following checks (matching rippled's checkMultiSign):
//  1. Looks up the account's SignerList
//  2. Verifies each signer is in the SignerList
//  3. Verifies each signature is valid for that signer
//  4. Sums the weights and checks against the quorum
//
// Returns nil if all signatures are valid and the quorum is met.
//
// mustBeFullyCanonical requires each secp256k1 signer signature to be low-S
// (see VerifySignature).
func VerifyMultiSignature(tx txcore.Transaction, lookup SignerListLookup, mustBeFullyCanonical bool) error {
	common := tx.GetCommon()

	// Verify this is actually a multi-signed transaction
	if !IsMultiSigned(tx) {
		if common.TxnSignature != "" {
			// This is a single-signed transaction, use VerifySignature
			return VerifySignature(tx, mustBeFullyCanonical)
		}
		return ErrMissingSignature
	}

	// Check that we have signers
	if len(common.Signers) == 0 {
		return ErrNoSigners
	}

	// Get the signer list for the idAccount.
	// For delegated transactions, use the delegate's signer list.
	// Reference: rippled Transactor::checkSign line 602 + 608-609
	idAccount := common.Account
	if common.Delegate != "" {
		idAccount = common.Delegate
	}
	signerList, err := lookup.GetSignerList(idAccount)
	if err != nil {
		return fmt.Errorf("failed to get signer list: %w", err)
	}
	if signerList == nil {
		return ErrNotMultiSigning
	}

	// Build a map of authorized signers for quick lookup
	authorizedSigners := make(map[string]state.AccountSignerEntry)
	for _, entry := range signerList.SignerEntries {
		authorizedSigners[entry.Account] = entry
	}

	// Sort the authorized signers by binary AccountID for the matching algorithm.
	// Reference: rippled stores signer lists sorted by binary AccountID.
	sortedAuthSigners := make([]state.AccountSignerEntry, len(signerList.SignerEntries))
	copy(sortedAuthSigners, signerList.SignerEntries)
	sort.Slice(sortedAuthSigners, func(i, j int) bool {
		idI, _ := state.DecodeAccountID(sortedAuthSigners[i].Account)
		idJ, _ := state.DecodeAccountID(sortedAuthSigners[j].Account)
		return bytes.Compare(idI[:], idJ[:]) < 0
	})

	// Get the multi-signing payload for this transaction
	// Each signer signs a different message (transaction + their account ID suffix)
	txMap, err := flattenForSigning(tx)
	if err != nil {
		return fmt.Errorf("failed to flatten transaction: %w", err)
	}

	// Verify signers are sorted by binary AccountID (required by XRPL).
	// Reference: rippled STTx.cpp multiSignHelper() lines 468-485
	var lastAccountID [20]byte
	seenAccounts := make(map[string]bool)

	var weightSum uint32
	authIter := 0

	for _, signerWrapper := range common.Signers {
		signer := signerWrapper.Signer
		txSignerAccount := signer.Account

		// Check for duplicate signers
		if seenAccounts[txSignerAccount] {
			return ErrDuplicateSigner
		}
		seenAccounts[txSignerAccount] = true

		// Check signers are sorted by binary AccountID
		signerID, decErr := state.DecodeAccountID(txSignerAccount)
		if decErr != nil {
			return ErrBadSignature
		}
		if bytes.Compare(lastAccountID[:], signerID[:]) > 0 {
			return ErrSignersNotSorted
		}
		if signerID == lastAccountID {
			return ErrDuplicateSigner
		}
		lastAccountID = signerID

		// Match the signer to an authorized signer (both lists are sorted by binary AccountID)
		for authIter < len(sortedAuthSigners) {
			authID, _ := state.DecodeAccountID(sortedAuthSigners[authIter].Account)
			if bytes.Compare(authID[:], signerID[:]) >= 0 {
				break
			}
			authIter++
		}

		if authIter >= len(sortedAuthSigners) || sortedAuthSigners[authIter].Account != txSignerAccount {
			// Signer is not in the authorized signer list
			return ErrBadSignature
		}

		authEntry := sortedAuthSigners[authIter]

		// Verify the signer's public key type is valid
		if signer.SigningPubKey == "" {
			return ErrBadSignature
		}
		pubKeyBytes, err := hex.DecodeString(signer.SigningPubKey)
		if err != nil || len(pubKeyBytes) == 0 {
			return ErrBadSignature
		}

		// Compute the account ID from the signer's public key
		signingAcctIDFromPubKey, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(signer.SigningPubKey)
		if err != nil {
			return ErrBadSignature
		}

		// Resolve the signer account's ledger state, distinguishing a genuinely
		// absent account (phantom) from a real storage/parse failure. The shared
		// authorization decision then renders the phantom/master/regular-key
		// verdict (rippled Transactor::checkMultiSign).
		flags, regularKey, lookupErr := lookup.GetAccountInfo(txSignerAccount)
		var acct SignerAccountState
		switch {
		case lookupErr == nil:
			acct = SignerAccountState{found: true, flags: flags, regularKey: regularKey}
		case errors.Is(lookupErr, ErrAccountNotFound):
			// Account absent — phantom branch (found stays false).
		default:
			// Real storage/parse failure — never silently allow the signer.
			return ErrInternalLookup
		}
		switch AuthorizeMultiSigner(txSignerAccount, signingAcctIDFromPubKey, acct) {
		case ter.TesSUCCESS:
			// Authorized — continue to crypto verification.
		case ter.TefMASTER_DISABLED:
			return ErrMasterDisabled
		default:
			return ErrBadSignature
		}

		// Get the multi-signing payload for this specific signer
		signingPayload, err := binarycodec.EncodeForMultisigning(copyMap(txMap), txSignerAccount)
		if err != nil {
			return fmt.Errorf("failed to encode for multi-signing: %w", err)
		}

		// Verify the signature
		valid := verifySignatureForKey(signingPayload, signer.SigningPubKey, signer.TxnSignature, mustBeFullyCanonical)
		if !valid {
			return ErrBadSignature
		}

		// Add this signer's weight
		weightSum += uint32(authEntry.SignerWeight)
	}

	// Check if quorum is met
	if weightSum < signerList.SignerQuorum {
		return ErrBadQuorum
	}

	return nil
}

// VerifyMultiSignatureCrypto verifies only the per-signer cryptographic
// signatures of a multi-signed transaction. Unlike VerifyMultiSignature it does
// NOT consult the ledger signer list, quorum, or per-signer key authorization —
// those are view-dependent preclaim checks (rippled Transactor::checkMultiSign).
// It mirrors rippled STTx::checkMultiSign, which is reached from preflight2 and
// is crypto-only. The structural rules (bounds, sorted, unique, no self-sign)
// are enforced separately before this runs, so any error here is preflight2's
// Validity::SigBad, which the caller maps to temINVALID.
func VerifyMultiSignatureCrypto(tx txcore.Transaction, mustBeFullyCanonical bool) error {
	common := tx.GetCommon()

	if !IsMultiSigned(tx) {
		if common.TxnSignature != "" {
			return VerifySignature(tx, mustBeFullyCanonical)
		}
		return ErrMissingSignature
	}
	if len(common.Signers) == 0 {
		return ErrNoSigners
	}

	txMap, err := flattenForSigning(tx)
	if err != nil {
		return fmt.Errorf("failed to flatten transaction: %w", err)
	}

	for _, signerWrapper := range common.Signers {
		signer := signerWrapper.Signer
		if signer.SigningPubKey == "" {
			return ErrBadSignature
		}
		pubKeyBytes, decErr := hex.DecodeString(signer.SigningPubKey)
		if decErr != nil || len(pubKeyBytes) == 0 {
			return ErrBadSignature
		}
		signingPayload, encErr := binarycodec.EncodeForMultisigning(copyMap(txMap), signer.Account)
		if encErr != nil {
			return fmt.Errorf("failed to encode for multi-signing: %w", encErr)
		}
		if !verifySignatureForKey(signingPayload, signer.SigningPubKey, signer.TxnSignature, mustBeFullyCanonical) {
			return ErrBadSignature
		}
	}

	return nil
}

// Nested-signature prefixes match the labels surfaced by rippled's
// STTx::checkSign for the two optional signature objects.
const (
	counterpartyPrefix = "Counterparty: "
	sponsorPrefix      = "Sponsor: "
)

// VerifyCounterpartySignature cryptographically verifies the nested
// sfCounterpartySignature object, mirroring rippled STTx::checkSign(sigObject).
// It runs after the top-level signature passes and is crypto-only: no
// signer-list or quorum authorization applies, because there is no counterparty
// account on the ledger to authorize against — this is wire-format groundwork
// for future multi-role transactions.
//
// A single-signed object (non-empty SigningPubKey) is verified against the same
// signing payload the top-level single signer covers; a multi-signed object
// (empty SigningPubKey + Signers) has each nested signer verified against the
// multi-signing payload. Unlike a top-level multi-sign, the counterparty may
// sign for the transaction's own Account. Every error is prefixed
// "Counterparty: " to match rippled's messages.
func VerifyCounterpartySignature(tx txcore.Transaction, cp *txcore.CounterpartySignature, mustBeFullyCanonical bool) error {
	return verifyNestedSignature(
		tx,
		cp.SigningPubKey,
		cp.TxnSignature,
		cp.Signers,
		counterpartyPrefix,
		mustBeFullyCanonical,
	)
}

// VerifySponsorSignature cryptographically verifies sfSponsorSignature. The
// object signs the same canonical STX/SMT projection as the transaction's
// ordinary signer; SponsorSignature itself is a non-signing field, so attaching
// or replacing it cannot change that projection. Ledger authorization of the
// sponsor's master/regular key or SignerList is performed later in preclaim.
func VerifySponsorSignature(tx txcore.Transaction, sponsor *txcore.SponsorSignature, mustBeFullyCanonical bool) error {
	return verifyNestedSignature(
		tx,
		sponsor.SigningPubKey,
		sponsor.TxnSignature,
		sponsor.Signers,
		sponsorPrefix,
		mustBeFullyCanonical,
	)
}

func verifyNestedSignature(
	tx txcore.Transaction,
	signingPubKey string,
	txnSignature string,
	signers []txcore.SignerWrapper,
	prefix string,
	mustBeFullyCanonical bool,
) error {
	if signingPubKey != "" {
		return verifyNestedSingleSign(
			tx,
			signingPubKey,
			txnSignature,
			signers,
			prefix,
			mustBeFullyCanonical,
		)
	}
	return verifyNestedMultiSign(tx, txnSignature, signers, prefix, mustBeFullyCanonical)
}

// verifyNestedSingleSign verifies a single-signed nested object. A Signers
// array alongside a SigningPubKey means the object is signed two ways.
// Reference: rippled STTx::singleSignHelper.
func verifyNestedSingleSign(
	tx txcore.Transaction,
	signingPubKey string,
	txnSignature string,
	signers []txcore.SignerWrapper,
	prefix string,
	mustBeFullyCanonical bool,
) error {
	if len(signers) > 0 {
		return errors.New(prefix + "Cannot both single- and multi-sign.")
	}
	payload, err := getSigningPayload(tx)
	if err != nil {
		return errors.New(prefix + "Invalid signature.")
	}
	if !verifySignatureForKey(payload, signingPubKey, txnSignature, mustBeFullyCanonical) {
		return errors.New(prefix + "Invalid signature.")
	}
	return nil
}

// verifyNestedMultiSign verifies a multi-signed nested object. The transaction
// account is deliberately not supplied to this helper, so a nested signer may
// equal the transaction Account. Reference: rippled multiSignHelper with an
// unseated txnAccountID for CounterpartySignature and SponsorSignature.
func verifyNestedMultiSign(
	tx txcore.Transaction,
	txnSignature string,
	signers []txcore.SignerWrapper,
	prefix string,
	mustBeFullyCanonical bool,
) error {
	// An empty SigningPubKey with no Signers is neither a single- nor a
	// multi-signature (rippled multiSignHelper's !isFieldPresent(sfSigners) arm).
	if len(signers) == 0 {
		return errors.New(prefix + "Empty SigningPubKey.")
	}
	if txnSignature != "" {
		return errors.New(prefix + "Cannot both single- and multi-sign.")
	}
	if len(signers) > MaxMultiSigners {
		return errors.New(prefix + "Invalid Signers array size.")
	}

	txMap, err := flattenForSigning(tx)
	if err != nil {
		return errors.New(prefix + "Invalid signature.")
	}

	var lastAccountID [20]byte
	for _, sw := range signers {
		signer := sw.Signer
		accountID, decErr := state.DecodeAccountID(signer.Account)
		if decErr != nil {
			return fmt.Errorf("%sInvalid signature on account %s.", prefix, signer.Account)
		}
		if lastAccountID == accountID {
			return errors.New(prefix + "Duplicate Signers not allowed.")
		}
		if bytes.Compare(lastAccountID[:], accountID[:]) > 0 {
			return errors.New(prefix + "Unsorted Signers array.")
		}
		lastAccountID = accountID

		payload, encErr := binarycodec.EncodeForMultisigningTarget(
			copyMap(txMap), signer.Account)
		if encErr != nil {
			return fmt.Errorf("%sInvalid signature on account %s.", prefix, signer.Account)
		}
		if !verifySignatureForKey(payload, signer.SigningPubKey, signer.TxnSignature, mustBeFullyCanonical) {
			return fmt.Errorf("%sInvalid signature on account %s.", prefix, signer.Account)
		}
	}
	return nil
}

// SignCounterparty produces a single-signed CounterpartySignature over the
// transaction's signing payload — the same payload the top-level signer covers
// (rippled STTx::sign with a signatureTarget writes into the nested object).
// The transaction must already carry the top-level SigningPubKey the primary
// signer used, since that key is part of the signed data. pubKeyHex is the
// counterparty's public key, placed in the returned object.
func SignCounterparty(tx txcore.Transaction, pubKeyHex, privateKeyHex string) (*txcore.CounterpartySignature, error) {
	sig, err := SignTransaction(tx, privateKeyHex)
	if err != nil {
		return nil, err
	}
	return &txcore.CounterpartySignature{SigningPubKey: pubKeyHex, TxnSignature: sig}, nil
}

// SignSponsor produces a single-signed SponsorSignature over the transaction's
// canonical signing projection. The caller must populate every signed field
// (including Fee, Sequence, and the top-level SigningPubKey) first.
func SignSponsor(tx txcore.Transaction, pubKeyHex, privateKeyHex string) (*txcore.SponsorSignature, error) {
	sig, err := SignTransaction(tx, privateKeyHex)
	if err != nil {
		return nil, err
	}
	return &txcore.SponsorSignature{SigningPubKey: pubKeyHex, TxnSignature: sig}, nil
}

// copyMap creates a shallow copy of a map to avoid modifying the original
func copyMap(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	maps.Copy(result, m)
	return result
}

func flattenForSigning(tx txcore.Transaction) (map[string]any, error) {
	if raw := tx.GetRawBytes(); len(raw) > 0 {
		txMap, err := binarycodec.DecodeBytes(raw)
		if err != nil {
			return nil, err
		}
		if tx.TxType() == txcore.TypeClawback {
			if err := txcore.ValidateTemplateFields(tx.TxType(), txMap); err != nil {
				return nil, err
			}
		}
		return txMap, nil
	}
	txMap, err := tx.Flatten()
	if err != nil {
		return nil, err
	}
	txcore.PopulateRequiredWireFields(txMap, tx.GetCommon())
	if tx.TxType() == txcore.TypeClawback {
		if err := txcore.ValidateTemplateFields(tx.TxType(), txMap); err != nil {
			return nil, err
		}
	}
	return txMap, nil
}

// getSigningPayload returns the binary data that should be signed
func getSigningPayload(tx txcore.Transaction) (string, error) {
	// Flatten the transaction to a map
	txMap, err := flattenForSigning(tx)
	if err != nil {
		return "", err
	}

	// Encode for signing (this adds the signing prefix and removes non-signing fields)
	return binarycodec.EncodeForSigning(txMap)
}

// verifySignatureForKey verifies a signature using the appropriate algorithm.
// mustBeFullyCanonical requires a secp256k1 signature to be low-S; ed25519
// signatures are always canonical and ignore the flag. Reference: rippled
// STTx::checkSingleSign / checkMultiSign pass fullyCanonical to verify().
func verifySignatureForKey(messageHex, pubKeyHex, signatureHex string, mustBeFullyCanonical bool) bool {
	// Decode the public key to determine the algorithm
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubKeyBytes) == 0 {
		return false
	}

	// The first byte indicates the key type
	// 0xED = ED25519
	// 0x02 or 0x03 = SECP256K1 (compressed)
	keyType := pubKeyBytes[0]

	// Decode the message hex to bytes for signing
	msgBytes, err := hex.DecodeString(messageHex)
	if err != nil {
		return false
	}

	// Convert message bytes to string for the crypto functions
	// The crypto functions expect the raw message bytes as a string
	msgStr := string(msgBytes)

	switch keyType {
	case 0xED:
		// ED25519
		algo := ed25519.Algorithm{}
		return algo.Validate(msgStr, pubKeyHex, signatureHex)

	case 0x02, 0x03:
		// SECP256K1 (compressed public key)
		algo := secp256k1.Algorithm{}
		return algo.ValidateWithCanonicality(msgStr, pubKeyHex, signatureHex, mustBeFullyCanonical)

	default:
		return false
	}
}

// SignTransaction signs a transaction with the given private key
// Returns the signature as a hex string
func SignTransaction(tx txcore.Transaction, privateKeyHex string) (string, error) {
	// Get the signing payload
	signingPayload, err := getSigningPayload(tx)
	if err != nil {
		return "", fmt.Errorf("failed to get signing payload: %w", err)
	}

	// Decode the private key to determine the algorithm
	privKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil || len(privKeyBytes) == 0 {
		return "", errors.New("invalid private key")
	}

	// Decode the message hex to bytes
	msgBytes, err := hex.DecodeString(signingPayload)
	if err != nil {
		return "", errors.New("failed to decode signing payload")
	}

	// Convert message bytes to string for the crypto functions
	msgStr := string(msgBytes)

	// The first byte indicates the key type
	keyType := privKeyBytes[0]

	var signature string

	switch keyType {
	case 0xED:
		// ED25519
		algo := ed25519.Algorithm{}
		signature, err = algo.Sign(msgStr, privateKeyHex)
		if err != nil {
			return "", fmt.Errorf("ED25519 signing failed: %w", err)
		}

	case 0x00:
		// SECP256K1
		algo := secp256k1.Algorithm{}
		signature, err = algo.Sign(msgStr, privateKeyHex)
		if err != nil {
			return "", fmt.Errorf("SECP256K1 signing failed: %w", err)
		}

	default:
		return "", ErrUnknownKeyType
	}

	return strings.ToUpper(signature), nil
}

// CalculateMultiSigFee calculates the fee for a multi-signed transaction
// The fee formula is: baseFee * (1 + numSigners)
// This matches rippled's Transactor::calculateBaseFee implementation
func CalculateMultiSigFee(baseFee uint64, numSigners int) uint64 {
	return baseFee * (1 + uint64(numSigners))
}

// CalculateDefaultBaseFee returns the ordinary transaction fee without any
// transaction-specific override.
func CalculateDefaultBaseFee(transaction txcore.Transaction, config txcore.EngineConfig) uint64 {
	common := transaction.GetCommon()
	return CalculateMultiSigFee(
		config.BaseFee,
		len(common.Signers)+SponsorSignerCount(transaction),
	)
}

// SponsorSignerCount returns the number of signatures in a sponsor's nested
// multisignature. A direct sponsor signature adds no extra fee unit; each
// nested Signer adds one base-fee unit.
func SponsorSignerCount(transaction txcore.Transaction) int {
	if transaction == nil || transaction.GetCommon() == nil {
		return 0
	}
	sponsor := transaction.GetCommon().SponsorSignature
	if sponsor == nil {
		return 0
	}
	return len(sponsor.Signers)
}

// CalculateBaseFee dispatches transaction-specific fees before falling back to
// the standard single-sign or multisign fee.
func CalculateBaseFee(transaction txcore.Transaction, view txcore.LedgerView, config txcore.EngineConfig) uint64 {
	if transaction.TxType() == txcore.TypeRegularKeySet && view != nil {
		accountID, err := state.DecodeAccountID(transaction.GetCommon().Account)
		if err == nil {
			account, readErr := txcore.ReadAccountRoot(view, accountID)
			if readErr == nil && txcore.SetRegularKeyFeeWaived(config.SkipSignatureVerification, transaction.GetCommon(), account) {
				return 0
			}
		}
	}
	if calculator, ok := transaction.(txcore.BatchFeeCalculator); ok {
		return calculator.CalculateMinimumFee(view, config)
	}
	if calculator, ok := transaction.(txcore.CustomBaseFeeCalculator); ok {
		return calculator.CalculateBaseFee(view, config)
	}
	return CalculateDefaultBaseFee(transaction, config)
}

// SignTransactionForMultiSign signs a transaction for multi-signing
// Each signer signs a message that includes their account ID as a suffix
// Returns the signature as a hex string
func SignTransactionForMultiSign(tx txcore.Transaction, signerAccount string, privateKeyHex string) (string, error) {
	return signTransactionForMultiSign(tx, signerAccount, privateKeyHex, false)
}

// SignTransactionForMultiSignTarget signs for a nested signature object while
// retaining the transaction's outer SigningPubKey in the payload.
func SignTransactionForMultiSignTarget(tx txcore.Transaction, signerAccount string, privateKeyHex string) (string, error) {
	return signTransactionForMultiSign(tx, signerAccount, privateKeyHex, true)
}

func signTransactionForMultiSign(tx txcore.Transaction, signerAccount string, privateKeyHex string, target bool) (string, error) {
	// Flatten the transaction to a map
	txMap, err := flattenForSigning(tx)
	if err != nil {
		return "", fmt.Errorf("failed to flatten transaction: %w", err)
	}

	// Get the multi-signing payload for this specific signer
	var signingPayload string
	if target {
		signingPayload, err = binarycodec.EncodeForMultisigningTarget(txMap, signerAccount)
	} else {
		signingPayload, err = binarycodec.EncodeForMultisigning(txMap, signerAccount)
	}
	if err != nil {
		return "", fmt.Errorf("failed to encode for multi-signing: %w", err)
	}

	// Decode the private key to determine the algorithm
	privKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil || len(privKeyBytes) == 0 {
		return "", errors.New("invalid private key")
	}

	// Decode the message hex to bytes
	msgBytes, err := hex.DecodeString(signingPayload)
	if err != nil {
		return "", errors.New("failed to decode signing payload")
	}

	// Convert message bytes to string for the crypto functions
	msgStr := string(msgBytes)

	// The first byte indicates the key type
	keyType := privKeyBytes[0]

	var signature string

	switch keyType {
	case 0xED:
		// ED25519
		algo := ed25519.Algorithm{}
		signature, err = algo.Sign(msgStr, privateKeyHex)
		if err != nil {
			return "", fmt.Errorf("ED25519 signing failed: %w", err)
		}

	case 0x00:
		// SECP256K1
		algo := secp256k1.Algorithm{}
		signature, err = algo.Sign(msgStr, privateKeyHex)
		if err != nil {
			return "", fmt.Errorf("SECP256K1 signing failed: %w", err)
		}

	default:
		return "", ErrUnknownKeyType
	}

	return strings.ToUpper(signature), nil
}

// AddMultiSigner adds a signer to a transaction's Signers array
// The signer should have already signed the transaction using SignTransactionForMultiSign
// Signers are maintained in sorted order by binary AccountID, matching rippled.
func AddMultiSigner(tx txcore.Transaction, account, publicKey, signature string) error {
	common := tx.GetCommon()

	// Clear single-signature fields if this is the first multi-signer
	if len(common.Signers) == 0 {
		common.SigningPubKey = ""
		common.TxnSignature = ""
	}

	// Decode the new signer's AccountID for binary comparison
	newID, err := state.DecodeAccountID(account)
	if err != nil {
		return fmt.Errorf("invalid signer account: %w", err)
	}

	// Create the new signer entry
	newSigner := txcore.SignerWrapper{
		Signer: txcore.Signer{
			Account:       account,
			SigningPubKey: publicKey,
			TxnSignature:  signature,
		},
	}

	// Find the correct position to insert (maintain sorted order by binary AccountID).
	// Reference: rippled STTx.cpp multiSignHelper — signers sorted by AccountID.
	insertPos := len(common.Signers)
	for i, sw := range common.Signers {
		if sw.Signer.Account == account {
			return ErrDuplicateSigner
		}
		existingID, decErr := state.DecodeAccountID(sw.Signer.Account)
		if decErr != nil {
			continue
		}
		if bytes.Compare(existingID[:], newID[:]) > 0 {
			insertPos = i
			break
		}
	}

	// Insert at the correct position
	common.Signers = append(common.Signers, txcore.SignerWrapper{})
	copy(common.Signers[insertPos+1:], common.Signers[insertPos:])
	common.Signers[insertPos] = newSigner

	return nil
}
