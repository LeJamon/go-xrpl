package lending

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
)

func requireLoanNumberEqual(t *testing.T, got, want lmath.N, name string) {
	t.Helper()
	if !got.Equal(want) {
		t.Fatalf("%s = %s, want %s", name, got.String(), want.String())
	}
}

func TestLoanAccountingDeltasByVaultVersion(t *testing.T) {
	legacy := &vault.VaultLending{AssetsMaximum: "1100"}
	cash := &vault.VaultLending{LEVersion: vault.VaultVersionCashBasis, AssetsMaximum: "1000"}
	principal := lmath.FromInt(1000)
	interest := lmath.FromInt(100)

	legacyOrigination := loanOriginationDeltasForRules(legacy, principal, interest)
	requireLoanNumberEqual(t, legacyOrigination.assetsTotal, interest, "legacy origination assets delta")
	requireLoanNumberEqual(t, legacyOrigination.debtTotal, lmath.FromInt(1100), "legacy origination debt delta")

	cashOrigination := loanOriginationDeltasForRules(cash, principal, interest)
	requireLoanNumberEqual(t, cashOrigination.assetsTotal, lmath.Zero(), "cash origination assets delta")
	requireLoanNumberEqual(t, cashOrigination.debtTotal, principal, "cash origination debt delta")

	parts := lmath.LoanPaymentParts{
		PrincipalPaid: lmath.FromInt(50),
		InterestPaid:  lmath.FromInt(30),
		ValueChange:   lmath.FromInt(20),
	}
	legacyPayment := loanPaymentDeltasForRules(legacy, parts)
	requireLoanNumberEqual(t, legacyPayment.assetsTotal, parts.ValueChange, "legacy payment assets delta")
	requireLoanNumberEqual(t, legacyPayment.debtTotal, lmath.FromInt(60), "legacy payment debt delta")

	cashPayment := loanPaymentDeltasForRules(cash, parts)
	requireLoanNumberEqual(t, cashPayment.assetsTotal, parts.InterestPaid, "cash payment assets delta")
	requireLoanNumberEqual(t, cashPayment.debtTotal, parts.PrincipalPaid, "cash payment debt delta")
}

func TestLoanOriginationMaximumUsesAccountingModel(t *testing.T) {
	legacy := &vault.VaultLending{AssetsMaximum: "1100"}
	cash := &vault.VaultLending{LEVersion: vault.VaultVersionCashBasis, AssetsMaximum: "1000"}
	vaultTotal := lmath.FromInt(1000)

	if loanOriginationExceedsVaultMaximumForRules(legacy, vaultTotal, lmath.FromInt(100), nil) {
		t.Fatal("legacy origination at the exact maximum must be allowed")
	}
	if !loanOriginationExceedsVaultMaximumForRules(legacy, vaultTotal, lmath.FromInt(101), nil) {
		t.Fatal("legacy origination above the maximum must be rejected")
	}
	if loanOriginationExceedsVaultMaximumForRules(cash, vaultTotal, lmath.FromInt(101), nil) {
		t.Fatal("cash-basis origination must not use interest to consume AssetsMaximum")
	}
}
