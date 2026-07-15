package lending

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestLendingFlagsMask pins each LendingProtocol type's FlagsMasker adoption: the
// mask returned by GetFlagsMask (enforced by the engine at preflight0, ahead of
// the fee check) matches the exact rippled 3.2.0 mask, rejects a stray
// non-universal bit, and permits the universal flags. Reference: rippled
// LoanSet/LoanManage/LoanPay::getFlagsMask override; the LoanBroker* and LoanDelete
// types inherit the base tfUniversalMask.
func TestLendingFlagsMask(t *testing.T) {
	rules := amendment.AllSupportedRules()
	const stray = uint32(0x08000000)

	cases := []struct {
		name string
		mask uint32
		want uint32
	}{
		{"LoanBrokerSet", (&LoanBrokerSet{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"LoanBrokerDelete", (&LoanBrokerDelete{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"LoanBrokerCoverDeposit", (&LoanBrokerCoverDeposit{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"LoanBrokerCoverWithdraw", (&LoanBrokerCoverWithdraw{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"LoanBrokerCoverClawback", (&LoanBrokerCoverClawback{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"LoanSet", (&LoanSet{}).GetFlagsMask(rules), TfLoanSetMask},
		{"LoanDelete", (&LoanDelete{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"LoanManage", (&LoanManage{}).GetFlagsMask(rules), TfLoanManageMask},
		{"LoanPay", (&LoanPay{}).GetFlagsMask(rules), TfLoanPayMask},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.mask != c.want {
				t.Fatalf("%s mask = %#x, want %#x", c.name, c.mask, c.want)
			}
			if c.mask&stray == 0 {
				t.Errorf("%s mask must reject the stray flag %#x", c.name, stray)
			}
			if c.mask&(tx.TfFullyCanonicalSig|tx.TfInnerBatchTxn) != 0 {
				t.Errorf("%s mask must permit the universal flags", c.name)
			}
		})
	}
}
