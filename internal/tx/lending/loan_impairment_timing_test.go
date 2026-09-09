package lending

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestLoanImpairmentDueDateBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name          string
		now, due      uint32
		legacyDue     uint32
		cleanupResult ter.Result
	}{
		{name: "epoch", now: 0, due: 0, legacyDue: 0, cleanupResult: ter.TecTOO_SOON},
		{name: "zero due", now: 100, due: 0, legacyDue: 0, cleanupResult: ter.TesSUCCESS},
		{name: "overdue", now: 100, due: 99, legacyDue: 99, cleanupResult: ter.TesSUCCESS},
		{name: "exact due", now: 100, due: 100, legacyDue: 100, cleanupResult: ter.TecTOO_SOON},
		{name: "future due", now: 100, due: 101, legacyDue: 100, cleanupResult: ter.TecTOO_SOON},
	} {
		for _, cleanup := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cleanup=%t", tc.name, cleanup), func(t *testing.T) {
				fixture := newCoverClawbackFixture(t, 0)
				features := fixture.view.rules.EnabledIDs()
				if cleanup {
					features = append(features, amendment.FeatureFixCleanup3_4_0)
				}
				fixture.view.rules = amendment.NewRules(features)
				fixture.config.Rules = fixture.view.rules
				fixture.config.ParentCloseTime = tc.now
				ctx := &tx.ApplyContext{View: fixture.view, Config: fixture.config}
				brokerKey := keylet.LoanBrokerByID(fixture.brokerID)
				broker, err := readLoanBroker(fixture.view, brokerKey)
				if err != nil || broker == nil {
					t.Fatalf("read broker: %v", err)
				}
				vaultKey := keylet.VaultByID(broker.VaultID)
				if got := vault.UpdateVaultTotals(ctx, vaultKey, "30000", "29900", "0"); got != ter.TesSUCCESS {
					t.Fatalf("prepare vault: %v", got)
				}
				v, err := vault.ReadVaultLending(fixture.view, vaultKey)
				if err != nil || v == nil {
					t.Fatalf("read vault: %v", err)
				}
				loanKey := keylet.Loan(fixture.brokerID, 1)
				loan := &loanData{
					Borrower: fixture.issuerID, LoanBrokerID: fixture.brokerID,
					PrincipalOutstanding: "100", TotalValueOutstanding: "100",
					PaymentRemaining: 1, NextPaymentDueDate: tc.due,
				}
				if got := updateLoan(ctx, loanKey, loan); got != ter.TesSUCCESS {
					t.Fatalf("prepare loan: %v", got)
				}
				beforeLoan := bytes.Clone(fixture.view.data[loanKey.Key])
				beforeVault := bytes.Clone(fixture.view.data[vaultKey.Key])
				wantResult, wantDue := ter.TesSUCCESS, tc.legacyDue
				if cleanup {
					wantResult, wantDue = tc.cleanupResult, tc.due
				}
				if got := (&LoanManage{}).impairLoan(ctx, loanKey, loan, vaultKey, v); got != wantResult {
					t.Fatalf("impairLoan = %v, want %v", got, wantResult)
				}
				if wantResult != ter.TesSUCCESS {
					if !bytes.Equal(beforeLoan, fixture.view.data[loanKey.Key]) || !bytes.Equal(beforeVault, fixture.view.data[vaultKey.Key]) {
						t.Fatal("rejected impairment changed ledger entries")
					}
					return
				}
				stored, err := readLoan(fixture.view, loanKey)
				if err != nil || stored == nil {
					t.Fatalf("read impaired loan: %v", err)
				}
				if stored.NextPaymentDueDate != wantDue || stored.Flags&LsfLoanImpaired == 0 {
					t.Fatalf("impaired loan due=%d flags=%x, want due=%d and impaired flag", stored.NextPaymentDueDate, stored.Flags, wantDue)
				}
				storedVault, err := vault.ReadVaultLending(fixture.view, vaultKey)
				if err != nil || storedVault == nil {
					t.Fatalf("read impaired vault: %v", err)
				}
				if !lendNum(storedVault.LossUnrealized).Equal(lendNum("100")) {
					t.Fatalf("impaired vault loss = %s, want 100", storedVault.LossUnrealized)
				}
			})
		}
	}
}
