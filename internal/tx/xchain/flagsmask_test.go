package xchain

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// TestXChainFlagsMask pins each XChain type's FlagsMasker adoption: the mask
// returned by GetFlagsMask (enforced by the engine at preflight0) matches the
// exact rippled 3.2.0 mask, rejects a stray non-universal bit, and permits the
// universal flags. Reference: rippled BridgeModify::getFlagsMask =
// tfXChainModifyBridgeMask; the other XChain transactors inherit the base
// tfUniversalMask.
func TestXChainFlagsMask(t *testing.T) {
	rules := amendment.AllSupportedRules()
	const stray = uint32(0x08000000)

	cases := []struct {
		name string
		mask uint32
		want uint32
	}{
		{"XChainCreateBridge", (&XChainCreateBridge{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"XChainModifyBridge", (&XChainModifyBridge{}).GetFlagsMask(rules), tfXChainModifyBridgeMask},
		{"XChainCreateClaimID", (&XChainCreateClaimID{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"XChainCommit", (&XChainCommit{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"XChainClaim", (&XChainClaim{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"XChainAccountCreateCommit", (&XChainAccountCreateCommit{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"XChainAddClaimAttestation", (&XChainAddClaimAttestation{}).GetFlagsMask(rules), tx.TfUniversalMask},
		{"XChainAddAccountCreateAttestation", (&XChainAddAccountCreateAttestation{}).GetFlagsMask(rules), tx.TfUniversalMask},
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
	// XChainModifyBridge must permit tfClearAccountCreateAmount.
	if m := (&XChainModifyBridge{}).GetFlagsMask(rules); m&tfClearAccountCreateAmount != 0 {
		t.Errorf("XChainModifyBridge mask must permit tfClearAccountCreateAmount, got %#x", m)
	}
}
