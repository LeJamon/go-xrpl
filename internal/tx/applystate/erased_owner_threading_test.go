package applystate

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestEraseThreadsOwnersFromOriginalEntry(t *testing.T) {
	lowID := [20]byte{1}
	highID := [20]byte{2}
	lowAddress := state.EncodeAccountIDSafe(lowID)
	highAddress := state.EncodeAccountIDSafe(highID)

	base := newMockBaseView()
	for _, account := range []struct {
		id      [20]byte
		address string
	}{
		{id: lowID, address: lowAddress},
		{id: highID, address: highAddress},
	} {
		data, err := state.SerializeAccountRoot(&state.AccountRoot{
			Account:  account.address,
			Balance:  100_000_000,
			Sequence: 1,
		})
		if err != nil {
			t.Fatalf("serialize account root: %v", err)
		}
		base.data[keylet.Account(account.id).Key] = data
	}

	lineKey := keylet.Line(lowID, highID, "USD")
	original := &state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", lowAddress),
		HighLimit: state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", highAddress),
	}
	originalData, err := state.SerializeRippleState(original)
	if err != nil {
		t.Fatalf("serialize original ripple state: %v", err)
	}
	base.data[lineKey.Key] = originalData

	current := *original
	current.HighLimit = state.NewIssuedAmountFromValue(state.MaxMantissa, state.MaxExponent, "USD", lowAddress)
	currentData, err := state.SerializeRippleState(&current)
	if err != nil {
		t.Fatalf("serialize current ripple state: %v", err)
	}

	txHash := [32]byte{0xaa}
	const ledgerSequence = uint32(123)
	table := NewApplyStateTable(base, txHash, ledgerSequence, amendment.AllSupportedRules())
	if err := table.Update(lineKey, currentData); err != nil {
		t.Fatalf("update ripple state: %v", err)
	}
	if err := table.Erase(lineKey); err != nil {
		t.Fatalf("erase ripple state: %v", err)
	}
	if _, err := table.Apply(); err != nil {
		t.Fatalf("apply state table: %v", err)
	}

	for _, id := range [][20]byte{lowID, highID} {
		data := base.data[keylet.Account(id).Key]
		account, err := state.ParseAccountRoot(data)
		if err != nil {
			t.Fatalf("parse threaded account root: %v", err)
		}
		if account.PreviousTxnID != txHash {
			t.Fatalf("account %s previous transaction = %x, want %x", account.Account, account.PreviousTxnID, txHash)
		}
		if account.PreviousTxnLgrSeq != ledgerSequence {
			t.Fatalf("account %s previous ledger sequence = %d, want %d", account.Account, account.PreviousTxnLgrSeq, ledgerSequence)
		}
	}
}
