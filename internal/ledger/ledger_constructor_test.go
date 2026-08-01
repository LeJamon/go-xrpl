package ledger

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

type constructorRecordingFamily struct {
	mu      sync.Mutex
	nodes   map[[32]byte][]byte
	entries []shamap.FlushEntry
}

func newConstructorRecordingFamily() *constructorRecordingFamily {
	return &constructorRecordingFamily{nodes: make(map[[32]byte][]byte)}
}

func (f *constructorRecordingFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return bytes.Clone(f.nodes[hash]), nil
}

func (f *constructorRecordingFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range entries {
		f.entries = append(f.entries, entry)
		f.nodes[entry.Hash] = bytes.Clone(entry.Data)
	}
	return nil
}

func (f *constructorRecordingFamily) takeEntries() []shamap.FlushEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries := append([]shamap.FlushEntry(nil), f.entries...)
	f.entries = nil
	return entries
}

func TestLedgerConstructorsRejectNilAndWrongMapTypes(t *testing.T) {
	constructors := map[string]func(*shamap.SHAMap, *shamap.SHAMap) (*Ledger, error){
		"genesis": func(stateMap, txMap *shamap.SHAMap) (*Ledger, error) {
			return FromGenesis(header.LedgerHeader{}, stateMap, txMap, drops.Fees{})
		},
		"header": func(stateMap, txMap *shamap.SHAMap) (*Ledger, error) {
			return NewFromHeader(header.LedgerHeader{}, stateMap, txMap, drops.Fees{})
		},
		"open": func(stateMap, txMap *shamap.SHAMap) (*Ledger, error) {
			return NewOpenWithHeader(header.LedgerHeader{}, stateMap, txMap, drops.Fees{})
		},
	}
	cases := map[string]func() (*shamap.SHAMap, *shamap.SHAMap){
		"nil state": func() (*shamap.SHAMap, *shamap.SHAMap) {
			return nil, shamap.New(shamap.TypeTransaction)
		},
		"nil transaction": func() (*shamap.SHAMap, *shamap.SHAMap) {
			return shamap.New(shamap.TypeState), nil
		},
		"transaction as state": func() (*shamap.SHAMap, *shamap.SHAMap) {
			return shamap.New(shamap.TypeTransaction), shamap.New(shamap.TypeTransaction)
		},
		"state as transaction": func() (*shamap.SHAMap, *shamap.SHAMap) {
			return shamap.New(shamap.TypeState), shamap.New(shamap.TypeState)
		},
		"swapped": func() (*shamap.SHAMap, *shamap.SHAMap) {
			return shamap.New(shamap.TypeTransaction), shamap.New(shamap.TypeState)
		},
	}

	for constructorName, construct := range constructors {
		for caseName, maps := range cases {
			t.Run(constructorName+"/"+caseName, func(t *testing.T) {
				stateMap, txMap := maps()
				if _, err := construct(stateMap, txMap); err == nil {
					t.Fatal("constructor accepted invalid maps")
				}
			})
		}
	}
}

func TestFromGenesisRejectsMalformedAmendments(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	if err := stateMap.Put(keylet.Amendments().Key, bytes.Repeat([]byte{0xff}, 16)); err != nil {
		t.Fatalf("seed malformed Amendments: %v", err)
	}
	if _, err := FromGenesis(
		header.LedgerHeader{},
		stateMap,
		shamap.New(shamap.TypeTransaction),
		drops.Fees{},
	); err == nil {
		t.Fatal("FromGenesis accepted malformed Amendments")
	}
}

func TestNewOpenWithHeaderDetachesReplayInputs(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	txMap := shamap.New(shamap.TypeTransaction)
	stateKey := mutAcct(0x51)
	originalState := mutData(0x11)
	if err := stateMap.Put(stateKey.Key, originalState); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	var txID [32]byte
	txID[0] = 0x61
	originalTx := bytes.Repeat([]byte{0x21}, 16)
	if err := txMap.Put(txID, originalTx); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}

	l, err := NewOpenWithHeader(
		header.LedgerHeader{LedgerIndex: 8, Drops: 1_000},
		stateMap,
		txMap,
		drops.Fees{},
	)
	if err != nil {
		t.Fatalf("NewOpenWithHeader: %v", err)
	}

	if err := stateMap.Put(stateKey.Key, mutData(0x22)); err != nil {
		t.Fatalf("mutate caller state map: %v", err)
	}
	callerOnly := mutAcct(0x52)
	if err := stateMap.Put(callerOnly.Key, mutData(0x33)); err != nil {
		t.Fatalf("add caller-only state: %v", err)
	}
	var callerTx [32]byte
	callerTx[0] = 0x62
	if err := txMap.Put(callerTx, bytes.Repeat([]byte{0x31}, 16)); err != nil {
		t.Fatalf("add caller-only transaction: %v", err)
	}
	if err := stateMap.SetImmutable(); err != nil {
		t.Fatalf("freeze caller state map: %v", err)
	}
	if err := txMap.SetImmutable(); err != nil {
		t.Fatalf("freeze caller transaction map: %v", err)
	}

	if got, err := l.Read(stateKey); err != nil || !bytes.Equal(got, originalState) {
		t.Fatalf("constructed state after caller mutation = %x, %v; want %x", got, err, originalState)
	}
	if exists, err := l.Exists(callerOnly); err != nil || exists {
		t.Fatalf("caller-only state leaked into ledger: exists=%v err=%v", exists, err)
	}
	if exists, err := l.TxExists(callerTx); err != nil || exists {
		t.Fatalf("caller-only transaction leaked into ledger: exists=%v err=%v", exists, err)
	}
	if got, found, err := l.GetTransaction(txID); err != nil || !found || !bytes.Equal(got, originalTx) {
		t.Fatalf("constructed transaction = %x, %v, %v; want %x", got, found, err, originalTx)
	}
	if err := l.Insert(mutAcct(0x53), mutData(0x44)); err != nil {
		t.Fatalf("detached ledger state is not writable: %v", err)
	}
	if err := l.AddTransaction([32]byte{0x63}, bytes.Repeat([]byte{0x41}, 16)); err != nil {
		t.Fatalf("detached ledger transaction map is not writable: %v", err)
	}
}

func TestHeaderConstructorsSnapshotAtHeaderSequenceWithoutMutatingInputs(t *testing.T) {
	constructors := map[string]func(header.LedgerHeader, *shamap.SHAMap, *shamap.SHAMap) (*Ledger, error){
		"closed": func(hdr header.LedgerHeader, stateMap, txMap *shamap.SHAMap) (*Ledger, error) {
			return NewFromHeader(hdr, stateMap, txMap, drops.Fees{})
		},
		"open": func(hdr header.LedgerHeader, stateMap, txMap *shamap.SHAMap) (*Ledger, error) {
			return NewOpenWithHeader(hdr, stateMap, txMap, drops.Fees{})
		},
	}

	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			family := newConstructorRecordingFamily()
			stateMap, err := shamap.NewBacked(shamap.TypeState, family)
			if err != nil {
				t.Fatalf("NewBacked state: %v", err)
			}
			txMap, err := shamap.NewBacked(shamap.TypeTransaction, family)
			if err != nil {
				t.Fatalf("NewBacked transaction: %v", err)
			}
			stateMap.SetLedgerSeq(11)
			txMap.SetLedgerSeq(12)
			if err := stateMap.Put(mutAcct(0x71).Key, mutData(0x11)); err != nil {
				t.Fatalf("seed state: %v", err)
			}
			if err := txMap.Put([32]byte{0x72}, bytes.Repeat([]byte{0x22}, 16)); err != nil {
				t.Fatalf("seed transaction: %v", err)
			}

			const targetSeq = uint32(600)
			if _, err := construct(header.LedgerHeader{LedgerIndex: targetSeq}, stateMap, txMap); err != nil {
				t.Fatalf("construct ledger: %v", err)
			}
			entries := family.takeEntries()
			if len(entries) == 0 {
				t.Fatal("constructor did not flush dirty inputs")
			}
			seenTypes := make(map[shamap.Type]bool)
			for _, entry := range entries {
				if entry.LedgerSeq != targetSeq {
					t.Fatalf("constructor flush ledger sequence = %d, want %d", entry.LedgerSeq, targetSeq)
				}
				seenTypes[entry.MapType] = true
			}
			if !seenTypes[shamap.TypeState] || !seenTypes[shamap.TypeTransaction] {
				t.Fatalf("constructor flush map types = %v, want state and transaction", seenTypes)
			}

			if err := stateMap.Put(mutAcct(0x73).Key, mutData(0x33)); err != nil {
				t.Fatalf("mutate caller state: %v", err)
			}
			if err := txMap.Put([32]byte{0x74}, bytes.Repeat([]byte{0x44}, 16)); err != nil {
				t.Fatalf("mutate caller transaction: %v", err)
			}
			if _, err := stateMap.SnapshotImmutable(); err != nil {
				t.Fatalf("snapshot caller state: %v", err)
			}
			if _, err := txMap.SnapshotImmutable(); err != nil {
				t.Fatalf("snapshot caller transaction: %v", err)
			}
			entries = family.takeEntries()
			if len(entries) == 0 {
				t.Fatal("caller mutations did not flush")
			}
			for _, entry := range entries {
				want := uint32(11)
				if entry.MapType == shamap.TypeTransaction {
					want = 12
				}
				if entry.LedgerSeq != want {
					t.Fatalf("caller %s flush ledger sequence = %d, want %d", entry.MapType, entry.LedgerSeq, want)
				}
			}
		})
	}
}
