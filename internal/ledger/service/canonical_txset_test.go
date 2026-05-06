package service

import (
	"bytes"
	"testing"

	"github.com/LeJamon/goXRPLd/crypto/common"
)

func TestCanonicalSortEmpty(t *testing.T) {
	var txs []pendingTx
	canonicalSort(txs)
	if len(txs) != 0 {
		t.Error("expected empty slice after sorting empty input")
	}
}

func TestCanonicalSortSingle(t *testing.T) {
	txs := []pendingTx{
		{hash: [32]byte{0x01}, account: [20]byte{0xAA}, seqProxy: 1},
	}
	canonicalSort(txs)
	if txs[0].hash[0] != 0x01 {
		t.Error("single element should remain unchanged")
	}
}

func TestCanonicalSortByAccountKey(t *testing.T) {
	// Two transactions from different accounts, same sequence.
	// After XOR with salt, the ordering should be deterministic.
	account1 := [20]byte{0x01}
	account2 := [20]byte{0xFF}

	txs := []pendingTx{
		{txBlob: []byte{1}, hash: makeHash(1), account: account2, seqProxy: 1},
		{txBlob: []byte{2}, hash: makeHash(2), account: account1, seqProxy: 1},
	}

	canonicalSort(txs)

	// The salt is computed from the sorted hashes, so account keys depend on it.
	// We just verify the sort is stable and deterministic.
	salt := computeSalt(txs)
	key1 := computeAccountKey(account1, salt)
	key2 := computeAccountKey(account2, salt)

	cmp := bytes.Compare(key1[:], key2[:])
	if cmp < 0 {
		// account1 should come first
		if txs[0].account != account1 {
			t.Error("expected account1 first when its key is smaller")
		}
	} else if cmp > 0 {
		// account2 should come first
		if txs[0].account != account2 {
			t.Error("expected account2 first when its key is smaller")
		}
	}
	// If cmp == 0 (extremely unlikely), it falls through to sequence/hash comparison
}

func TestCanonicalSortBySequence(t *testing.T) {
	// Same account, different sequences
	account := [20]byte{0x42}
	txs := []pendingTx{
		{txBlob: []byte{1}, hash: makeHash(3), account: account, seqProxy: 10},
		{txBlob: []byte{2}, hash: makeHash(1), account: account, seqProxy: 5},
		{txBlob: []byte{3}, hash: makeHash(2), account: account, seqProxy: 8},
	}

	canonicalSort(txs)

	// Same account => sorted by sequence
	if txs[0].seqProxy != 5 {
		t.Errorf("expected sequence 5 first, got %d", txs[0].seqProxy)
	}
	if txs[1].seqProxy != 8 {
		t.Errorf("expected sequence 8 second, got %d", txs[1].seqProxy)
	}
	if txs[2].seqProxy != 10 {
		t.Errorf("expected sequence 10 third, got %d", txs[2].seqProxy)
	}
}

func TestCanonicalSortSeqBeforeTicket(t *testing.T) {
	// Same account: a sequence-based txn must sort before a ticket-based txn
	// regardless of numeric value, matching rippled's SeqProxy::operator<.
	// This guarantees that ticket-creating txns (sequence-based) sort before
	// ticket-consuming txns (ticket-based) and that an account-creating txn
	// is applied before a batch that uses an earlier-issued ticket.
	account := [20]byte{0x42}
	const ticketBit = uint64(1) << 32
	txs := []pendingTx{
		{txBlob: []byte{1}, hash: makeHash(1), account: account, seqProxy: ticketBit | 6}, // ticket value 6
		{txBlob: []byte{2}, hash: makeHash(2), account: account, seqProxy: 16},             // sequence value 16
	}

	canonicalSort(txs)

	if txs[0].seqProxy != 16 {
		t.Errorf("expected sequence-based (16) first, got seqProxy=%#x", txs[0].seqProxy)
	}
	if txs[1].seqProxy != ticketBit|6 {
		t.Errorf("expected ticket-based (6) second, got seqProxy=%#x", txs[1].seqProxy)
	}
}

func TestCanonicalSortByTxID(t *testing.T) {
	// Same account, same sequence, different hashes
	account := [20]byte{0x42}
	hash1 := [32]byte{0x01}
	hash2 := [32]byte{0x02}
	hash3 := [32]byte{0x03}

	txs := []pendingTx{
		{txBlob: []byte{1}, hash: hash3, account: account, seqProxy: 1},
		{txBlob: []byte{2}, hash: hash1, account: account, seqProxy: 1},
		{txBlob: []byte{3}, hash: hash2, account: account, seqProxy: 1},
	}

	canonicalSort(txs)

	// Same account, same sequence => sorted by txID (hash)
	if txs[0].hash != hash1 {
		t.Errorf("expected hash1 first, got %x", txs[0].hash[:4])
	}
	if txs[1].hash != hash2 {
		t.Errorf("expected hash2 second, got %x", txs[1].hash[:4])
	}
	if txs[2].hash != hash3 {
		t.Errorf("expected hash3 third, got %x", txs[2].hash[:4])
	}
}

func TestCanonicalSortDeterministic(t *testing.T) {
	// Sorting the same set twice should produce the same result
	makeTxs := func() []pendingTx {
		return []pendingTx{
			{txBlob: []byte{1}, hash: makeHash(5), account: [20]byte{0xAA}, seqProxy: 3},
			{txBlob: []byte{2}, hash: makeHash(2), account: [20]byte{0xBB}, seqProxy: 1},
			{txBlob: []byte{3}, hash: makeHash(8), account: [20]byte{0xCC}, seqProxy: 2},
			{txBlob: []byte{4}, hash: makeHash(1), account: [20]byte{0xAA}, seqProxy: 1},
		}
	}

	txs1 := makeTxs()
	txs2 := makeTxs()

	canonicalSort(txs1)
	canonicalSort(txs2)

	for i := range txs1 {
		if txs1[i].hash != txs2[i].hash {
			t.Errorf("sort not deterministic at index %d: %x vs %x",
				i, txs1[i].hash[:4], txs2[i].hash[:4])
		}
	}
}

func TestComputeSalt(t *testing.T) {
	// SHAMap leaf nodes require >= 12 bytes of data
	blob1 := make([]byte, 16)
	blob1[0] = 0x01
	blob2 := make([]byte, 16)
	blob2[0] = 0x02

	txs := []pendingTx{
		{hash: makeHash(2), txBlob: blob2},
		{hash: makeHash(1), txBlob: blob1},
	}

	salt := computeSalt(txs)
	var zero [32]byte
	if salt == zero {
		t.Error("salt should not be zero")
	}

	// Verify it's deterministic regardless of input order
	txsReversed := []pendingTx{
		{hash: makeHash(1), txBlob: blob1},
		{hash: makeHash(2), txBlob: blob2},
	}
	saltReversed := computeSalt(txsReversed)
	if salt != saltReversed {
		t.Error("salt should be the same regardless of input order")
	}
}

func TestComputeAccountKey(t *testing.T) {
	account := [20]byte{0xFF, 0x00, 0xAA}
	salt := [32]byte{0x11, 0x22, 0x33}

	key := computeAccountKey(account, salt)

	// Verify XOR: first 20 bytes = account XOR salt[:20]
	if key[0] != 0xFF^0x11 {
		t.Errorf("expected %x, got %x", 0xFF^0x11, key[0])
	}
	if key[1] != 0x00^0x22 {
		t.Errorf("expected %x, got %x", 0x00^0x22, key[1])
	}
	if key[2] != 0xAA^0x33 {
		t.Errorf("expected %x, got %x", 0xAA^0x33, key[2])
	}

	// Bytes beyond account (20-31) should be salt only (since account is zero-padded)
	if key[20] != salt[20] {
		t.Errorf("byte 20: expected %x, got %x", salt[20], key[20])
	}
}

// makeHash creates a deterministic hash from a seed byte using SHA-512Half
func makeHash(seed byte) [32]byte {
	return common.Sha512Half([]byte{seed})
}
