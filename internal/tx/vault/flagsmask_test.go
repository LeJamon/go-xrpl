package vault

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestVaultFlagsMask pins each Vault type's FlagsMasker adoption: the mask
// returned by GetFlagsMask (enforced by the engine at preflight0, ahead of the fee
// check) matches the exact rippled 3.2.0 mask, rejects a stray non-universal bit,
// and permits the universal flags. Reference: rippled VaultCreate::getFlagsMask =
// tfVaultCreateMask; the other vault types inherit the base tfUniversalMask.
func TestVaultFlagsMask(t *testing.T) {
	rules := amendment.AllSupportedRules()
	const stray = uint32(0x08000000)

	cases := []struct {
		name string
		mask uint32
		want uint32
	}{
		{"VaultCreate", (&VaultCreate{}).GetFlagsMask(rules), tfVaultCreateMask},
		{"VaultSet", (&VaultSet{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"VaultDelete", (&VaultDelete{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"VaultDeposit", (&VaultDeposit{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"VaultWithdraw", (&VaultWithdraw{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"VaultClawback", (&VaultClawback{}).GetFlagsMask(rules), tx.TfUniversalMask},
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
	// VaultCreate must permit its two type-specific flags.
	if m := (&VaultCreate{}).GetFlagsMask(rules); m&(VaultFlagPrivate|VaultFlagShareNonTransferable) != 0 {
		t.Errorf("VaultCreate mask must permit tfVaultPrivate/tfVaultShareNonTransferable, got %#x", m)
	}
}
