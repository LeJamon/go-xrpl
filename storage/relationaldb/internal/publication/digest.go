// Package publication contains the bounded, deterministic encoding used by
// relational ledger publication receipts.
package publication

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"slices"

	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// Digest hashes the publication descriptor and each streamed transaction in
// arrival order. Account IDs are sorted within a transaction because their
// database index is a set, while transaction order remains part of the input
// contract (the canonical SHAMap order supplied by the caller).
type Digest struct {
	hash hash.Hash
}

// NewDigest starts a digest for one semantic publication attempt.
func NewDigest(ledger relationaldb.LedgerInfo, commitID [32]byte, publicationKind string) *Digest {
	d := &Digest{hash: sha256.New()}
	d.writeBytes([]byte("go-xrpl/relational-ledger-publication/v1"))
	d.writeBytes(commitID[:])
	d.writeString(publicationKind)
	d.writeBytes(ledger.Hash[:])
	d.writeUint32(uint32(ledger.Sequence))
	d.writeBytes(ledger.ParentHash[:])
	d.writeBytes(ledger.AccountHash[:])
	d.writeBytes(ledger.TransactionHash[:])
	d.writeInt64(int64(ledger.TotalCoins))
	// Ledger headers encode close times as whole XRPL-epoch seconds. Hash the
	// persisted representation so a retry built from a durable header has the
	// same receipt as the original in-memory ledger, even when the latter
	// carried sub-second wall-clock precision.
	d.writeInt64(protocol.RippleSeconds(ledger.CloseTime))
	d.writeInt64(protocol.RippleSeconds(ledger.ParentCloseTime))
	d.writeInt32(ledger.CloseTimeRes)
	d.writeUint32(ledger.CloseFlags)
	return d
}

// Add appends one indexed transaction to the digest.
func (d *Digest) Add(indexed relationaldb.IndexedTransaction) {
	tx := indexed.Transaction
	d.writeBytes(tx.Hash[:])
	d.writeUint32(uint32(tx.LedgerSeq))
	d.writeUint32(tx.TxnSeq)
	d.writeString(tx.Status)
	d.writeBytes(tx.RawTxn)
	d.writeBytes(tx.TxnMeta)

	accounts := slices.Clone(indexed.Accounts)
	slices.SortFunc(accounts, func(a, b relationaldb.AccountID) int {
		for i := range a {
			if a[i] < b[i] {
				return -1
			}
			if a[i] > b[i] {
				return 1
			}
		}
		return 0
	})
	d.writeUint32(uint32(len(accounts)))
	for _, account := range accounts {
		d.writeBytes(account[:])
	}
}

// Sum returns the receipt digest.
func (d *Digest) Sum() [32]byte {
	var result [32]byte
	copy(result[:], d.hash.Sum(nil))
	return result
}

func (d *Digest) writeBytes(value []byte) {
	d.writeUint64(uint64(len(value)))
	_, _ = d.hash.Write(value)
}

func (d *Digest) writeString(value string) {
	d.writeBytes([]byte(value))
}

func (d *Digest) writeUint32(value uint32) {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	_, _ = d.hash.Write(encoded[:])
}

func (d *Digest) writeUint64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = d.hash.Write(encoded[:])
}

func (d *Digest) writeInt32(value int32) {
	d.writeUint32(uint32(value))
}

func (d *Digest) writeInt64(value int64) {
	d.writeUint64(uint64(value))
}
