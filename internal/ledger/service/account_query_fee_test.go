package service

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/feetrack"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

func newNoRippleFeeLedger(t *testing.T, svc *Service, seq uint32, account string, accountID [20]byte, baseFee drops.XRPAmount) string {
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
	h.Hash[0] = byte(seq)
	hist, err := ledger.NewOpenWithHeader(h, stateMap, txMap, drops.Fees{
		Base:      baseFee,
		Reserve:   drops.DefaultReserveBase,
		Increment: drops.DefaultReserveIncrement,
	})
	if err != nil {
		t.Fatalf("NewOpenWithHeader: %v", err)
	}

	data, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  account,
		Balance:  1_000_000_000,
		Sequence: 1,
	})
	if err != nil {
		t.Fatalf("serialize account root: %v", err)
	}
	if err := hist.Insert(keylet.Account(accountID), data); err != nil {
		t.Fatalf("insert account root into history: %v", err)
	}
	feeData, err := state.SerializeFeeSettings(&state.FeeSettings{
		BaseFeeDrops:          uint64(baseFee),
		ReserveBaseDrops:      uint64(drops.DefaultReserveBase),
		ReserveIncrementDrops: uint64(drops.DefaultReserveIncrement),
		XRPFeesMode:           true,
	})
	if err != nil {
		t.Fatalf("serialize fee settings: %v", err)
	}
	if err := hist.Update(keylet.Fees(), feeData); err != nil {
		t.Fatalf("update fee settings in history: %v", err)
	}

	svc.mu.Lock()
	svc.putHistoryLocked(hist)
	svc.mu.Unlock()

	hash := hist.Hash()
	return hex.EncodeToString(hash[:])
}

func TestGetNoRippleCheck_SuggestedFeesFollowLoad(t *testing.T) {
	svc := newOfferTestService(t)
	gwAddr, _ := addressFromBytes(t, 0x10)
	peerAddr, _ := addressFromBytes(t, 0x40)
	insertAccountRoot(t, svc, gwAddr, 1_000_000_000, 0)
	insertLineRaw(t, svc, gwAddr, peerAddr, "USD", "0", "100", "100", state.LsfLowNoRipple)

	for _, tc := range []struct {
		name      string
		remoteFee uint32
		wantFee   string
	}{
		{name: "normal load", remoteFee: 256, wantFee: "10"},
		{name: "loaded", remoteFee: 512, wantFee: "20"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc.FeeTrack().SetRemoteFee(tc.remoteFee)
			result, err := svc.GetNoRippleCheck(context.Background(), gwAddr, "gateway", "current", 0, true)
			if err != nil {
				t.Fatalf("GetNoRippleCheck: %v", err)
			}
			if len(result.Transactions) != 2 {
				t.Fatalf("transactions = %d, want 2: %+v", len(result.Transactions), result.Transactions)
			}
			for _, suggested := range result.Transactions {
				if suggested.Fee != tc.wantFee {
					t.Errorf("%s fee = %s, want %s", suggested.TransactionType, suggested.Fee, tc.wantFee)
				}
			}
		})
	}
}

func TestGetNoRippleCheck_UsesSelectedLedgerFee(t *testing.T) {
	svc := newOfferTestService(t)
	account, accountID := addressFromBytes(t, 0x10)
	ledgerHash := newNoRippleFeeLedger(t, svc, 2, account, accountID, 42)

	result, err := svc.GetNoRippleCheck(context.Background(), account, "gateway", ledgerHash, 0, true)
	if err != nil {
		t.Fatalf("GetNoRippleCheck: %v", err)
	}
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1: %+v", len(result.Transactions), result.Transactions)
	}
	if got := result.Transactions[0].Fee; got != "42" {
		t.Errorf("historical fee = %s, want 42", got)
	}
}

func TestGetNoRippleCheck_ClipsSuggestedFeeToJSONInteger(t *testing.T) {
	svc := newOfferTestService(t)
	account, accountID := addressFromBytes(t, 0x20)
	ledgerHash := newNoRippleFeeLedger(t, svc, 3, account, accountID, drops.XRPAmount(1<<31))

	result, err := svc.GetNoRippleCheck(context.Background(), account, "gateway", ledgerHash, 0, true)
	if err != nil {
		t.Fatalf("GetNoRippleCheck: %v", err)
	}
	if len(result.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1: %+v", len(result.Transactions), result.Transactions)
	}
	if got := result.Transactions[0].Fee; got != "2147483647" {
		t.Errorf("clipped fee = %s, want 2147483647", got)
	}
}

func TestGetNoRippleCheck_FeeOverflowPropagatesOnlyWhenNeeded(t *testing.T) {
	svc := newOfferTestService(t)
	account, accountID := addressFromBytes(t, 0x30)
	ledgerHash := newNoRippleFeeLedger(t, svc, 4, account, accountID, drops.MaxDrops)
	svc.FeeTrack().SetRemoteFee(256_000_000)

	if _, err := svc.GetNoRippleCheck(context.Background(), account, "gateway", ledgerHash, 0, false); err != nil {
		t.Fatalf("transactions=false should not load AccountSet fee: %v", err)
	}
	_, err := svc.GetNoRippleCheck(context.Background(), account, "gateway", ledgerHash, 0, true)
	if !errors.Is(err, feetrack.ErrOverflow) {
		t.Fatalf("transactions=true error = %v, want feetrack.ErrOverflow", err)
	}
}
