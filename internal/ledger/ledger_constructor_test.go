package ledger

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

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
