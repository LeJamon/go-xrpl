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

// LoanBrokerSet accepts tfLoanBrokerPrivate only under LendingProtocolV1_2.
// Reference: rippled 3.5.0-rc1 LoanBrokerSet::getFlagsMask.
func TestLoanBrokerSetFlagsMaskLendingProtocolV1_2(t *testing.T) {
	without := amendment.AllSupportedRules()
	if without.Enabled(amendment.FeatureLendingProtocolV1_2) {
		t.Fatal("LendingProtocolV1_2 is unsupported and must not be in the supported rules")
	}
	with := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).Enable(amendment.FeatureLendingProtocolV1_2).Build()

	if got := (&LoanBrokerSet{}).GetFlagsMask(without); got != tx.TfUniversalMask || got&tfLoanBrokerPrivate == 0 {
		t.Fatalf("mask without LendingProtocolV1_2 = %#x, want the universal mask", got)
	}
	got := (&LoanBrokerSet{}).GetFlagsMask(with)
	if got != tx.TfUniversalMask&^0x00010000 {
		t.Fatalf("mask with LendingProtocolV1_2 = %#x, want %#x", got, tx.TfUniversalMask&^0x00010000)
	}
}
