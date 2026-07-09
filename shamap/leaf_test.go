package shamap

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
)

// TestNewLeafNode_RejectsShortPayload pins rippled's minSHAMapItemBytes
// (SHAMapTreeNode.h): a leaf whose item payload is under 12 bytes is
// rejected across all three leaf kinds. The bound is on the payload
// itself — for keyed kinds that is the data after the 32-byte key/tag is
// stripped (matching rippled's post-`s.chop(tag)` check).
func TestNewLeafNode_RejectsShortPayload(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1) // non-zero: account-state rejects a zero key
	}

	ctors := []struct {
		name string
		fn   func(*Item) (*leafNode, error)
	}{
		{"account_state", newAccountStateLeafNode},
		{"transaction", newTransactionLeafNode},
		{"transaction_with_meta", newTransactionWithMetaLeafNode},
	}

	for _, c := range ctors {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.fn(NewItem(key, make([]byte, 11))); !errors.Is(err, ErrItemTooSmall) {
				t.Fatalf("11-byte payload: want ErrItemTooSmall, got %v", err)
			}
			if _, err := c.fn(NewItem(key, make([]byte, 12))); err != nil {
				t.Fatalf("12-byte payload: unexpected error %v", err)
			}
		})
	}
}

// TestLeafFromWire_RejectsShortPayload exercises the wire-deserialization
// path actually hit by peer-received SHAMap nodes: the 12-byte minimum is
// measured after the trailing key/tag is stripped, so a node with 11 data
// bytes plus a full key is still rejected as too short.
func TestLeafFromWire_RejectsShortPayload(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	// account-state wire = stateData | key(32) | wireType(1)
	buildAS := func(dataLen int) []byte {
		w := make([]byte, 0, dataLen+33)
		w = append(w, make([]byte, dataLen)...)
		w = append(w, key...)
		return append(w, byte(protocol.WireTypeAccountState))
	}
	if _, err := newAccountStateLeafFromWire(buildAS(11)); !errors.Is(err, ErrItemTooSmall) {
		t.Fatalf("AS 11-byte payload: want ErrItemTooSmall, got %v", err)
	}
	if _, err := newAccountStateLeafFromWire(buildAS(12)); err != nil {
		t.Fatalf("AS 12-byte payload: unexpected error %v", err)
	}

	// transaction (no meta) wire = txData | wireType(1)
	buildTxn := func(dataLen int) []byte {
		return append(make([]byte, dataLen), byte(protocol.WireTypeTransaction))
	}
	if _, err := NewTransactionLeafFromWire(buildTxn(11)); !errors.Is(err, ErrItemTooSmall) {
		t.Fatalf("TXN 11-byte payload: want ErrItemTooSmall, got %v", err)
	}
	if _, err := NewTransactionLeafFromWire(buildTxn(12)); err != nil {
		t.Fatalf("TXN 12-byte payload: unexpected error %v", err)
	}
}
