package ledger

import (
	"bytes"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

func TestTransactionInsertionIsAddOnly(t *testing.T) {
	type addFunc func(*Ledger, [32]byte, []byte) error
	raw := func(l *Ledger, id [32]byte, data []byte) error {
		return l.AddTransaction(id, data)
	}
	withMeta := func(l *Ledger, id [32]byte, data []byte) error {
		return l.AddTransactionWithMeta(id, data)
	}
	cases := []struct {
		name      string
		first     addFunc
		duplicate addFunc
	}{
		{name: "raw then raw", first: raw, duplicate: raw},
		{name: "raw then metadata", first: raw, duplicate: withMeta},
		{name: "metadata then metadata", first: withMeta, duplicate: withMeta},
		{name: "metadata then raw", first: withMeta, duplicate: raw},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newOpenChild(t)
			original := bytes.Repeat([]byte{byte(0x10 + i)}, 16)
			replacement := bytes.Repeat([]byte{byte(0x80 + i)}, 16)
			txID := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), original)
			if err := tc.first(l, txID, original); err != nil {
				t.Fatalf("first insertion: %v", err)
			}
			beforeRoot, err := l.TxMapHash()
			if err != nil {
				t.Fatalf("transaction root before duplicate: %v", err)
			}

			if err := tc.duplicate(l, txID, replacement); !errors.Is(err, ErrEntryExists) {
				t.Fatalf("duplicate insertion error = %v, want ErrEntryExists", err)
			}
			if got, found, err := l.GetTransaction(txID); err != nil || !found || !bytes.Equal(got, original) {
				t.Fatalf("transaction after duplicate = %x, %v, %v; want %x", got, found, err, original)
			}
			if got, err := l.TxMapHash(); err != nil || got != beforeRoot {
				t.Fatalf("transaction root after duplicate = %x, %v; want %x", got, err, beforeRoot)
			}
			if got := l.TxCount(); got != 1 {
				t.Fatalf("transaction count after duplicate = %d, want 1", got)
			}
		})
	}
}

func TestTxExistsPreservesBackingStoreFailure(t *testing.T) {
	var txID [32]byte
	txID[0] = 0xc1
	source := shamap.New(shamap.TypeTransaction)
	if err := source.PutWithNodeType(txID, bytes.Repeat([]byte{0x31}, 16), shamap.NodeTypeTransactionWithMeta); err != nil {
		t.Fatalf("seed transaction map: %v", err)
	}
	family := newLifecycleMemoryFamily()
	lazyTx := lifecycleLazyMap(t, source, family)

	store, _ := executionTestStore(t, family)
	parent := newOpenChildWithStore(t, store)
	stateMap, err := parent.StateMapSnapshot()
	if err != nil {
		t.Fatalf("state snapshot: %v", err)
	}
	l, err := NewOpenWithHeader(parent.Header(), stateMap, lazyTx, drops.Fees{}, store)
	if err != nil {
		t.Fatalf("NewOpenWithHeader: %v", err)
	}
	wantErr := errors.New("injected transaction membership failure")
	family.setFetchError(wantErr)
	exists, err := l.TxExists(txID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("TxExists error = %v, want %v", err, wantErr)
	}
	if exists {
		t.Fatal("TxExists reported presence alongside a lookup failure")
	}

	family.setFetchError(nil)
	exists, err = l.TxExists(txID)
	if err != nil || !exists {
		t.Fatalf("TxExists after recovery = %v, %v; want true, nil", exists, err)
	}
}
