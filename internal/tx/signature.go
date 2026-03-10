package tx

import (
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
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
	ErrNotMultiSigning = errors.New("tefNOT_MULTI_SIGNING: account is not configured for multi-signing")

	// ErrBadQuorum is returned when signers fail to meet the quorum (tefBAD_QUORUM)
	ErrBadQuorum = errors.New("tefBAD_QUORUM: signers failed to meet quorum")

	// ErrBadSignature is returned when a multi-sig signature is invalid (tefBAD_SIGNATURE)
	ErrBadSignature = errors.New("tefBAD_SIGNATURE: invalid signer or signature")

	// ErrMasterDisabled is returned when trying to sign with a disabled master key (tefMASTER_DISABLED)
	ErrMasterDisabled = errors.New("tefMASTER_DISABLED: master key is disabled for this signer")

	// ErrNoSigners is returned when Signers array is empty
	ErrNoSigners = errors.New("multi-signed transaction has no signers")

	// ErrDuplicateSigner is returned when duplicate signers are found
	ErrDuplicateSigner = errors.New("duplicate signer in transaction")

	// ErrSignersNotSorted is returned when signers are not sorted by account
	ErrSignersNotSorted = errors.New("signers must be sorted by account")
)

// SignerListLookup is the interface for looking up an account's signer list
// This must be implemented by the ledger/state layer
type SignerListLookup interface {
	// GetSignerList returns the signer list for an account
	// Returns nil, nil if the account has no signer list
	// Returns nil, error if there was an error looking up the signer list
	GetSignerList(account string) (*state.SignerListInfo, error)

	// GetAccountInfo returns account information needed for signer validation
	// Returns the account's flags (to check if master key is disabled) and regular key
	GetAccountInfo(account string) (flags uint32, regularKey string, err error)
}

// Note: state.LsfDisableMaster is defined in account_root.go (0x00100000)

// IsMultiSigned returns true if the transaction is multi-signed
// A transaction is multi-signed if it has a Signers array and an empty SigningPubKey
func IsMultiSigned(tx Transaction) bool {
	common := tx.GetCommon()
	return len(common.Signers) > 0 && common.SigningPubKey == ""
}

// VerifySignature verifies that a transaction is properly signed
// Returns nil if the signature is valid, or an error describing the problem
// For multi-signed transactions, use VerifyMultiSignature instead
func VerifySignature(tx Transaction) error {
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
		return errors.New("failed to get signing payload: " + err.Error())
	}

	// Verify the signature based on the key type
	valid := verifySignatureForKey(signingPayload, common.SigningPubKey, common.TxnSignature)
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
// Returns nil if all signatures are valid and the quorum is met
func VerifyMultiSignature(tx Transaction, lookup SignerListLookup) error {
	common := tx.GetCommon()

	// Verify this is actually a multi-signed transaction
	if !IsMultiSigned(tx) {
		if common.TxnSignature != "" {
			// This is a single-signed transaction, use VerifySignature
			return VerifySignature(tx)
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
		return errors.New("failed to get signer list: " + err.Error())
	}
	if signerList == nil {
		return ErrNotMultiSigning
	}

	// Build a map of authorized signers for quick lookup
	authorizedSigners := make(map[string]state.AccountSignerEntry)
	for _, entry := range signerList.SignerEntries {
		authorizedSigners[entry.Account] = entry
	}

	// Sort the authorized signers by account for the matching algorithm
	sortedAuthSigners := make([]state.AccountSignerEntry, len(signerList.SignerEntries))
	copy(sortedAuthSigners, signerList.SignerEntries)
	sort.Slice(sortedAuthSigners, func(i, j int) bool {
		return sortedAuthSigners[i].Account < sortedAuthSigners[j].Account
	})

	// Get the multi-signing payload for this transaction
	// Each signer signs a different message (transaction + their account ID suffix)
	txMap, err := tx.Flatten()
	if err != nil {
		return errors.New("failed to flatten transaction: " + err.Error())
	}

	// Verify signers are sorted by account (required by XRPL)
	var prevAccount string
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

		// Check signers are sorted
		if txSignerAccount < prevAccount {
			return ErrSignersNotSorted
		}
		prevAccount = txSignerAccount

		// Match the signer to an authorized signer (both lists are sorted)
		for authIter < len(sortedAuthSigners) && sortedAuthSigners[authIter].Account < txSignerAccount {
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

		// Verify the signing relationship (following rippled's rules):
		// 1. Phantom account: signingAcctID == signingAcctIDFromPubKey and account not in ledger
		// 2. Master Key: signingAcctID == signingAcctIDFromPubKey and master key not disabled
		// 3. Regular Key: signingAcctID != signingAcctIDFromPubKey and pubkey matches regular key
		if signingAcctIDFromPubKey == txSignerAccount {
			// Either Phantom or Master Key case
			// Check if the signer account exists in the ledger
			flags, _, lookupErr := lookup.GetAccountInfo(txSignerAccount)
			if lookupErr == nil {
				// Account exists - this is the Master Key case
				// Check if master key is disabled
				if flags&state.LsfDisableMaster != 0 {
					return ErrMasterDisabled
				}
			}
			// If account doesn't exist, it's a Phantom account - allowed
		} else {
			// May be a Regular Key case
			// The public key must hash to the signer's regular key
			flags, regularKey, lookupErr := lookup.GetAccountInfo(txSignerAccount)
			if lookupErr != nil {
				// Non-phantom signer lacks account root
				return ErrBadSignature
			}
			_ = flags // flags checked above if needed

			if regularKey == "" {
				// Account lacks RegularKey
				return ErrBadSignature
			}

			if signingAcctIDFromPubKey != regularKey {
				// Account doesn't match RegularKey
				return ErrBadSignature
			}
		}

		// Get the multi-signing payload for this specific signer
		signingPayload, err := binarycodec.EncodeForMultisigning(copyMap(txMap), txSignerAccount)
		if err != nil {
			return errors.New("failed to encode for multi-signing: " + err.Error())
		}

		// Verify the signature
		valid := verifySignatureForKey(signingPayload, signer.SigningPubKey, signer.TxnSignature)
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
	for k, v := range m {
		result[k] = v
	}
	return result
}

// verifyPublicKeyMatchesAccount checks that the public key derives to the given account
func verifyPublicKeyMatchesAccount(pubKeyHex, account string) error {
	// Derive the account address from the public key
	derivedAddress, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
	if err != nil {
		return errors.New("failed to derive address from public key: " + err.Error())
	}

	if derivedAddress != account {
		return ErrPublicKeyMismatch
	}

	return nil
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

// verifySignatureForKey verifies a signature using the appropriate algorithm
func verifySignatureForKey(messageHex, pubKeyHex, signatureHex string) bool {
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
		return algo.Validate(msgStr, pubKeyHex, signatureHex)

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
		return "", errors.New("failed to get signing payload: " + err.Error())
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
			return "", errors.New("ED25519 signing failed: " + err.Error())
		}

	case 0x00:
		// SECP256K1
		algo := secp256k1.SECP256K1()
		signature, err = algo.Sign(msgStr, privateKeyHex)
		if err != nil {
			return "", errors.New("SECP256K1 signing failed: " + err.Error())
		}

	default:
		return "", ErrUnknownKeyType
	}

	return strings.ToUpper(signature), nil
}

// DeriveAddressFromPublicKey derives a classic address from a public key
func DeriveAddressFromPublicKey(publicKeyHex string) (string, error) {
	return addresscodec.EncodeClassicAddressFromPublicKeyHex(publicKeyHex)
}

// CalculateMultiSigFee calculates the fee for a multi-signed transaction
// The fee formula is: baseFee * (1 + numSigners)
// This matches rippled's Transactor::calculateBaseFee implementation
func CalculateMultiSigFee(baseFee uint64, numSigners int) uint64 {
	return baseFee * (1 + uint64(numSigners))
}

// CalculateMultiSigFeeDrops calculates the fee in drops for a multi-signed transaction
// baseFeeDrops is the base fee in drops (e.g., 10 for the standard base fee)
// numSigners is the number of signers in the transaction
func CalculateMultiSigFeeDrops(baseFeeDrops string, numSigners int) (string, error) {
	baseFee, err := strconv.ParseUint(baseFeeDrops, 10, 64)
	if err != nil {
		return "", errors.New("invalid base fee: " + err.Error())
	}

	totalFee := CalculateMultiSigFee(baseFee, numSigners)
	return strconv.FormatUint(totalFee, 10), nil
}

// GetTransactionSignerCount returns the number of signers in a transaction
// Returns 0 for single-signed transactions
func GetTransactionSignerCount(tx Transaction) int {
	common := tx.GetCommon()
	return len(common.Signers)
}

// SignTransactionForMultiSign signs a transaction for multi-signing
// Each signer signs a message that includes their account ID as a suffix
// Returns the signature as a hex string
func SignTransactionForMultiSign(tx Transaction, signerAccount string, privateKeyHex string) (string, error) {
	// Flatten the transaction to a map
	txMap, err := tx.Flatten()
	if err != nil {
		return "", errors.New("failed to flatten transaction: " + err.Error())
	}

	// Get the multi-signing payload for this specific signer
	signingPayload, err := binarycodec.EncodeForMultisigning(txMap, signerAccount)
	if err != nil {
		return "", errors.New("failed to encode for multi-signing: " + err.Error())
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
			return "", errors.New("ED25519 signing failed: " + err.Error())
		}

	case 0x00:
		// SECP256K1
		algo := secp256k1.SECP256K1()
		signature, err = algo.Sign(msgStr, privateKeyHex)
		if err != nil {
			return "", errors.New("SECP256K1 signing failed: " + err.Error())
		}

	default:
		return "", ErrUnknownKeyType
	}

	return strings.ToUpper(signature), nil
}

// AddMultiSigner adds a signer to a transaction's Signers array
// The signer should have already signed the transaction using SignTransactionForMultiSign
// Signers must be added in sorted order by account address
func AddMultiSigner(tx Transaction, account, publicKey, signature string) error {
	common := tx.GetCommon()

	// Clear single-signature fields if this is the first multi-signer
	if len(common.Signers) == 0 {
		common.SigningPubKey = ""
		common.TxnSignature = ""
	}

	// Create the new signer entry
	newSigner := SignerWrapper{
		Signer: Signer{
			Account:       account,
			SigningPubKey: publicKey,
			TxnSignature:  signature,
		},
	}

	// Find the correct position to insert (maintain sorted order)
	insertPos := len(common.Signers)
	for i, sw := range common.Signers {
		if sw.Signer.Account == account {
			return ErrDuplicateSigner
		}
		if sw.Signer.Account > account {
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
