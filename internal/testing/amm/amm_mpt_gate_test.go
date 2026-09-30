package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/ter"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
)

const releaseGateMPTID = "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12"

// TestAMMMPTReleaseGate records the v3.4.1 release scope: MPTokensV2 is not
// enabled by the supported amendment registry, so AMM MPT amounts are rejected
// before any pool state can be created. Forced enabled profiles remain separate
// conformance fixtures and do not claim release support.
func TestAMMMPTReleaseGate(t *testing.T) {
	env := amm.NewAMMTestEnv(t)
	env.Fund()
	env.DisableFeature("MPTokensV2")
	env.Close()

	mpt := state.NewMPTAmountWithIssuanceID(100, env.GW.Address, releaseGateMPTID)
	result := env.Submit(amm.AMMCreate(env.Alice, mpt, amm.XRPAmount(1)).Build())
	amm.ExpectTER(t, result, ter.TemDISABLED.String())
}
