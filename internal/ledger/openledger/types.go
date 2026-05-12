// Package openledger implements rippled's OpenLedger semantics for
// goXRPL. The open ledger is a node-local view that holds applied-but-
// not-yet-validated transactions: it sits between the latest closed
// ledger and the next-to-be-built closed ledger.
// Reference: rippled OpenLedger.h:209-270, BuildLedger.cpp:107-170.
package openledger

import (
	"bytes"
	"sort"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/shamap"
)

// PendingTx is a parsed pending transaction used by the apply loop and
// canonical sort. Exported because the consensus build path still feeds
// canonical-sorted slices into ApplyTxs (rippled RCLConsensus.cpp:512
// uses the SHAMap root of the agreed tx set as the salt).
type PendingTx struct {
	// Blob is the raw signed binary transaction.
	Blob []byte
	// Hash is SHA-512Half of the TXN prefix concatenated with Blob.
	Hash [32]byte
	// Account is the 20-byte sender AccountID.
	Account [20]byte
	// Sequence is the effective sequence (Sequence or TicketSequence
	// via SeqProxy).
	Sequence uint32
	// LastLedgerSequence is the tx's sfLastLedgerSequence, or 0 when
	// unset. LocalTxs uses it to clamp the held-pool expiration so a tx
	// never lingers past its own validity window.
	LastLedgerSequence uint32
	// IsTicket is true when the tx consumes a Ticket rather than a raw
	// Sequence. LocalTxs.Sweep uses this to switch between the seq-
	// advance check and the ticket-burn check.
	IsTicket bool
}

// Result classifies the outcome of applying a single transaction in the
// 3-pass loop. Mirrors rippled OpenLedger::Result (OpenLedger.h:192) and
// OpenLedger::apply_one (OpenLedger.cpp:170-189):
//   - Success: applied or terQUEUED
//   - Failure: tef / tem / tel (permanent drop)
//   - Retry:   tec, ter, and anything else (try again on next pass)
type Result int

const (
	ResultSuccess Result = iota
	ResultFailure
	ResultRetry
)

// ParsePendingTx creates a PendingTx from a raw transaction blob,
// extracting the account ID, sequence proxy, and tx hash.
func ParsePendingTx(blob []byte) (PendingTx, error) {
	transaction, err := tx.ParseFromBinary(blob)
	if err != nil {
		return PendingTx{}, err
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
		return PendingTx{}, hashErr
	}

	var lastLedger uint32
	if common.LastLedgerSequence != nil {
		lastLedger = *common.LastLedgerSequence
	}

	return PendingTx{
		Blob:               blob,
		Hash:               txHash,
		Account:            accountID,
		Sequence:           common.SeqProxy(),
		LastLedgerSequence: lastLedger,
		IsTicket:           common.TicketSequence != nil,
	}, nil
}

// CanonicalSort sorts pending transactions using the CanonicalTXSet
// ordering from rippled. The sort key is (accountKey, sequence, txID)
// where accountKey = account XOR salt[:20].
//
// Salt source by call site (per-call-site convention; the C++ constructor
// signature `CanonicalTXSet(LedgerHash const& salt)` names the parameter
// type but does NOT constrain the semantic value):
//
//   - Consensus build path (rippled RCLConsensus.cpp:512):
//     `CanonicalTXSet retriableTxs{result.txns.map_->getHash().as_uint256()}`
//     Salt = SHAMap root of the AGREED tx set. Use ComputeSalt(txs).
//   - Held-tx replay (rippled LedgerMaster.cpp:461):
//     `CanonicalTXSet set(...->info().parentHash)`
//     Salt = open ledger's parent (= LCL) hash.
//   - Local-tx pickup (rippled LocalTxs.cpp:126):
//     `CanonicalTXSet tset(uint256{})`
//     Zero salt.
//
// Reference: rippled CanonicalTXSet.cpp / CanonicalTXSet.h, RCLConsensus.cpp:508-514.
func CanonicalSort(txs []PendingTx, salt [32]byte) {
	if len(txs) <= 1 {
		return
	}

	type sortEntry struct {
		accountKey [32]byte
		tx         *PendingTx
	}

	entries := make([]sortEntry, len(txs))
	for i := range txs {
		entries[i].tx = &txs[i]
		entries[i].accountKey = computeAccountKey(txs[i].Account, salt)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		cmp := bytes.Compare(entries[i].accountKey[:], entries[j].accountKey[:])
		if cmp != 0 {
			return cmp < 0
		}
		if entries[i].tx.Sequence != entries[j].tx.Sequence {
			return entries[i].tx.Sequence < entries[j].tx.Sequence
		}
		return bytes.Compare(entries[i].tx.Hash[:], entries[j].tx.Hash[:]) < 0
	})

	sorted := make([]PendingTx, len(txs))
	for i, e := range entries {
		sorted[i] = *e.tx
	}
	copy(txs, sorted)
}

// computeAccountKey computes the sort key for an account.
// Mirrors rippled: copy 20-byte account into 32-byte uint256, then XOR with salt.
// Reference: rippled CanonicalTXSet::accountKey()
func computeAccountKey(account [20]byte, salt [32]byte) [32]byte {
	var key [32]byte
	copy(key[:20], account[:])
	for i := 0; i < 32; i++ {
		key[i] ^= salt[i]
	}
	return key
}

// ComputeSalt builds the RCLTxSet SHAMap (TypeTransaction, tnTRANSACTION_NM
// keyed by tx hash) and returns its root — the value rippled passes as the
// CanonicalTXSet salt on the consensus-build path. Insertion-order-
// independent. Returns the zero hash on construction failure;
// CanonicalSort remains deterministic via (sequence, txID) tiebreakers.
// Reference: rippled RCLConsensus.cpp:512, RCLCxTx.h:62-90.
func ComputeSalt(txs []PendingTx) [32]byte {
	txMap, err := shamap.New(shamap.TypeTransaction)
	if err != nil {
		return [32]byte{}
	}
	for _, ptx := range txs {
		_ = txMap.PutWithNodeType(ptx.Hash, ptx.Blob, shamap.NodeTypeTransactionNoMeta)
	}
	hash, err := txMap.Hash()
	if err != nil {
		return [32]byte{}
	}
	return hash
}
