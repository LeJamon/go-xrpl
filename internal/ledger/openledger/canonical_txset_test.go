package openledger

import (
	"bytes"
	"testing"

	"github.com/LeJamon/goXRPLd/crypto/common"
)

func TestCanonicalSortEmpty(t *testing.T) {
	var txs []PendingTx
	CanonicalSort(txs, [32]byte{})
	if len(txs) != 0 {
		t.Error("expected empty slice after sorting empty input")
	}
}

func TestCanonicalSortSingle(t *testing.T) {
	txs := []PendingTx{
		{Hash: [32]byte{0x01}, Account: [20]byte{0xAA}, Sequence: 1},
	}
	CanonicalSort(txs, [32]byte{})
	if txs[0].Hash[0] != 0x01 {
		t.Error("single element should remain unchanged")
	}
}

func TestCanonicalSortByAccountKey(t *testing.T) {
	// Two transactions from different accounts, same sequence.
	// After XOR with salt, the ordering should be deterministic.
	account1 := [20]byte{0x01}
	account2 := [20]byte{0xFF}

	txs := []PendingTx{
		{Blob: []byte{1}, Hash: makeHash(1), Account: account2, Sequence: 1},
		{Blob: []byte{2}, Hash: makeHash(2), Account: account1, Sequence: 1},
	}

	CanonicalSort(txs, [32]byte{})

	salt := [32]byte{}
	key1 := computeAccountKey(account1, salt)
	key2 := computeAccountKey(account2, salt)

	cmp := bytes.Compare(key1[:], key2[:])
	if cmp < 0 {
		if txs[0].Account != account1 {
			t.Error("expected account1 first when its key is smaller")
		}
	} else if cmp > 0 {
		if txs[0].Account != account2 {
			t.Error("expected account2 first when its key is smaller")
		}
	}
}

func TestCanonicalSortBySequence(t *testing.T) {
	account := [20]byte{0x42}
	txs := []PendingTx{
		{Blob: []byte{1}, Hash: makeHash(3), Account: account, Sequence: 10},
		{Blob: []byte{2}, Hash: makeHash(1), Account: account, Sequence: 5},
		{Blob: []byte{3}, Hash: makeHash(2), Account: account, Sequence: 8},
	}

	CanonicalSort(txs, [32]byte{})

	if txs[0].Sequence != 5 {
		t.Errorf("expected sequence 5 first, got %d", txs[0].Sequence)
	}
	if txs[1].Sequence != 8 {
		t.Errorf("expected sequence 8 second, got %d", txs[1].Sequence)
	}
	if txs[2].Sequence != 10 {
		t.Errorf("expected sequence 10 third, got %d", txs[2].Sequence)
	}
}

func TestCanonicalSortByTxID(t *testing.T) {
	account := [20]byte{0x42}
	hash1 := [32]byte{0x01}
	hash2 := [32]byte{0x02}
	hash3 := [32]byte{0x03}

	txs := []PendingTx{
		{Blob: []byte{1}, Hash: hash3, Account: account, Sequence: 1},
		{Blob: []byte{2}, Hash: hash1, Account: account, Sequence: 1},
		{Blob: []byte{3}, Hash: hash2, Account: account, Sequence: 1},
	}

	CanonicalSort(txs, [32]byte{})

	if txs[0].Hash != hash1 {
		t.Errorf("expected hash1 first, got %x", txs[0].Hash[:4])
	}
	if txs[1].Hash != hash2 {
		t.Errorf("expected hash2 second, got %x", txs[1].Hash[:4])
	}
	if txs[2].Hash != hash3 {
		t.Errorf("expected hash3 third, got %x", txs[2].Hash[:4])
	}
}

func TestCanonicalSortDeterministic(t *testing.T) {
	makeTxs := func() []PendingTx {
		return []PendingTx{
			{Blob: []byte{1}, Hash: makeHash(5), Account: [20]byte{0xAA}, Sequence: 3},
			{Blob: []byte{2}, Hash: makeHash(2), Account: [20]byte{0xBB}, Sequence: 1},
			{Blob: []byte{3}, Hash: makeHash(8), Account: [20]byte{0xCC}, Sequence: 2},
			{Blob: []byte{4}, Hash: makeHash(1), Account: [20]byte{0xAA}, Sequence: 1},
		}
	}

	txs1 := makeTxs()
	txs2 := makeTxs()

	CanonicalSort(txs1, [32]byte{})
	CanonicalSort(txs2, [32]byte{})

	for i := range txs1 {
		if txs1[i].Hash != txs2[i].Hash {
			t.Errorf("sort not deterministic at index %d: %x vs %x",
				i, txs1[i].Hash[:4], txs2[i].Hash[:4])
		}
	}
}

func TestComputeSaltOrderIndependent(t *testing.T) {
	mkBlob := func(seed byte) []byte {
		b := make([]byte, 16)
		for i := range b {
			b[i] = seed + byte(i)
		}
		return b
	}
	txs := []PendingTx{
		{Blob: mkBlob(0x10), Hash: makeHash(1), Account: [20]byte{0xAA}, Sequence: 1},
		{Blob: mkBlob(0x20), Hash: makeHash(2), Account: [20]byte{0xAA}, Sequence: 2},
		{Blob: mkBlob(0x30), Hash: makeHash(3), Account: [20]byte{0xAA}, Sequence: 3},
	}

	salt1 := ComputeSalt(txs)

	permuted := []PendingTx{txs[2], txs[0], txs[1]}
	salt2 := ComputeSalt(permuted)
	if salt1 != salt2 {
		t.Errorf("ComputeSalt is order-dependent: %x vs %x", salt1[:8], salt2[:8])
	}

	emptySalt1 := ComputeSalt(nil)
	emptySalt2 := ComputeSalt([]PendingTx{})
	if emptySalt1 != emptySalt2 {
		t.Errorf("ComputeSalt diverges on nil vs empty slice: %x vs %x",
			emptySalt1[:8], emptySalt2[:8])
	}

	other := []PendingTx{
		{Blob: mkBlob(0xFF), Hash: makeHash(99), Account: [20]byte{0xAA}, Sequence: 1},
	}
	if ComputeSalt(other) == salt1 {
		t.Error("ComputeSalt collapsed two different tx sets to the same salt")
	}
}

func TestComputeAccountKey(t *testing.T) {
	account := [20]byte{0xFF, 0x00, 0xAA}
	salt := [32]byte{0x11, 0x22, 0x33}

	key := computeAccountKey(account, salt)

	if key[0] != 0xFF^0x11 {
		t.Errorf("expected %x, got %x", 0xFF^0x11, key[0])
	}
	if key[1] != 0x00^0x22 {
		t.Errorf("expected %x, got %x", 0x00^0x22, key[1])
	}
	if key[2] != 0xAA^0x33 {
		t.Errorf("expected %x, got %x", 0xAA^0x33, key[2])
	}

	if key[20] != salt[20] {
		t.Errorf("byte 20: expected %x, got %x", salt[20], key[20])
	}
}

// makeHash creates a deterministic hash from a seed byte using SHA-512Half
func makeHash(seed byte) [32]byte {
	return common.Sha512Half([]byte{seed})
}
