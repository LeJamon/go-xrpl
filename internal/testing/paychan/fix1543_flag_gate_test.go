package paychan

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// strayPayChanFlag is a bit outside every PayChan flag mask — it is neither a
// universal flag nor a PayChanClaim flag (tfRenew=0x00010000, tfClose=0x00020000)
// — so it is stray for Create, Fund and Claim alike.
const strayPayChanFlag = uint32(0x00040000)

func withFlag(txn tx.Transaction, flag uint32) tx.Transaction {
	txn.GetCommon().SetFlags(txn.GetCommon().GetFlags() | flag)
	return txn
}

// TestPayChan_Fix1543FlagGate exercises both branches of the fix1543 gate on
// the tfUniversalMask / tfPayChanClaimMask checks of PayChanCreate, PayChanFund
// and PayChanClaim. With fix1543 enabled (default) a stray flag is rejected with
// temINVALID_FLAG; with it disabled the flag is ignored.
// Reference: rippled PayChan.cpp:177,332,443.
func TestPayChan_Fix1543FlagGate(t *testing.T) {
	const settleDelay = uint32(3600)

	setup := func(t *testing.T, disable bool) (*jtx.TestEnv, *jtx.Account, *jtx.Account, string) {
		t.Helper()
		env := jtx.NewTestEnv(t)
		if disable {
			env.DisableFeature("fix1543")
		}
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		createSeq := env.Seq(alice)
		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, alice.PublicKeyHex()).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		chanK := chanKeylet(alice, bob, createSeq)
		return env, alice, bob, hex.EncodeToString(chanK.Key[:])
	}

	t.Run("create enabled rejects stray flag", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		txn := withFlag(ChannelCreate(alice, bob, xrp(1000), settleDelay, alice.PublicKeyHex()).Build(), strayPayChanFlag)
		require.Equal(t, "temINVALID_FLAG", env.Submit(txn).Code)
	})

	t.Run("create disabled ignores stray flag", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fix1543")
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		txn := withFlag(ChannelCreate(alice, bob, xrp(1000), settleDelay, alice.PublicKeyHex()).Build(), strayPayChanFlag)
		jtx.RequireTxSuccess(t, env.Submit(txn))
	})

	t.Run("fund enabled rejects stray flag", func(t *testing.T) {
		env, alice, _, chanID := setup(t, false)
		txn := withFlag(ChannelFund(alice, chanID, xrp(1000)).Build(), strayPayChanFlag)
		require.Equal(t, "temINVALID_FLAG", env.Submit(txn).Code)
	})

	t.Run("fund disabled ignores stray flag", func(t *testing.T) {
		env, alice, _, chanID := setup(t, true)
		txn := withFlag(ChannelFund(alice, chanID, xrp(1000)).Build(), strayPayChanFlag)
		jtx.RequireTxSuccess(t, env.Submit(txn))
	})

	t.Run("claim enabled rejects stray flag", func(t *testing.T) {
		env, alice, _, chanID := setup(t, false)
		txn := withFlag(ChannelClaim(alice, chanID).Balance(xrp(500)).Amount(xrp(500)).Build(), strayPayChanFlag)
		require.Equal(t, "temINVALID_FLAG", env.Submit(txn).Code)
	})

	t.Run("claim disabled ignores stray flag", func(t *testing.T) {
		env, alice, _, chanID := setup(t, true)
		txn := withFlag(ChannelClaim(alice, chanID).Balance(xrp(500)).Amount(xrp(500)).Build(), strayPayChanFlag)
		jtx.RequireTxSuccess(t, env.Submit(txn))
	})
}
