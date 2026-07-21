package lending

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type loanSetOwnerCountView struct {
	data map[[32]byte][]byte
}

func (v *loanSetOwnerCountView) Read(k keylet.Keylet) ([]byte, error) {
	return v.data[k.Key], nil
}

func (v *loanSetOwnerCountView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.data[k.Key]
	return ok, nil
}

func (v *loanSetOwnerCountView) Insert(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}

func (v *loanSetOwnerCountView) Update(k keylet.Keylet, data []byte) error {
	v.data[k.Key] = data
	return nil
}

func (v *loanSetOwnerCountView) Erase(k keylet.Keylet) error {
	delete(v.data, k.Key)
	return nil
}

func (*loanSetOwnerCountView) AdjustDropsDestroyed(drops.XRPAmount)      {}
func (*loanSetOwnerCountView) ForEach(func([32]byte, []byte) bool) error { return nil }
func (*loanSetOwnerCountView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (*loanSetOwnerCountView) TxExists([32]byte) bool  { return false }
func (*loanSetOwnerCountView) Rules() *amendment.Rules { return amendment.EmptyRules() }
func (*loanSetOwnerCountView) LedgerSeq() uint32       { return 1 }

func TestLoanSetAppliesHoldingOwnerCountDelta(t *testing.T) {
	var submitter, counterparty [20]byte
	submitter[19] = 1
	counterparty[19] = 2

	newContext := func(t *testing.T) (*tx.ApplyContext, *loanSetOwnerCountView) {
		t.Helper()
		view := &loanSetOwnerCountView{data: make(map[[32]byte][]byte)}
		counterpartyRoot := &state.AccountRoot{
			Account:    state.EncodeAccountIDSafe(counterparty),
			OwnerCount: 1,
		}
		data, err := state.SerializeAccountRoot(counterpartyRoot)
		if err != nil {
			t.Fatalf("serialize counterparty: %v", err)
		}
		view.data[keylet.Account(counterparty).Key] = data
		return &tx.ApplyContext{
			View:      view,
			AccountID: submitter,
			Account: &state.AccountRoot{
				Account:    state.EncodeAccountIDSafe(submitter),
				OwnerCount: 1,
			},
		}, view
	}

	for _, tc := range []struct {
		name      string
		accountID [20]byte
		delta     int32
		want      uint32
	}{
		{name: "IOU borrower without trust line", accountID: submitter, delta: 1, want: 2},
		{name: "MPT borrower without MPToken", accountID: submitter, delta: 1, want: 2},
		{name: "existing holding", accountID: submitter, delta: 0, want: 1},
		{name: "counterparty borrower", accountID: counterparty, delta: 1, want: 2},
		{name: "origination fee recipient", accountID: counterparty, delta: 1, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, view := newContext(t)
			if got := applyLoanSetHoldingOwnerCount(ctx, tc.accountID, tc.delta); got != ter.TesSUCCESS {
				t.Fatalf("applyLoanSetHoldingOwnerCount = %v, want tesSUCCESS", got)
			}
			if tc.accountID == submitter {
				if ctx.Account.OwnerCount != tc.want {
					t.Fatalf("submitter OwnerCount = %d, want %d", ctx.Account.OwnerCount, tc.want)
				}
				return
			}
			root, err := tx.ReadAccountRoot(view, counterparty)
			if err != nil {
				t.Fatalf("read counterparty: %v", err)
			}
			if root.OwnerCount != tc.want {
				t.Fatalf("counterparty OwnerCount = %d, want %d", root.OwnerCount, tc.want)
			}
			if ctx.Account.OwnerCount != 1 {
				t.Fatalf("submitter OwnerCount = %d, want unchanged 1", ctx.Account.OwnerCount)
			}
		})
	}
}
