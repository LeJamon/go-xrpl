package clawback_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/clawback"
	"github.com/stretchr/testify/require"
)

// TestClawback_FlagMaskBeatsBadFee pins Clawback's FlagsMasker adoption: the
// invalid-flags rejection runs at preflight0, ahead of the fee check, so a stray
// flag plus a malformed (negative) fee surfaces temINVALID_FLAG, not temBAD_FEE.
// Reference: rippled base Transactor::getFlagsMask precedes preflight1's fee check.
func TestClawback_FlagMaskBeatsBadFee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount("issuer")
	holder := jtx.NewAccount("holder")
	env.Fund(issuer, holder)
	env.Close()

	c := clawback.Claw(issuer, holder, "USD", 100).Flags(0x08000000).Build()
	c.GetCommon().Fee = "-10" // malformed fee, reached only if the flag check passes
	require.Equal(t, "temINVALID_FLAG", env.Submit(c).Code)
}
