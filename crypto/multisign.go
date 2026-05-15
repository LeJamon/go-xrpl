package crypto

import (
	"encoding/binary"
)

// HashPrefix represents hash prefixes used in XRPL for domain separation.
// These prefixes are inserted before the source material to put each hash
// in its own "space", ensuring different types of objects produce different hashes.
type HashPrefix uint32

const (
	AccountIDSize = 20

	// HashPrefixTransactionID is the prefix for transaction ID calculation (TXN\0).
	HashPrefixTransactionID HashPrefix = 0x54584E00

	// HashPrefixTxNode is the prefix for transaction + metadata (SND\0).
	HashPrefixTxNode HashPrefix = 0x534E4400

	// HashPrefixLeafNode is the prefix for account state (MLN\0).
	HashPrefixLeafNode HashPrefix = 0x4D4C4E00

	// HashPrefixInnerNode is the prefix for inner node in V1 tree (MIN\0).
	HashPrefixInnerNode HashPrefix = 0x4D494E00

	// HashPrefixLedgerMaster is the prefix for ledger master data signing (LWR\0).
	HashPrefixLedgerMaster HashPrefix = 0x4C575200

	// HashPrefixTxSign is the prefix for inner transaction to sign (STX\0).
	HashPrefixTxSign HashPrefix = 0x53545800

	// HashPrefixTxMultiSign is the prefix for inner transaction to multi-sign (SMT\0).
	HashPrefixTxMultiSign HashPrefix = 0x534D5400

	// HashPrefixValidation is the prefix for validation signing (VAL\0).
	HashPrefixValidation HashPrefix = 0x56414C00

	// HashPrefixProposal is the prefix for proposal signing (PRP\0).
	HashPrefixProposal HashPrefix = 0x50525000

	// HashPrefixManifest is the prefix for manifest (MAN\0).
	HashPrefixManifest HashPrefix = 0x4D414E00

	// HashPrefixPaymentChannelClaim is the prefix for payment channel claim (CLM\0).
	HashPrefixPaymentChannelClaim HashPrefix = 0x434C4D00

	// HashPrefixCredential is the prefix for credentials signature (CRD\0).
	HashPrefixCredential HashPrefix = 0x43524400

	// HashPrefixBatch is the prefix for batch (BCH\0).
	HashPrefixBatch HashPrefix = 0x42434800
)

// hashPrefixBytes caches the immutable 4-byte form of every known
// HashPrefix so that Bytes() is allocation-free on the hot signing/hashing
// path. The slices alias shared package state and must not be mutated.
//
// Note: the protocol package exposes the same XRPL hash-prefix constants
// in [4]byte form (see protocol.HashPrefix*). They live in two packages
// because protocol is the public, structural source of truth for the
// protocol layer while crypto.HashPrefix is the internal uint32-encoded
// form used by signing helpers in this package.
var hashPrefixBytes = func() map[HashPrefix][]byte {
	all := []HashPrefix{
		HashPrefixTransactionID, HashPrefixTxNode, HashPrefixLeafNode,
		HashPrefixInnerNode, HashPrefixLedgerMaster, HashPrefixTxSign,
		HashPrefixTxMultiSign, HashPrefixValidation, HashPrefixProposal,
		HashPrefixManifest, HashPrefixPaymentChannelClaim,
		HashPrefixCredential, HashPrefixBatch,
	}
	m := make(map[HashPrefix][]byte, len(all))
	for _, hp := range all {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(hp))
		m[hp] = b
	}
	return m
}()

// MultiSignPrefix is the raw bytes for the multi-sign hash prefix ("SMT\0").
// This is the same value as HashPrefixTxMultiSign but as bytes. It aliases
// shared package state and must not be mutated.
var MultiSignPrefix = hashPrefixBytes[HashPrefixTxMultiSign]

// Bytes returns the hash prefix as a 4-byte big-endian slice. The returned
// slice aliases shared package state and must not be mutated.
func (hp HashPrefix) Bytes() []byte {
	if b, ok := hashPrefixBytes[hp]; ok {
		return b
	}
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(hp))
	return b
}

// BuildMultiSigningData constructs the signing data for a multi-signature.
// It prepends the multi-sign hash prefix to the transaction data.
//
// The multi-signing process:
//  1. Start with the serialized transaction (without signatures)
//  2. Prepend the multi-sign prefix ("SMT\0")
//  3. Append the signer's account ID (using FinishMultiSigningData)
//  4. Hash the result for signing
//
// This function performs step 1-2. Use FinishMultiSigningData for step 3.
func BuildMultiSigningData(txData []byte) []byte {
	result := make([]byte, len(MultiSignPrefix)+len(txData))
	copy(result, MultiSignPrefix)
	copy(result[len(MultiSignPrefix):], txData)
	return result
}

// StartMultiSigningData is an alias for BuildMultiSigningData.
// It creates the initial part of the multi-signing data that is shared
// across all signers.
func StartMultiSigningData(txData []byte) []byte {
	return BuildMultiSigningData(txData)
}

// FinishMultiSigningData appends the signer's account ID to the signing data.
// This completes the multi-signing data construction for a specific signer.
//
// The account ID is included to prevent signature theft attacks:
// Without it, a signature from one account could be reused by another account
// that has the same signing key (e.g., if they share a RegularKey).
func FinishMultiSigningData(signingData []byte, accountID [AccountIDSize]byte) []byte {
	result := make([]byte, len(signingData)+AccountIDSize)
	copy(result, signingData)
	copy(result[len(signingData):], accountID[:])
	return result
}

// FinishMultiSigningDataBytes is like FinishMultiSigningData but accepts
// the account ID as a byte slice. Returns nil if accountID is not 20 bytes.
func FinishMultiSigningDataBytes(signingData, accountID []byte) []byte {
	if len(accountID) != AccountIDSize {
		return nil
	}
	result := make([]byte, len(signingData)+AccountIDSize)
	copy(result, signingData)
	copy(result[len(signingData):], accountID)
	return result
}

// BuildCompleteMultiSigningData constructs the complete signing data for
// a multi-signature in a single call. This is equivalent to calling
// BuildMultiSigningData followed by FinishMultiSigningData.
func BuildCompleteMultiSigningData(txData []byte, accountID [AccountIDSize]byte) []byte {
	result := make([]byte, len(MultiSignPrefix)+len(txData)+AccountIDSize)
	copy(result, MultiSignPrefix)
	copy(result[len(MultiSignPrefix):], txData)
	copy(result[len(MultiSignPrefix)+len(txData):], accountID[:])
	return result
}

// PrependHashPrefix prepends the specified hash prefix to the data.
// This is used for various signing and hashing operations in XRPL.
func PrependHashPrefix(prefix HashPrefix, data []byte) []byte {
	prefixBytes := prefix.Bytes()
	result := make([]byte, len(prefixBytes)+len(data))
	copy(result, prefixBytes)
	copy(result[len(prefixBytes):], data)
	return result
}
