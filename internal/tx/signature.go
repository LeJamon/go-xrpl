package tx

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// Bounds on the size of a multi-signer array, mirroring rippled
// STTx::minMultiSigners / STTx::maxMultiSigners. The maximum is amendment-gated:
// featureExpandedSignerList raises it from 8 to 32.
const (
	minMultiSigners    = 1
	maxSignersBase     = 8
	maxSignersExpanded = 32
)

// MaxMultiSigners returns the upper bound on a multi-signer array for the given
// rules: 32 with featureExpandedSignerList enabled, 8 otherwise. It governs both
// the regular Signers array and a Batch signer's nested Signers array.
// Reference: rippled STTx::maxMultiSigners.
func MaxMultiSigners(rules *amendment.Rules) int {
	if rules != nil && rules.Enabled(amendment.FeatureExpandedSignerList) {
		return maxSignersExpanded
	}
	return maxSignersBase
}

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
func IsMultiSigned(tx Transaction) bool {
	common := tx.GetCommon()
	return len(common.Signers) > 0 && common.SigningPubKey == ""
}

// VerifySignature verifies that a transaction is properly signed.
// Returns nil if the signature is valid, or an error describing the problem.
// For multi-signed transactions, use VerifyMultiSignature instead.
//
// mustBeFullyCanonical requires secp256k1 signatures to be low-S. The caller
// derives it from the RequireFullyCanonicalSig amendment and the per-tx
// tfFullyCanonicalSig flag (rippled apply.cpp:78-84 + STTx::checkSingleSign).
func VerifySignature(tx Transaction, mustBeFullyCanonical bool) error {
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
func VerifyMultiSignature(tx Transaction, lookup SignerListLookup, mustBeFullyCanonical bool) error {
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
	txMap, err := tx.Flatten()
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
		var acct signerAccountState
		switch {
		case lookupErr == nil:
			acct = signerAccountState{found: true, flags: flags, regularKey: regularKey}
		case errors.Is(lookupErr, ErrAccountNotFound):
			// Account absent — phantom branch (found stays false).
		default:
			// Real storage/parse failure — never silently allow the signer.
			return ErrInternalLookup
		}
		switch authorizeMultiSigner(txSignerAccount, signingAcctIDFromPubKey, acct) {
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

// copyMap creates a shallow copy of a map to avoid modifying the original
func copyMap(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	maps.Copy(result, m)
	return result
}

// getSigningPayload returns the binary data that should be signed
func getSigningPayload(tx Transaction) (string, error) {
	// Flatten the transaction to a map
	txMap, err := tx.Flatten()
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
		algo := ed25519.ED25519()
		return algo.Validate(msgStr, pubKeyHex, signatureHex)

	case 0x02, 0x03:
		// SECP256K1 (compressed public key)
		algo := secp256k1.SECP256K1()
		return algo.ValidateWithCanonicality(msgStr, pubKeyHex, signatureHex, mustBeFullyCanonical)

	default:
		return false
	}
}

// SignTransaction signs a transaction with the given private key
// Returns the signature as a hex string
func SignTransaction(tx Transaction, privateKeyHex string) (string, error) {
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
		algo := ed25519.ED25519()
		signature, err = algo.Sign(msgStr, privateKeyHex)
		if err != nil {
			return "", fmt.Errorf("ED25519 signing failed: %w", err)
		}

	case 0x00:
		// SECP256K1
		algo := secp256k1.SECP256K1()
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

// SignTransactionForMultiSign signs a transaction for multi-signing
// Each signer signs a message that includes their account ID as a suffix
// Returns the signature as a hex string
func SignTransactionForMultiSign(tx Transaction, signerAccount string, privateKeyHex string) (string, error) {
	// Flatten the transaction to a map
	txMap, err := tx.Flatten()
	if err != nil {
		return "", fmt.Errorf("failed to flatten transaction: %w", err)
	}

	// Get the multi-signing payload for this specific signer
	signingPayload, err := binarycodec.EncodeForMultisigning(txMap, signerAccount)
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
		algo := ed25519.ED25519()
		signature, err = algo.Sign(msgStr, privateKeyHex)
		if err != nil {
			return "", fmt.Errorf("ED25519 signing failed: %w", err)
		}

	case 0x00:
		// SECP256K1
		algo := secp256k1.SECP256K1()
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
func AddMultiSigner(tx Transaction, account, publicKey, signature string) error {
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
	newSigner := SignerWrapper{
		Signer: Signer{
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
	common.Signers = append(common.Signers, SignerWrapper{})
	copy(common.Signers[insertPos+1:], common.Signers[insertPos:])
	common.Signers[insertPos] = newSigner

	return nil
}
