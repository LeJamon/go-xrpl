package lending

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/vault"
)

func TestLoanVaultExposureByVaultVersion(t *testing.T) {
	loan := &loanData{
		PrincipalOutstanding:     "100",
		TotalValueOutstanding:    "130",
		ManagementFeeOutstanding: "10",
	}
	legacy := &vault.VaultLending{}
	cash := &vault.VaultLending{LEVersion: vault.VaultVersionCashBasis}

	if got := loanVaultExposureForRules(legacy, loan, nil); !got.Equal(lendNum("120")) {
		t.Fatalf("legacy exposure = %s, want 120", got.String())
	}
	if got := loanVaultExposureForRules(cash, loan, nil); !got.Equal(lendNum("100")) {
		t.Fatalf("cash-basis exposure = %s, want 100", got.String())
	}
}
