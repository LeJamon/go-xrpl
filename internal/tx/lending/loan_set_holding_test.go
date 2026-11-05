package lending

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type loanSetHoldingErrorView struct {
	tx.ReadOnlyLedgerView
	holdingKey [32]byte
}

func (v loanSetHoldingErrorView) Exists(k keylet.Keylet) (bool, error) {
	if k.Key == v.holdingKey {
		return false, errors.New("holding lookup failed")
	}
	return v.ReadOnlyLedgerView.Exists(k)
}

func TestLoanSetHoldingLookupErrors(t *testing.T) {
	tests := []struct {
		name       string
		cleanup    bool
		ownerError bool
		fee        string
		want       ter.Result
	}{
		{name: "borrower lookup fails", cleanup: true, want: ter.TefINTERNAL},
		{name: "fee recipient lookup fails", cleanup: true, ownerError: true, fee: "1", want: ter.TefINTERNAL},
		{name: "absent fee skips recipient lookup", cleanup: true, ownerError: true, want: ter.TesSUCCESS},
		{name: "zero fee skips recipient lookup", cleanup: true, ownerError: true, fee: "0", want: ter.TesSUCCESS},
		{name: "legacy skips holding lookup", want: ter.TesSUCCESS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCoverAuthFixture(t)
			builder := amendment.NewRulesBuilder()
			for _, id := range f.base.view.rules.EnabledIDs() {
				builder.Enable(id)
			}
			if tc.cleanup {
				builder.Enable(amendment.FeatureFixCleanup3_4_0)
			} else {
				builder.Disable(amendment.FeatureFixCleanup3_4_0)
			}
			f.base.view.rules = builder.Build()
			f.base.config.Rules = f.base.view.rules
			if f.base.config.Rules.Enabled(amendment.FeatureFixCleanup3_4_0) != tc.cleanup {
				t.Fatal("unexpected fixCleanup3_4_0 state")
			}
			f.putHolding(t, f.dest)
			f.putHolding(t, f.owner)
			loan := NewLoanSet(f.destinationAddress(t), f.broker, "100")
			if tc.fee != "" {
				loan.LoanOriginationFee = &tc.fee
			}
			account := f.dest
			if tc.ownerError {
				account = f.owner
			}
			view := loanSetHoldingErrorView{
				ReadOnlyLedgerView: f.base.view,
				holdingKey:         keylet.MPTokenByID(f.base.mptID, account).Key,
			}
			if got := loan.Preclaim(view, f.base.config); got != tc.want {
				t.Fatalf("LoanSet.Preclaim() = %v, want %v", got, tc.want)
			}
		})
	}
}
