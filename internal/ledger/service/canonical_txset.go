package service

import (
	"bytes"
	"sort"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/tx"
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
//
// Salt source by call site (per-call-site convention; the C++ constructor
// signature `CanonicalTXSet(LedgerHash const& salt)` names the parameter
// type but does NOT constrain the semantic value):
//
//   - Consensus build path (rippled RCLConsensus.cpp:512):
//       `CanonicalTXSet retriableTxs{result.txns.map_->getHash().as_uint256()}`
//     Salt = SHAMap root of the AGREED tx set. Use `computeSalt(txs)`.
//   - Held-tx replay (rippled LedgerMaster.cpp:461):
//       `CanonicalTXSet set(...->info().parentHash)`
//     Salt = open ledger's parent (= LCL) hash.
//   - Local-tx pickup (rippled LocalTxs.cpp:126):
//       `CanonicalTXSet tset(uint256{})`
//     Zero salt.
//
// Reference: rippled CanonicalTXSet.cpp / CanonicalTXSet.h, RCLConsensus.cpp:508-514
func canonicalSort(txs []pendingTx, salt [32]byte) {
	if len(txs) <= 1 {
		return
	}

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
		cmp := bytes.Compare(entries[i].accountKey[:], entries[j].accountKey[:])
		if cmp != 0 {
			return cmp < 0
		}
		if entries[i].tx.sequence != entries[j].tx.sequence {
			return entries[i].tx.sequence < entries[j].tx.sequence
		}
		return bytes.Compare(entries[i].tx.hash[:], entries[j].tx.hash[:]) < 0
	})

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

// computeSalt builds the RCLTxSet SHAMap (TypeTransaction, tnTRANSACTION_NM
// keyed by tx hash) and returns its root — the value rippled passes as the
// CanonicalTXSet salt on the consensus-build path. Insertion-order-independent.
// Returns the zero hash on construction failure; canonicalSort remains
// deterministic via (sequence, txID) tiebreakers.
// Reference: rippled RCLConsensus.cpp:512, RCLCxTx.h:62-90.
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
