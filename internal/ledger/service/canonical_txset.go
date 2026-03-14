package service

import (
	"bytes"
	"sort"

	"github.com/LeJamon/goXRPLd/shamap"
)

// pendingTx holds a transaction that was applied during the open ledger phase.
// At ledger_accept time, pending transactions are re-applied in canonical order.
// Reference: rippled CanonicalTXSet
type pendingTx struct {
	txBlob   []byte   // raw binary blob
	hash     [32]byte // transaction hash (SHA-512Half of TXN prefix + blob)
	account  [20]byte // sender account ID (raw 20 bytes)
	sequence uint32   // effective sequence (SeqProxy: Sequence or TicketSequence)
}

// canonicalSort sorts pending transactions using the CanonicalTXSet ordering from rippled.
// The sort key is (accountKey, sequence, txID) where accountKey = account XOR salt[:20].
// The salt is derived from the SHA-512Half of all transaction hashes concatenated in
// sorted order, approximating rippled's SHAMap hash of the transaction set.
// Reference: rippled CanonicalTXSet.cpp
func canonicalSort(txs []pendingTx) {
	if len(txs) <= 1 {
		return
	}

	// Compute the salt: SHA-512Half of all tx hashes concatenated in sorted (hash) order.
	// This approximates the SHAMap hash used as salt in rippled's CanonicalTXSet.
	salt := computeSalt(txs)

	// Pre-compute the account keys (account XOR salt[:20]) for sorting.
	// In rippled, account is copied into a 32-byte uint256 (padded with zeros),
	// then XORed with the full 32-byte salt. We only need the first 20 bytes
	// for comparison since bytes 20-31 of the account uint256 are zero and thus
	// equal to salt[20:31] XOR 0 = salt[20:31] for all entries.
	type sortEntry struct {
		accountKey [32]byte
		tx         *pendingTx
	}

	entries := make([]sortEntry, len(txs))
	for i := range txs {
		entries[i].tx = &txs[i]
		entries[i].accountKey = computeAccountKey(txs[i].account, salt)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		// Compare accountKey (32 bytes)
		cmp := bytes.Compare(entries[i].accountKey[:], entries[j].accountKey[:])
		if cmp != 0 {
			return cmp < 0
		}
		// Compare sequence
		if entries[i].tx.sequence != entries[j].tx.sequence {
			return entries[i].tx.sequence < entries[j].tx.sequence
		}
		// Compare txID (hash)
		return bytes.Compare(entries[i].tx.hash[:], entries[j].tx.hash[:]) < 0
	})

	// Write sorted results back to the slice
	sorted := make([]pendingTx, len(txs))
	for i, e := range entries {
		sorted[i] = *e.tx
	}
	copy(txs, sorted)
}

// computeSalt returns a deterministic salt derived from the transaction set.
// Matches rippled: builds a SHAMap of type TRANSACTION with each tx blob
// keyed by its hash (node type tnTRANSACTION_NM), then returns the root hash.
// Reference: rippled RCLConsensus.cpp onClose() lines 335-349
func computeSalt(txs []pendingTx) [32]byte {
	txMap, err := shamap.New(shamap.TypeTransaction)
	if err != nil {
		return [32]byte{}
	}
	for _, ptx := range txs {
		_ = txMap.PutWithNodeType(ptx.hash, ptx.txBlob, shamap.NodeTypeTransactionNoMeta)
	}
	hash, err := txMap.Hash()
	if err != nil {
		return [32]byte{}
	}
	return hash
}

// computeAccountKey computes the sort key for an account.
// Mirrors rippled: copy 20-byte account into 32-byte uint256, then XOR with salt.
// Reference: rippled CanonicalTXSet::accountKey()
func computeAccountKey(account [20]byte, salt [32]byte) [32]byte {
	var key [32]byte
	// Copy account into first 20 bytes (bytes 20-31 remain zero)
	copy(key[:20], account[:])
	// XOR with full 32-byte salt
	for i := 0; i < 32; i++ {
		key[i] ^= salt[i]
	}
	return key
}
