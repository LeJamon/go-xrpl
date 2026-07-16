package lending

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
)

func TestAssociateLoanAssetUsesSFieldMetadata(t *testing.T) {
	rules := amendment.NewRules([][32]byte{
		amendment.FeatureSingleAssetVault,
		amendment.FeatureLendingProtocol,
		amendment.FeatureFixCleanup3_2_0,
	})
	loan := &loanData{
		LoanOriginationFee:       "1.4",
		LoanServiceFee:           "2.4",
		LatePaymentFee:           "3.4",
		ClosePaymentFee:          "4.4",
		PeriodicPayment:          "10.00000001505552512",
		PrincipalOutstanding:     "11.4",
		TotalValueOutstanding:    "12.6",
		ManagementFeeOutstanding: "0.4",
	}

	associateLoanAsset(loan, true, rules)

	for _, field := range []struct {
		name string
		got  string
		want string
	}{
		{"LoanOriginationFee", loan.LoanOriginationFee, "1.4"},
		{"LoanServiceFee", loan.LoanServiceFee, "2.4"},
		{"LatePaymentFee", loan.LatePaymentFee, "3.4"},
		{"ClosePaymentFee", loan.ClosePaymentFee, "4.4"},
		{"PeriodicPayment", loan.PeriodicPayment, "10.00000001505552512"},
	} {
		if field.got != field.want {
			t.Errorf("%s = %q, want %q", field.name, field.got, field.want)
		}
	}
	if got := lendNumForRules(loan.PrincipalOutstanding, rules); !got.Equal(lendNumForRules("11", rules)) {
		t.Errorf("PrincipalOutstanding = %q, want 11", loan.PrincipalOutstanding)
	}
	if got := lendNumForRules(loan.TotalValueOutstanding, rules); !got.Equal(lendNumForRules("13", rules)) {
		t.Errorf("TotalValueOutstanding = %q, want 13", loan.TotalValueOutstanding)
	}
	if got := loan.ManagementFeeOutstanding; got != "" {
		t.Errorf("ManagementFeeOutstanding = %q, want empty", got)
	}
}
