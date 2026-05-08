package service

import (
	"bytes"
	"sort"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/tx"
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
// The salt is the LCL's ledger hash — see rippled CanonicalTXSet's constructor
// `CanonicalTXSet(LedgerHash const& salt)`. The previous implementation used the
// SHAMap hash of the new tx set as salt, which produced a different sort order from
// rippled and broke sfTransactionIndex parity in metadata, forking the network at
// every multi-tx ledger.
// Reference: rippled CanonicalTXSet.cpp / CanonicalTXSet.h
func canonicalSort(txs []pendingTx, salt [32]byte) {
	if len(txs) <= 1 {
		return
	}

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

// parsePendingTx creates a pendingTx from a raw transaction blob.
// It parses the blob to extract account, sequence, and hash.
func parsePendingTx(blob []byte) (pendingTx, error) {
	transaction, err := tx.ParseFromBinary(blob)
	if err != nil {
		return pendingTx{}, err
	}
	transaction.SetRawBytes(blob)

	common := transaction.GetCommon()

	var accountID [20]byte
	_, accountBytes, decErr := addresscodec.DecodeClassicAddressToAccountID(common.Account)
	if decErr == nil && len(accountBytes) == 20 {
		copy(accountID[:], accountBytes)
	}

	txHash, hashErr := tx.ComputeTransactionHash(transaction)
	if hashErr != nil {
		return pendingTx{}, hashErr
	}

	return pendingTx{
		txBlob:   blob,
		hash:     txHash,
		account:  accountID,
		sequence: common.SeqProxy(),
	}, nil
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
