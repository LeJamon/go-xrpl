package lending

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/stretchr/testify/require"
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

	legacyOrigination := loanOriginationDeltas(legacy, principal, interest)
	requireLoanNumberEqual(t, legacyOrigination.assetsTotal, interest, "legacy origination assets delta")
	requireLoanNumberEqual(t, legacyOrigination.debtTotal, lmath.FromInt(1100), "legacy origination debt delta")

	cashOrigination := loanOriginationDeltas(cash, principal, interest)
	requireLoanNumberEqual(t, cashOrigination.assetsTotal, lmath.Zero(), "cash origination assets delta")
	requireLoanNumberEqual(t, cashOrigination.debtTotal, principal, "cash origination debt delta")

	parts := lmath.LoanPaymentParts{
		PrincipalPaid: lmath.FromInt(50),
		InterestPaid:  lmath.FromInt(30),
		ValueChange:   lmath.FromInt(20),
	}
	legacyPayment := loanPaymentDeltas(legacy, parts)
	requireLoanNumberEqual(t, legacyPayment.assetsTotal, parts.ValueChange, "legacy payment assets delta")
	requireLoanNumberEqual(t, legacyPayment.debtTotal, lmath.FromInt(60), "legacy payment debt delta")

	cashPayment := loanPaymentDeltas(cash, parts)
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

func TestCreditLoanPayment(t *testing.T) {
	iou := tx.Asset{Currency: "USD"}
	xrp := tx.Asset{Currency: "XRP"}
	mpt := tx.Asset{MPTIssuanceID: "000000010000000000000000000000000000000000000001"}
	for _, tc := range []struct {
		name                     string
		asset                    tx.Asset
		available, total         string
		paid, totalDelta         string
		want                     ter.Result
		wantAvailable, wantTotal string
	}{
		{"XRP total reaches maximum", xrp, "10", "99999999999999999", "1", "1", ter.TesSUCCESS, "11", "100000000000000000"},
		{"XRP total overflow precedes unchanged available", xrp, "10", "100000000000000000", "0", "1", ter.TefEXCEPTION, "", ""},
		{"XRP available overflow precedes excess available", xrp, "100000000000000000", "100000000000000000", "1", "0", ter.TefEXCEPTION, "", ""},
		{"MPT totals above XRP maximum", mpt, "100000000000000000", "100000000000000000", "1", "1", ter.TesSUCCESS, "100000000000000001", "100000000000000001"},
		{"available rounds back", iou, "10", "20", "1e-15", "0", ter.TecPRECISION_LOSS, "", ""},
		{"positive delta rounds away", iou, "10", "20", "1", "1e-15", ter.TecPRECISION_LOSS, "", ""},
		{"negative delta rounds away", iou, "10", "20", "1", "-1e-15", ter.TecPRECISION_LOSS, "", ""},
		{"total changes without delta", xrp, "10", "20.25", "1", "0", ter.TecINTERNAL, "", ""},
		{"unchanged available takes precedence", xrp, "10", "20.25", "0", "0", ter.TecPRECISION_LOSS, "", ""},
		{"lost delta precedes excess available", iou, "20", "20", "1", "1e-15", ter.TecPRECISION_LOSS, "", ""},
		{"available exceeds total", xrp, "20", "20", "1", "0", ter.TecINTERNAL, "", ""},
		{"equal after rounding", iou, "9", "10", "1.000000000000001", "0", ter.TesSUCCESS, "10", "10"},
		{"total decreases", xrp, "10", "20", "2", "-1", ter.TesSUCCESS, "12", "19"},
		{"total increases", iou, "", "20", "2.25", "0.25", ter.TesSUCCESS, "2.25", "20.25"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := vault.VaultLending{VaultInfo: vault.VaultInfo{Asset: tc.asset}, AssetsAvailable: tc.available, AssetsTotal: tc.total}
			before := v
			require.Equal(t, tc.want, creditLoanPayment(&v, lendNum(tc.paid), lendNum(tc.totalDelta), nil))
			if tc.want != ter.TesSUCCESS {
				require.Equal(t, before, v)
				return
			}
			requireLoanNumberEqual(t, lendNum(v.AssetsAvailable), lendNum(tc.wantAvailable), "AssetsAvailable")
			requireLoanNumberEqual(t, lendNum(v.AssetsTotal), lendNum(tc.wantTotal), "AssetsTotal")
		})
	}
}
