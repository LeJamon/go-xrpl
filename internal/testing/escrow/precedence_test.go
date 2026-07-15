package escrow_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/escrow"
	"github.com/stretchr/testify/require"
)

// strayEscrowFlag is a bit outside tfUniversalMask — Escrow has no type-specific
// flags, so any non-universal bit is stray for every escrow transaction.
const strayEscrowFlag = uint32(0x00010000)

// TestEscrowCreate_MaskBeatsBadAmount pins the EscrowCreate mask-position finding
// through the engine: the fix1543 flag mask fires at preflight0 (temINVALID_FLAG),
// ahead of the PreflightRules amount check that a zero Amount would otherwise
// report as temBAD_AMOUNT.
func TestEscrowCreate_MaskBeatsBadAmount(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	create := escrow.EscrowCreate(alice, bob, 0). // zero Amount
							Flags(strayEscrowFlag).
							FinishAfter(900000000).
							Build()
	require.Equal(t, "temINVALID_FLAG", env.Submit(create).Code)
}

// TestEscrowFinish_CredentialsDisabledBeatsShape pins EscrowFinish CredentialIDs
// finding #1 through the engine: with Credentials disabled a CredentialIDs-bearing
// finish is temDISABLED (checkExtraFeatures, before preflight1), winning over the
// Condition/Fulfillment XOR temMALFORMED that would otherwise fire in Validate.
func TestEscrowFinish_CredentialsDisabledBeatsShape(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("Credentials")
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	// Condition-without-Fulfillment (a Validate temMALFORMED) plus CredentialIDs on
	// a Credentials-disabled network: temDISABLED must win.
	finish := escrow.EscrowFinish(alice, alice, 999).
		ConditionHex("A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100").
		CredentialIDs([]string{"00000000000000000000000000000000000000000000000000000000000000AB"}).
		Sequence(env.Seq(alice) + 10).
		Build()
	require.Equal(t, "temDISABLED", env.Submit(finish).Code)
}
