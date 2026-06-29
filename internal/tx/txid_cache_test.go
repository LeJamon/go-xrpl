package tx

import "testing"

// TestComputeTransactionHash_MemoizesRawBytes pins the txid memoisation: once
// the raw signed bytes are present the id is computed once and reused, so the
// repeated under-lock ComputeTransactionHash calls on the apply strand become
// O(1) cache hits instead of re-hashing the blob (issue #1137).
func TestComputeTransactionHash_MemoizesRawBytes(t *testing.T) {
	blob := encodeTx(t, baseCommon("AccountSet"))

	txn, err := ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}

	c := txn.GetCommon()
	if c.txIDCached {
		t.Fatalf("txid must not be cached before the first ComputeTransactionHash")
	}

	h1, err := ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash: %v", err)
	}

	// The cached value is the canonical txid: SHA512Half(TXN-prefix || blob).
	if want := hashWithTxnPrefix(blob); h1 != want {
		t.Fatalf("txid = %x, want %x", h1, want)
	}
	if !c.txIDCached || c.cachedTxID != h1 {
		t.Fatalf("first compute must memoise the txid (cached=%v, value=%x, want %x)",
			c.txIDCached, c.cachedTxID, h1)
	}

	// A second compute on the same object — the open-ledger apply strand reusing
	// the parsed tx — returns the identical id without re-hashing.
	h2, err := ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash (2nd): %v", err)
	}
	if h2 != h1 {
		t.Fatalf("memoised txid diverged: %x != %x", h2, h1)
	}
}

// TestComputeTransactionHash_SetRawBytesInvalidates guards the invalidation
// contract: the id tracks the current raw bytes, so SetRawBytes must drop a
// stale cache rather than return the previous transaction's id.
func TestComputeTransactionHash_SetRawBytesInvalidates(t *testing.T) {
	blob1 := encodeTx(t, baseCommon("AccountSet"))

	fields2 := baseCommon("AccountSet")
	fields2["Sequence"] = uint32(2)
	blob2 := encodeTx(t, fields2)

	txn, err := ParseFromBinary(blob1)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}

	h1, err := ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash(blob1): %v", err)
	}

	// Different bytes must yield a different id once the cache is invalidated.
	txn.SetRawBytes(blob2)
	if txn.GetCommon().txIDCached {
		t.Fatalf("SetRawBytes must invalidate the memoised txid")
	}
	h2, err := ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash(blob2): %v", err)
	}
	if h2 == h1 {
		t.Fatalf("txid did not change after SetRawBytes: still %x", h1)
	}
	if want := hashWithTxnPrefix(blob2); h2 != want {
		t.Fatalf("post-invalidation txid = %x, want %x", h2, want)
	}

	// Restoring the original bytes recomputes the original id, not a stale h2.
	txn.SetRawBytes(blob1)
	h3, err := ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash(blob1 restored): %v", err)
	}
	if h3 != h1 {
		t.Fatalf("restored txid = %x, want original %x", h3, h1)
	}
}

// TestComputeTransactionHash_FlattenPathUnmemoised verifies the no-raw-bytes
// fallback still hashes from current field state — and must NOT memoise, since
// without raw bytes the blob is rebuilt from mutable fields each call (the
// Flatten branch must survive the memoisation refactor unchanged).
func TestComputeTransactionHash_FlattenPathUnmemoised(t *testing.T) {
	blob := encodeTx(t, baseCommon("AccountSet"))

	txn, err := ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary: %v", err)
	}

	// Drop the raw bytes: ComputeTransactionHash must fall back to Flatten,
	// producing a stable non-zero id with nothing memoised.
	txn.SetRawBytes(nil)
	h1, err := ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash(flatten): %v", err)
	}
	if h1 == ([32]byte{}) {
		t.Fatalf("flatten-path txid must not be zero")
	}
	if txn.GetCommon().txIDCached {
		t.Fatalf("the Flatten path must not memoise (no raw bytes to key on)")
	}

	h2, err := ComputeTransactionHash(txn)
	if err != nil {
		t.Fatalf("ComputeTransactionHash(flatten 2nd): %v", err)
	}
	if h2 != h1 {
		t.Fatalf("flatten-path txid not deterministic: %x != %x", h2, h1)
	}
}
