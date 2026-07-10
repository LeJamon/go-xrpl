package amm

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestAMMFlagsMask pins each AMM type's FlagsMasker adoption: the mask returned by
// GetFlagsMask (enforced by the engine at preflight0, ahead of the fee check)
// matches the exact rippled 3.2.0 mask, rejects a stray non-universal bit, and
// permits the universal flags. Reference: rippled AMM*::getFlagsMask (AMMDeposit,
// AMMWithdraw, AMMClawback override; the rest inherit the base tfUniversalMask).
func TestAMMFlagsMask(t *testing.T) {
	rules := amendment.AllSupportedRules()
	const stray = uint32(0x08000000)

	cases := []struct {
		name string
		mask uint32
		want uint32
	}{
		{"AMMCreate", (&AMMCreate{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"AMMBid", (&AMMBid{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"AMMVote", (&AMMVote{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"AMMDelete", (&AMMDelete{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"AMMDeposit", (&AMMDeposit{}).GetFlagsMask(rules), tfAMMDepositMask},
		{"AMMWithdraw", (&AMMWithdraw{}).GetFlagsMask(rules), tfAMMWithdrawMask},
		{"AMMClawback", (&AMMClawback{}).GetFlagsMask(rules), tfAMMClawbackMask},
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
