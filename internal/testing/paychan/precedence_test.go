package paychan

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

// openChannel funds alice + bob and opens a 1000-XRP channel, returning its hex ID.
func openChannel(t *testing.T, env *jtx.TestEnv, alice, bob *jtx.Account) string {
	t.Helper()
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()
	createSeq := env.Seq(alice)
	jtx.RequireTxSuccess(t, env.Submit(ChannelCreate(alice, bob, xrp(1000), 3600, alice.PublicKeyHex()).Build()))
	env.Close()
	chanK := chanKeylet(alice, bob, createSeq)
	return hex.EncodeToString(chanK.Key[:])
}

// TestPayChanClaim_MaskBeatsSeqGap pins the mask-position finding through the full
// engine: a stray flag is rejected at preflight0 (temINVALID_FLAG) ahead of the
// preclaim sequence check, so it wins over the retriable terPRE_SEQ that a future
// Sequence would otherwise produce.
func TestPayChanClaim_MaskBeatsSeqGap(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	chanID := openChannel(t, env, alice, bob)

	claim := ChannelClaim(alice, chanID).
		Balance(xrp(500)).Amount(xrp(500)).
		Sequence(env.Seq(alice) + 10).
		Build()
	claim = withFlag(claim, strayPayChanFlag)
	require.Equal(t, "temINVALID_FLAG", env.Submit(claim).Code)
}

// TestPayChanClaim_CredentialsDisabledBeatsSeqGap pins the CredentialIDs finding
// through the engine: with Credentials disabled a CredentialIDs-bearing claim is
// temDISABLED (checkExtraFeatures, before preflight1), winning over the sequence
// gap's terPRE_SEQ.
func TestPayChanClaim_CredentialsDisabledBeatsSeqGap(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.DisableFeature("Credentials")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	chanID := openChannel(t, env, alice, bob)

	claim := ChannelClaim(alice, chanID).
		Balance(xrp(500)).Amount(xrp(500)).
		CredentialIDs([]string{"00000000000000000000000000000000000000000000000000000000000000AB"}).
		Sequence(env.Seq(alice) + 10).
		Build()
	require.Equal(t, "temDISABLED", env.Submit(claim).Code)
}
