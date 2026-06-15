package service

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// newHistoricalLedger builds a closed, hash-addressable historical ledger at
// the given sequence carrying a single account root, then registers it so
// getLedgerForQuery can resolve it by hash. It returns the ledger's hash hex.
func newHistoricalLedger(t *testing.T, svc *Service, seq uint32, addr string, id [20]byte, balance uint64) string {
	t.Helper()
	stateMap, err := svc.genesisLedger.StateMapSnapshot()
	if err != nil {
		t.Fatalf("StateMapSnapshot: %v", err)
	}
	txMap, err := svc.genesisLedger.TxMapSnapshot()
	if err != nil {
		t.Fatalf("TxMapSnapshot: %v", err)
	}
	var h header.LedgerHeader
	h.LedgerIndex = seq
	hist := ledger.NewOpenWithHeader(h, stateMap, txMap, drops.Fees{})

	root := &state.AccountRoot{Account: addr, Balance: balance, Sequence: 1}
	data, err := state.SerializeAccountRoot(root)
	if err != nil {
		t.Fatalf("serialize account root: %v", err)
	}
	if err := hist.Insert(keylet.Account(id), data); err != nil {
		t.Fatalf("insert account root into history: %v", err)
	}

	svc.mu.Lock()
	svc.putHistoryLocked(hist)
	svc.mu.Unlock()

	hHash := hist.Hash()
	return hex.EncodeToString(hHash[:])
}

// TestGetAccountInfo_ByHash proves a ledger_hash selector resolves the specific
// named historical ledger — with that ledger's own account state, sequence, and
// hash — rather than collapsing to the latest validated ledger. Regression for
// the M1 finding on PR #870 (ledger_hash not threaded to the service layer).
func TestGetAccountInfo_ByHash(t *testing.T) {
	svc := newOfferTestService(t)
	addr, id := addressFromBytes(t, 0x10)

	// Open (current) ledger holds the account at one balance...
	insertAccountRoot(t, svc, addr, 250_000_000, 0)

	// ...the historical ledger holds it at a *different* balance, so a correct
	// by-hash resolution is observable in the returned data.
	const histSeq = uint32(2)
	const histBalance = uint64(777_000_000)
	hashHex := newHistoricalLedger(t, svc, histSeq, addr, id, histBalance)

	t.Run("resolves the named ledger's data", func(t *testing.T) {
		info, err := svc.GetAccountInfo(context.Background(), addr, hashHex)
		if err != nil {
			t.Fatalf("GetAccountInfo by hash: %v", err)
		}
		if info.Balance != histBalance {
			t.Errorf("balance = %d, want %d (historical, not current)", info.Balance, histBalance)
		}
		if info.LedgerIndex != histSeq {
			t.Errorf("ledger_index = %d, want %d", info.LedgerIndex, histSeq)
		}
		gotHash := hex.EncodeToString(info.LedgerHash[:])
		if gotHash != hashHex {
			t.Errorf("ledger_hash = %s, want %s", gotHash, hashHex)
		}
	})

	t.Run("unknown hash yields ErrLedgerNotFound", func(t *testing.T) {
		var miss [32]byte
		miss[0] = 0xAB
		_, err := svc.GetAccountInfo(context.Background(), addr, hex.EncodeToString(miss[:]))
		if !errors.Is(err, ErrLedgerNotFound) {
			t.Fatalf("want ErrLedgerNotFound, got %v", err)
		}
	})

	t.Run("malformed 64-char hash", func(t *testing.T) {
		bad := make([]byte, 64)
		for i := range bad {
			bad[i] = 'z'
		}
		_, err := svc.GetAccountInfo(context.Background(), addr, string(bad))
		if !errors.Is(err, ErrInvalidLedgerHash) {
			t.Fatalf("want ErrInvalidLedgerHash, got %v", err)
		}
	})
}
