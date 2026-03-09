// Package paychan contains integration tests for payment channel behavior.
// Tests ported from rippled's PayChan_test.cpp for 100% parity.
package paychan

import (
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/testing/accountset"
	"github.com/LeJamon/goXRPLd/internal/testing/credential"
	"github.com/LeJamon/goXRPLd/internal/testing/depositpreauth"

	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/stretchr/testify/require"
)

// TestPayChan_Simple tests basic payment channel creation and operations.
// From rippled: PayChan_test::testSimple
func TestPayChan_Simple(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(100)
	createSeq := env.Seq(alice)
	chanK := chanKeylet(alice, bob, createSeq)

	result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	require.Equal(t, uint64(0), chanBalance(env, chanK), "channelBalance should be 0")
	require.Equal(t, uint64(xrp(1000)), chanAmount(env, chanK), "channelAmount should be 1000 XRP")

	// Fund the channel
	{
		chanIDHex := hex.EncodeToString(chanK.Key[:])
		preAlice := env.Balance(alice)
		result := env.Submit(ChannelFund(alice, chanIDHex, xrp(1000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.Equal(t, preAlice-uint64(xrp(1000))-10, env.Balance(alice))
	}

	chanBal := chanBalance(env, chanK)
	chanAmt := chanAmount(env, chanK)
	require.Equal(t, uint64(0), chanBal)
	require.Equal(t, uint64(xrp(2000)), chanAmt)

	chanIDHex := hex.EncodeToString(chanK.Key[:])

	// Bad amounts (negative amounts)
	// Note: IOU amounts tested via direct tx construction in rippled;
	// the Go builder only accepts drops so we test negative amounts here.
	submitExpect(t, env, ChannelCreate(alice, bob, -xrp(1000), settleDelay, pk).Build(), "temBAD_AMOUNT")
	submitExpect(t, env, ChannelFund(alice, chanIDHex, -xrp(1000)).Build(), "temBAD_AMOUNT")

	// Invalid destination
	noAccount := jtx.NewAccount("noAccount")
	submitExpect(t, env, ChannelCreate(alice, noAccount, xrp(1000), settleDelay, pk).Build(), "tecNO_DST")

	// Can't create channel to same account
	submitExpect(t, env, ChannelCreate(alice, alice, xrp(1000), settleDelay, pk).Build(), "temDST_IS_SRC")

	// Invalid channel (fund non-existent channel)
	submitExpect(t, env, ChannelFund(alice, "0000000000000000000000000000000000000000000000000000000000000000", xrp(1000)).Build(), "tecNO_ENTRY")

	// Not enough funds
	submitExpect(t, env, ChannelCreate(alice, bob, xrp(10000), settleDelay, pk).Build(), "tecUNFUNDED")

	// No signature claim with bad amounts (negative and non-xrp)
	// We test the no-sig claim temBAD_AMOUNT cases
	{
		negXRP := drops(-100_000_000)
		posXRP := drops(100_000_000)

		// neg balance, neg amount
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).Balance(negXRP).Amount(negXRP).Build(), "temBAD_AMOUNT")
		// pos balance, neg amount
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).Balance(posXRP).Amount(negXRP).Build(), "temBAD_AMOUNT")
		// neg balance, pos amount
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).Balance(negXRP).Amount(posXRP).Build(), "temBAD_AMOUNT")
	}

	// No signature claim more than authorized (reqBal > authAmt)
	{
		delta := xrp(500)
		reqBal := int64(chanBal) + delta
		authAmt := reqBal - xrp(100)
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).Balance(reqBal).Amount(authAmt).Build(), "temBAD_AMOUNT")
	}

	// No signature needed since the owner is claiming
	{
		preBob := env.Balance(bob)
		delta := xrp(500)
		reqBal := int64(chanBal) + delta
		authAmt := reqBal + xrp(100)

		result := env.Submit(ChannelClaim(alice, chanIDHex).Balance(reqBal).Amount(authAmt).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint64(reqBal), chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
		require.Equal(t, preBob+uint64(delta), env.Balance(bob))
		chanBal = uint64(reqBal)
	}

	// Claim with signature
	{
		preBob := env.Balance(bob)
		delta := xrp(500)
		reqBal := int64(chanBal) + delta
		authAmt := reqBal + xrp(100)

		sig := signClaimAuth(alice, chanIDHex, uint64(authAmt))

		result := env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqBal).Amount(authAmt).
			Signature(sig).PublicKey(pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint64(reqBal), chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
		require.Equal(t, preBob+uint64(delta)-10, env.Balance(bob))
		chanBal = uint64(reqBal)

		// Claim again same amount
		preBob = env.Balance(bob)
		result = env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqBal).Amount(authAmt).
			Signature(sig).PublicKey(pk).Build())
		require.Equal(t, "tecUNFUNDED_PAYMENT", result.Code)
		env.Close()

		require.Equal(t, chanBal, chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
		require.Equal(t, preBob-10, env.Balance(bob))
	}

	// Try to claim more than authorized
	{
		preBob := env.Balance(bob)
		authAmt := int64(chanBal) + xrp(500)
		reqAmt := authAmt + 1 // 1 drop more than authorized

		sig := signClaimAuth(alice, chanIDHex, uint64(authAmt))

		result := env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqAmt).Amount(authAmt).
			Signature(sig).PublicKey(pk).Build())
		require.Equal(t, "temBAD_AMOUNT", result.Code)

		require.Equal(t, chanBal, chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
		require.Equal(t, preBob, env.Balance(bob))
	}

	// Dst tries to fund the channel
	{
		result := env.Submit(ChannelFund(bob, chanIDHex, xrp(1000)).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)

		require.Equal(t, chanBal, chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
	}

	// Wrong signing key
	{
		sig := signClaimAuth(bob, chanIDHex, uint64(xrp(1500)))
		result := env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(xrp(1500)).Amount(xrp(1500)).
			Signature(sig).PublicKey(bob.PublicKeyHex()).Build())
		require.Equal(t, "temBAD_SIGNER", result.Code)

		require.Equal(t, chanBal, chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
	}

	// Bad signature
	{
		sig := signClaimAuth(bob, chanIDHex, uint64(xrp(1500)))
		result := env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(xrp(1500)).Amount(xrp(1500)).
			Signature(sig).PublicKey(pk).Build())
		require.Equal(t, "temBAD_SIGNATURE", result.Code)

		require.Equal(t, chanBal, chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
	}

	// Dst closes channel
	{
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		result := env.Submit(ChannelClaim(bob, chanIDHex).Close().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.False(t, chanExists(env, chanK))
		delta := chanAmt - chanBal
		require.Equal(t, preAlice+delta, env.Balance(alice))
		require.Equal(t, preBob-10, env.Balance(bob))
	}
}

// TestPayChan_DisallowIncoming tests the DisallowIncoming flag.
// From rippled: PayChan_test::testDisallowIncoming
func TestPayChan_DisallowIncoming(t *testing.T) {
	// Test flag doesn't set without amendment
	t.Run("FlagDoesntSetWithoutAmendment", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("DisallowIncoming")

		alice := jtx.NewAccount("alice")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.Close()

		env.EnableDisallowIncomingPayChan(alice)
		env.Close()

		// Flag should not be set since amendment is disabled
		info := env.AccountInfo(alice)
		require.NotNil(t, info)
		require.Equal(t, uint32(0), info.Flags&state.LsfDisallowIncomingPayChan,
			"DisallowIncomingPayChan flag should not be set without amendment")
	})

	t.Run("MainFlow", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		cho := jtx.NewAccount("cho")

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(cho, uint64(jtx.XRP(10000)))
		env.Close()

		pk := alice.PublicKeyHex()
		settleDelay := uint32(100)

		// Set flag on bob only
		env.EnableDisallowIncomingPayChan(bob)
		env.Close()

		// Channel creation from alice to bob is disallowed
		{
			seq := env.Seq(alice)
			chanK := chanKeylet(alice, bob, seq)
			submitExpect(t, env, ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build(), "tecNO_PERMISSION")
			require.False(t, chanExists(env, chanK))
		}

		// Set flag on alice also
		env.EnableDisallowIncomingPayChan(alice)
		env.Close()

		// Channel creation from bob to alice is now disallowed
		{
			seq := env.Seq(bob)
			chanK := chanKeylet(bob, alice, seq)
			submitExpect(t, env, ChannelCreate(bob, alice, xrp(1000), settleDelay, pk).Build(), "tecNO_PERMISSION")
			require.False(t, chanExists(env, chanK))
		}

		// Remove flag from bob
		env.DisableDisallowIncomingPayChan(bob)
		env.Close()

		// Now the channel from alice to bob can exist
		{
			seq := env.Seq(alice)
			chanK := chanKeylet(alice, bob, seq)
			result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			require.True(t, chanExists(env, chanK))
		}

		// A channel from cho to alice isn't allowed
		{
			seq := env.Seq(cho)
			chanK := chanKeylet(cho, alice, seq)
			submitExpect(t, env, ChannelCreate(cho, alice, xrp(1000), settleDelay, pk).Build(), "tecNO_PERMISSION")
			require.False(t, chanExists(env, chanK))
		}

		// Remove flag from alice
		env.DisableDisallowIncomingPayChan(alice)
		env.Close()

		// Now a channel from cho to alice is allowed
		{
			seq := env.Seq(cho)
			chanK := chanKeylet(cho, alice, seq)
			result := env.Submit(ChannelCreate(cho, alice, xrp(1000), settleDelay, pk).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			require.True(t, chanExists(env, chanK))
		}
	})
}

// TestPayChan_CancelAfter tests the CancelAfter field behavior.
// From rippled: PayChan_test::testCancelAfter
func TestPayChan_CancelAfter(t *testing.T) {
	// If dst claims after cancel after, channel closes
	t.Run("DstClaimAfterCancelAfter", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		pk := alice.PublicKeyHex()
		settleDelay := uint32(100)
		cancelAfterTime := ToRippleTime(env.Now()) + 3600

		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])
		channelFunds := xrp(1000)

		result := env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).
			CancelAfterRipple(cancelAfterTime).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, chanExists(env, chanK))

		// Advance past cancelAfter
		env.AdvanceTime(3601 * time.Second)
		env.Close()

		// dst claims after cancelAfter → channel auto-closes
		chanBal := chanBalance(env, chanK)
		chanAmt := chanAmount(env, chanK)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		delta := xrp(500)
		reqBal := int64(chanBal) + delta
		authAmt := reqBal + xrp(100)
		_ = chanAmt

		sig := signClaimAuth(alice, chanIDHex, uint64(authAmt))
		result = env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqBal).Amount(authAmt).
			Signature(sig).PublicKey(pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.False(t, chanExists(env, chanK))
		require.Equal(t, preBob-10, env.Balance(bob))
		require.Equal(t, preAlice+uint64(channelFunds), env.Balance(alice))
	})

	// Third party can close after cancel after
	t.Run("ThirdPartyCloseAfterCancelAfter", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		carol := jtx.NewAccount("carol")

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(carol, uint64(jtx.XRP(10000)))
		env.Close()

		pk := alice.PublicKeyHex()
		settleDelay := uint32(100)
		cancelAfterTime := ToRippleTime(env.Now()) + 3600
		channelFunds := xrp(1000)

		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])

		result := env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).
			CancelAfterRipple(cancelAfterTime).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, chanExists(env, chanK))

		// Third party close before cancelAfter
		submitExpect(t, env, ChannelClaim(carol, chanIDHex).Close().Build(), "tecNO_PERMISSION")
		require.True(t, chanExists(env, chanK))

		// Advance past cancelAfter
		env.AdvanceTime(3601 * time.Second)
		env.Close()

		// Third party close after cancelAfter → succeeds
		preAlice := env.Balance(alice)
		result = env.Submit(ChannelClaim(carol, chanIDHex).Close().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.False(t, chanExists(env, chanK))
		require.Equal(t, preAlice+uint64(channelFunds), env.Balance(alice))
	})

	// fixPayChanCancelAfter: CancelAfter should be greater than close time
	t.Run("FixPayChanCancelAfter_LessThan", func(t *testing.T) {
		for _, withFix := range []bool{true, false} {
			label := "WithFix"
			if !withFix {
				label = "WithoutFix"
			}
			t.Run(label, func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				if !withFix {
					env.DisableFeature("fixPayChanCancelAfter")
				}

				alice := jtx.NewAccount("alice")
				bob := jtx.NewAccount("bob")
				env.FundAmount(alice, uint64(jtx.XRP(10000)))
				env.FundAmount(bob, uint64(jtx.XRP(10000)))
				env.Close()

				pk := alice.PublicKeyHex()
				settleDelay := uint32(100)
				channelFunds := xrp(1000)
				// CancelAfter < parentCloseTime
				cancelAfterTime := ToRippleTime(env.Now()) - 1

				expectedCode := "tesSUCCESS"
				if withFix {
					expectedCode = "tecEXPIRED"
				}
				submitExpect(t, env, ChannelCreate(alice, bob, channelFunds, settleDelay, pk).
					CancelAfterRipple(cancelAfterTime).Build(), expectedCode)
			})
		}
	})

	// fixPayChanCancelAfter: CancelAfter can be equal to the close time
	t.Run("FixPayChanCancelAfter_Equal", func(t *testing.T) {
		for _, withFix := range []bool{true, false} {
			label := "WithFix"
			if !withFix {
				label = "WithoutFix"
			}
			t.Run(label, func(t *testing.T) {
				env := jtx.NewTestEnv(t)
				if !withFix {
					env.DisableFeature("fixPayChanCancelAfter")
				}

				alice := jtx.NewAccount("alice")
				bob := jtx.NewAccount("bob")
				env.FundAmount(alice, uint64(jtx.XRP(10000)))
				env.FundAmount(bob, uint64(jtx.XRP(10000)))
				env.Close()

				pk := alice.PublicKeyHex()
				settleDelay := uint32(100)
				channelFunds := xrp(1000)
				// CancelAfter == parentCloseTime
				cancelAfterTime := ToRippleTime(env.Now())

				result := env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).
					CancelAfterRipple(cancelAfterTime).Build())
				jtx.RequireTxSuccess(t, result)
			})
		}
	})
}

// TestPayChan_SettleDelay tests the settle delay behavior.
// From rippled: PayChan_test::testSettleDelay
func TestPayChan_SettleDelay(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(3600)
	channelFunds := xrp(1000)

	// Compute settle timepoint before creating channel
	settleTimepoint := env.Now().Add(time.Duration(settleDelay) * time.Second)

	seq := env.Seq(alice)
	chanK := chanKeylet(alice, bob, seq)
	chanIDHex := hex.EncodeToString(chanK.Key[:])

	result := env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, chanExists(env, chanK))

	// Owner closes, will close after settleDelay
	result = env.Submit(ChannelClaim(alice, chanIDHex).Close().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, chanExists(env, chanK))

	// Advance to half of settle delay — receiver can still claim
	halfSettle := settleTimepoint.Add(-time.Duration(settleDelay/2) * time.Second)
	env.SetTime(halfSettle)
	env.Close()
	{
		chanBal := chanBalance(env, chanK)
		chanAmt := chanAmount(env, chanK)
		preBob := env.Balance(bob)
		delta := xrp(500)
		reqBal := int64(chanBal) + delta
		authAmt := reqBal + xrp(100)
		require.LessOrEqual(t, uint64(reqBal), chanAmt)

		sig := signClaimAuth(alice, chanIDHex, uint64(authAmt))
		result := env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqBal).Amount(authAmt).
			Signature(sig).PublicKey(pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint64(reqBal), chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
		require.Equal(t, preBob+uint64(delta)-10, env.Balance(bob))
	}

	// Advance past settle time — channel will close
	env.SetTime(settleTimepoint)
	env.Close()
	{
		chanBal := chanBalance(env, chanK)
		chanAmt := chanAmount(env, chanK)
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)
		delta := xrp(500)
		reqBal := int64(chanBal) + delta
		authAmt := reqBal + xrp(100)
		require.LessOrEqual(t, uint64(reqBal), chanAmt)

		sig := signClaimAuth(alice, chanIDHex, uint64(authAmt))
		result := env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqBal).Amount(authAmt).
			Signature(sig).PublicKey(pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.False(t, chanExists(env, chanK))
		require.Equal(t, preAlice+(chanAmt-chanBal), env.Balance(alice))
		require.Equal(t, preBob-10, env.Balance(bob))
	}
}

// TestPayChan_Expiration tests the Expiration field behavior.
// From rippled: PayChan_test::testExpiration
func TestPayChan_Expiration(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(3600)
	// Capture closeTime before create for CancelAfter (must be in the future)
	preCloseTime := ToRippleTime(env.Now())
	cancelAfterTime := preCloseTime + 7200
	channelFunds := xrp(1000)

	seq := env.Seq(alice)
	chanK := chanKeylet(alice, bob, seq)
	chanIDHex := hex.EncodeToString(chanK.Key[:])

	result := env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).
		CancelAfterRipple(cancelAfterTime).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, chanExists(env, chanK))

	// No expiration initially
	_, hasExp := chanExpiration(env, chanK)
	require.False(t, hasExp, "channel should not have expiration initially")

	// In rippled, all these operations happen in the same ledger with same parentCloseTime.
	// We capture closeTime right before the operations to match.
	closeTime := ToRippleTime(env.Now())
	minExpiration := closeTime + settleDelay

	// Owner closes, will close after settleDelay
	result = env.Submit(ChannelClaim(alice, chanIDHex).Close().Build())
	jtx.RequireTxSuccess(t, result)

	// Expiration should now be set to closeTime + settleDelay
	exp, hasExp := chanExpiration(env, chanK)
	require.True(t, hasExp, "channel should have expiration after owner close")
	require.Equal(t, minExpiration, exp, "expiration should equal minExpiration")

	// Increase the expiration time
	result = env.Submit(ChannelFund(alice, chanIDHex, drops(1_000_000)).
		ExpirationRipple(minExpiration + 100).Build())
	jtx.RequireTxSuccess(t, result)

	exp, hasExp = chanExpiration(env, chanK)
	require.True(t, hasExp)
	require.Equal(t, minExpiration+100, exp)

	// Decrease the expiration, but still above minExpiration
	result = env.Submit(ChannelFund(alice, chanIDHex, drops(1_000_000)).
		ExpirationRipple(minExpiration + 50).Build())
	jtx.RequireTxSuccess(t, result)

	exp, hasExp = chanExpiration(env, chanK)
	require.True(t, hasExp)
	require.Equal(t, minExpiration+50, exp)

	// Decrease expiration below minExpiration → temBAD_EXPIRATION
	submitExpect(t, env, ChannelFund(alice, chanIDHex, drops(1_000_000)).
		ExpirationRipple(minExpiration-50).Build(), "temBAD_EXPIRATION")

	exp, hasExp = chanExpiration(env, chanK)
	require.True(t, hasExp)
	require.Equal(t, minExpiration+50, exp)

	// Bob tries tfRenew → tecNO_PERMISSION
	submitExpect(t, env, ChannelClaim(bob, chanIDHex).Renew().Build(), "tecNO_PERMISSION")

	exp, hasExp = chanExpiration(env, chanK)
	require.True(t, hasExp)
	require.Equal(t, minExpiration+50, exp)

	// Owner renews → expiration cleared
	result = env.Submit(ChannelClaim(alice, chanIDHex).Renew().Build())
	jtx.RequireTxSuccess(t, result)

	_, hasExp = chanExpiration(env, chanK)
	require.False(t, hasExp, "expiration should be cleared after renew")

	// Decrease expiration below minExpiration after renew → temBAD_EXPIRATION
	submitExpect(t, env, ChannelFund(alice, chanIDHex, drops(1_000_000)).
		ExpirationRipple(minExpiration-50).Build(), "temBAD_EXPIRATION")

	_, hasExp = chanExpiration(env, chanK)
	require.False(t, hasExp)

	// Set expiration at minExpiration → success
	result = env.Submit(ChannelFund(alice, chanIDHex, drops(1_000_000)).
		ExpirationRipple(minExpiration).Build())
	jtx.RequireTxSuccess(t, result)

	exp, hasExp = chanExpiration(env, chanK)
	require.True(t, hasExp)
	require.Equal(t, minExpiration, exp)

	// Advance past expiration
	env.SetTime(time.Unix(int64(minExpiration)+RippleEpoch, 0))
	env.Close()

	// Try to extend the expiration after the expiration has already passed
	result = env.Submit(ChannelFund(alice, chanIDHex, drops(1_000_000)).
		ExpirationRipple(minExpiration + 1000).Build())
	// Channel auto-closes
	require.False(t, chanExists(env, chanK))
}

// TestPayChan_CloseDry tests closing a dry channel.
// From rippled: PayChan_test::testCloseDry
func TestPayChan_CloseDry(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(3600)
	channelFunds := xrp(1000)

	seq := env.Seq(alice)
	chanK := chanKeylet(alice, bob, seq)
	chanIDHex := hex.EncodeToString(chanK.Key[:])

	result := env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, chanExists(env, chanK))

	// Owner tries to close channel, but it will remain open (settle delay)
	result = env.Submit(ChannelClaim(alice, chanIDHex).Close().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, chanExists(env, chanK))

	// Claim the entire amount
	{
		preBob := env.Balance(bob)
		result := env.Submit(ChannelClaim(alice, chanIDHex).
			Balance(channelFunds).Amount(channelFunds).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint64(channelFunds), chanBalance(env, chanK))
		require.Equal(t, preBob+uint64(channelFunds), env.Balance(bob))
	}

	preAlice := env.Balance(alice)
	// Channel is now dry, can close before expiration date
	result = env.Submit(ChannelClaim(alice, chanIDHex).Close().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	require.False(t, chanExists(env, chanK))
	require.Equal(t, preAlice-10, env.Balance(alice))
}

// TestPayChan_DefaultAmount tests that auth amount defaults to balance if not present.
// From rippled: PayChan_test::testDefaultAmount
func TestPayChan_DefaultAmount(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(3600)
	channelFunds := xrp(1000)

	seq := env.Seq(alice)
	chanK := chanKeylet(alice, bob, seq)
	chanIDHex := hex.EncodeToString(chanK.Key[:])

	result := env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, chanExists(env, chanK))

	// Owner tries to close channel, but it will remain open (settle delay)
	result = env.Submit(ChannelClaim(alice, chanIDHex).Close().Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, chanExists(env, chanK))

	// First claim: auth amount defaults to balance (std::nullopt in C++)
	{
		chanBal := chanBalance(env, chanK)
		preBob := env.Balance(bob)
		delta := xrp(500)
		reqBal := int64(chanBal) + delta

		// Auth amount == reqBal (rippled defaults auth amount to balance when omitted)
		sig := signClaimAuth(alice, chanIDHex, uint64(reqBal))
		result := env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqBal).
			Signature(sig).PublicKey(pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint64(reqBal), chanBalance(env, chanK))
		require.Equal(t, preBob+uint64(delta)-10, env.Balance(bob))
	}

	// Second claim
	{
		chanBal := chanBalance(env, chanK)
		preBob := env.Balance(bob)
		delta := xrp(500)
		reqBal := int64(chanBal) + delta

		sig := signClaimAuth(alice, chanIDHex, uint64(reqBal))
		result := env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqBal).
			Signature(sig).PublicKey(pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint64(reqBal), chanBalance(env, chanK))
		require.Equal(t, preBob+uint64(delta)-10, env.Balance(bob))
	}
}

// TestPayChan_DisallowXRP tests the DisallowXRP flag behavior.
// From rippled: PayChan_test::testDisallowXRP
func TestPayChan_DisallowXRP(t *testing.T) {
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	// Create channel where dst disallows XRP (without DepositAuth)
	t.Run("CreateWithoutDepositAuth", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("DepositAuth")

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		result := env.Submit(accountset.AccountSet(bob).DisallowXRP().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)

		submitExpect(t, env, ChannelCreate(alice, bob, xrp(1000), 3600, alice.PublicKeyHex()).Build(), "tecNO_TARGET")
		require.False(t, chanExists(env, chanK))
	})

	// Create channel where dst disallows XRP — with DepositAuth (advisory)
	t.Run("CreateWithDepositAuth", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		result := env.Submit(accountset.AccountSet(bob).DisallowXRP().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)

		result = env.Submit(ChannelCreate(alice, bob, xrp(1000), 3600, alice.PublicKeyHex()).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, chanExists(env, chanK))
	})

	// Claim to a channel where dst disallows XRP (without DepositAuth)
	t.Run("ClaimWithoutDepositAuth", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("DepositAuth")

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])

		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), 3600, alice.PublicKeyHex()).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, chanExists(env, chanK))

		result = env.Submit(accountset.AccountSet(bob).DisallowXRP().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		reqBal := xrp(500)
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).
			Balance(reqBal).Amount(reqBal).Build(), "tecNO_TARGET")
	})

	// Claim to a channel where dst disallows XRP — with DepositAuth (advisory)
	t.Run("ClaimWithDepositAuth", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])

		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), 3600, alice.PublicKeyHex()).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, chanExists(env, chanK))

		result = env.Submit(accountset.AccountSet(bob).DisallowXRP().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		reqBal := xrp(500)
		result = env.Submit(ChannelClaim(alice, chanIDHex).
			Balance(reqBal).Amount(reqBal).Build())
		jtx.RequireTxSuccess(t, result)
	})
}

// TestPayChan_DstTag tests the DestinationTag behavior.
// From rippled: PayChan_test::testDstTag
func TestPayChan_DstTag(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	env.EnableRequireDest(bob)
	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(3600)
	channelFunds := xrp(1000)

	// Without destination tag → tecDST_TAG_NEEDED
	{
		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		submitExpect(t, env, ChannelCreate(alice, bob, channelFunds, settleDelay, pk).Build(), "tecDST_TAG_NEEDED")
		require.False(t, chanExists(env, chanK))
	}

	// With destination tag → success
	{
		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		result := env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).DestTag(1).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, chanExists(env, chanK))
	}
}

// TestPayChan_DepositAuth tests the DepositAuth flag behavior.
// From rippled: PayChan_test::testDepositAuth
func TestPayChan_DepositAuth(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.Close()

	env.EnableDepositAuth(bob)
	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(100)
	seq := env.Seq(alice)
	chanK := chanKeylet(alice, bob, seq)
	chanIDHex := hex.EncodeToString(chanK.Key[:])

	result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	require.Equal(t, uint64(0), chanBalance(env, chanK))
	require.Equal(t, uint64(xrp(1000)), chanAmount(env, chanK))

	// Alice can add more funds even though bob has DepositAuth
	result = env.Submit(ChannelFund(alice, chanIDHex, xrp(1000)).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	// alice claims. Fails because bob's lsfDepositAuth flag is set.
	submitExpect(t, env, ChannelClaim(alice, chanIDHex).
		Balance(xrp(500)).Amount(xrp(500)).Build(), "tecNO_PERMISSION")
	env.Close()

	// Claim with signature
	baseFee := uint64(10)
	preBob := env.Balance(bob)
	{
		delta := xrp(500)
		sig := signClaimAuth(alice, chanIDHex, uint64(delta))

		// alice claims with signature. Fails since bob has lsfDepositAuth.
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).
			Signature(sig).PublicKey(pk).Build(), "tecNO_PERMISSION")
		env.Close()
		require.Equal(t, preBob, env.Balance(bob))

		// bob claims but omits the signature. Fails because only alice can claim without sig.
		submitExpect(t, env, ChannelClaim(bob, chanIDHex).
			Balance(delta).Amount(delta).Build(), "temBAD_SIGNATURE")
		env.Close()

		// bob claims with signature. Succeeds since bob submitted the transaction.
		result = env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(delta).Amount(delta).
			Signature(sig).PublicKey(pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.Equal(t, preBob+uint64(delta)-baseFee, env.Balance(bob))
	}

	{
		// Explore the limits of deposit preauthorization
		delta := xrp(600)
		sig := signClaimAuth(alice, chanIDHex, uint64(delta))

		// carol claims and fails. Only channel participants may claim.
		submitExpect(t, env, ChannelClaim(carol, chanIDHex).
			Balance(delta).Amount(delta).
			Signature(sig).PublicKey(pk).Build(), "tecNO_PERMISSION")
		env.Close()

		// bob preauthorizes carol for deposit. But carol still can't claim.
		env.Preauthorize(bob, carol)
		env.Close()

		submitExpect(t, env, ChannelClaim(carol, chanIDHex).
			Balance(delta).Amount(delta).
			Signature(sig).PublicKey(pk).Build(), "tecNO_PERMISSION")

		// Since alice is not preauthorized she also may not claim for bob.
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).
			Signature(sig).PublicKey(pk).Build(), "tecNO_PERMISSION")
		env.Close()

		// bob preauthorizes alice → she can now submit a claim
		env.Preauthorize(bob, alice)
		env.Close()

		result = env.Submit(ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).
			Signature(sig).PublicKey(pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, preBob+uint64(delta)-(3*baseFee), env.Balance(bob))
	}

	{
		// bob removes preauthorization of alice
		delta := xrp(800)

		env.Unauthorize(bob, alice)
		env.Close()

		// alice claims and fails since she is no longer preauthorized
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).Build(), "tecNO_PERMISSION")
		env.Close()

		// bob clears lsfDepositAuth. Now alice can claim.
		env.DisableDepositAuth(bob)
		env.Close()

		// alice claims successfully
		result = env.Submit(ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.Equal(t, preBob+uint64(xrp(800))-(5*baseFee), env.Balance(bob))
	}
}

// TestPayChan_DepositAuthCreds tests deposit auth with credentials.
// From rippled: PayChan_test::testDepositAuthCreds
func TestPayChan_DepositAuthCreds(t *testing.T) {
	credType := "abcde"

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	dillon := jtx.NewAccount("dillon")
	zelda := jtx.NewAccount("zelda")

	// Main test
	t.Run("MainFlow", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(carol, uint64(jtx.XRP(10000)))
		env.FundAmount(dillon, uint64(jtx.XRP(10000)))
		env.FundAmount(zelda, uint64(jtx.XRP(10000)))
		env.Close()

		pk := alice.PublicKeyHex()
		settleDelay := uint32(100)
		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])

		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Alice adds funds
		result = env.Submit(ChannelFund(alice, chanIDHex, xrp(1000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		credBadIdx := "D007AE4B6E1274B4AF872588267B810C2F82716726351D1C7D38D3E5499FC6E1"
		delta := xrp(500)

		// Create credentials with expiration
		{
			expTime := ToRippleTime(env.Now()) + 100
			result := env.Submit(credential.CredentialCreate(carol, alice, credType).
				Expiration(expTime).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
		}

		credIdx := depositpreauth.CredentialIndex(alice, carol, credType)

		// Bob requires preauthorization
		env.EnableDepositAuth(bob)
		env.Close()

		// Fail, credentials not accepted
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).
			CredentialIDs([]string{credIdx}).Build(), "tecBAD_CREDENTIALS")
		env.Close()

		// Accept credentials
		result = env.Submit(credential.CredentialAccept(alice, carol, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Fail, no depositPreauth object
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).
			CredentialIDs([]string{credIdx}).Build(), "tecNO_PERMISSION")
		env.Close()

		// Setup deposit authorization with credentials
		result = env.Submit(depositpreauth.AuthCredentials(bob, []depositpreauth.AuthorizeCredentials{
			{Issuer: carol, CredType: credType},
		}).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Fail, credentials don't belong to dillon
		submitExpect(t, env, ChannelClaim(dillon, chanIDHex).
			Balance(delta).Amount(delta).
			CredentialIDs([]string{credIdx}).Build(), "tecBAD_CREDENTIALS")

		// Fails because bob's lsfDepositAuth flag is set (no credentials provided)
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).Build(), "tecNO_PERMISSION")

		// Fail, bad credentials index
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).
			CredentialIDs([]string{credBadIdx}).Build(), "tecBAD_CREDENTIALS")

		// Fail, empty credentials
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).
			CredentialIDs([]string{}).Build(), "temMALFORMED")

		// Claim fails because of expired credentials
		{
			// Advance time past credential expiration (every Close ~+10sec)
			for i := 0; i < 10; i++ {
				env.Close()
			}

			submitExpect(t, env, ChannelClaim(alice, chanIDHex).
				Balance(delta).Amount(delta).
				CredentialIDs([]string{credIdx}).Build(), "tecEXPIRED")
			env.Close()
		}

		// Create credentials once more (without expiration)
		{
			result := env.Submit(credential.CredentialCreate(carol, alice, credType).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			result = env.Submit(credential.CredentialAccept(alice, carol, credType).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			credIdx2 := depositpreauth.CredentialIndex(alice, carol, credType)

			// Success
			result = env.Submit(ChannelClaim(alice, chanIDHex).
				Balance(delta).Amount(delta).
				CredentialIDs([]string{credIdx2}).Build())
			jtx.RequireTxSuccess(t, result)
		}
	})

	// Succeed without DepositAuth set (credentials are just advisory)
	t.Run("WithoutDepositAuth", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(carol, uint64(jtx.XRP(10000)))
		env.FundAmount(dillon, uint64(jtx.XRP(10000)))
		env.FundAmount(zelda, uint64(jtx.XRP(10000)))
		env.Close()

		pk := alice.PublicKeyHex()
		settleDelay := uint32(100)
		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])

		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(ChannelFund(alice, chanIDHex, xrp(1000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		delta := xrp(500)

		// Create and accept credentials
		result = env.Submit(credential.CredentialCreate(carol, alice, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(credential.CredentialAccept(alice, carol, credType).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		credIdx := depositpreauth.CredentialIndex(alice, carol, credType)

		// Succeed, lsfDepositAuth is not set
		result = env.Submit(ChannelClaim(alice, chanIDHex).
			Balance(delta).Amount(delta).
			CredentialIDs([]string{credIdx}).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})

	// Credentials amendment not enabled
	t.Run("AmendmentDisabled", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("Credentials")

		env.FundAmount(alice, uint64(jtx.XRP(5000)))
		env.FundAmount(bob, uint64(jtx.XRP(5000)))
		env.Close()

		pk := alice.PublicKeyHex()
		settleDelay := uint32(100)
		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])

		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		result = env.Submit(ChannelFund(alice, chanIDHex, xrp(1000)).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		credIdx := "48004829F915654A81B11C4AB8218D96FED67F209B58328A72314FB6EA288BE4"

		// Set DepositAuth and preauth alice
		env.EnableDepositAuth(bob)
		env.Close()
		env.Preauthorize(bob, alice)
		env.Close()

		// Can't use credentials — feature disabled
		submitExpect(t, env, ChannelClaim(alice, chanIDHex).
			Balance(xrp(500)).Amount(xrp(500)).
			CredentialIDs([]string{credIdx}).Build(), "temDISABLED")
	})
}

// TestPayChan_Multiple tests multiple channels to the same account.
// From rippled: PayChan_test::testMultiple
func TestPayChan_Multiple(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(3600)
	channelFunds := xrp(1000)

	seq1 := env.Seq(alice)
	chanK1 := chanKeylet(alice, bob, seq1)
	result := env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, chanExists(env, chanK1))

	seq2 := env.Seq(alice)
	chanK2 := chanKeylet(alice, bob, seq2)
	result = env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	require.True(t, chanExists(env, chanK2))

	require.NotEqual(t, chanK1.Key, chanK2.Key)
}

// TestPayChan_AccountChannelsRPC tests the account_channels RPC method.
// From rippled: PayChan_test::testAccountChannelsRPC
func TestPayChan_AccountChannelsRPC(t *testing.T) {
	t.Skip("RPC test - requires server setup")
}

// TestPayChan_AccountChannelsRPCMarkers tests account_channels with markers.
// From rippled: PayChan_test::testAccountChannelsRPCMarkers
func TestPayChan_AccountChannelsRPCMarkers(t *testing.T) {
	t.Skip("RPC test - requires server setup")
}

// TestPayChan_AccountChannelsRPCSenderOnly tests account_channels with sender only.
// From rippled: PayChan_test::testAccountChannelsRPCSenderOnly
func TestPayChan_AccountChannelsRPCSenderOnly(t *testing.T) {
	t.Skip("RPC test - requires server setup")
}

// TestPayChan_AccountChannelAuthorize tests channel_authorize RPC method.
// From rippled: PayChan_test::testAccountChannelAuthorize
func TestPayChan_AccountChannelAuthorize(t *testing.T) {
	t.Skip("RPC test - requires server setup")
}

// TestPayChan_AuthVerifyRPC tests channel_verify RPC method.
// From rippled: PayChan_test::testAuthVerifyRPC
func TestPayChan_AuthVerifyRPC(t *testing.T) {
	t.Skip("RPC test - requires server setup")
}

// TestPayChan_OptionalFields tests optional fields in payment channels.
// From rippled: PayChan_test::testOptionalFields
func TestPayChan_OptionalFields(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.FundAmount(carol, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(3600)
	channelFunds := xrp(1000)

	// Channel without destination tag
	{
		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		result := env.Submit(ChannelCreate(alice, bob, channelFunds, settleDelay, pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, chanExists(env, chanK))
	}

	// Channel with destination tag = 42
	{
		seq := env.Seq(alice)
		chanK := chanKeylet(alice, carol, seq)
		result := env.Submit(ChannelCreate(alice, carol, channelFunds, settleDelay, pk).
			DestTag(42).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		require.True(t, chanExists(env, chanK))
	}
}

// TestPayChan_MalformedPK tests malformed public key handling.
// From rippled: PayChan_test::testMalformedPK
func TestPayChan_MalformedPK(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(100)

	// Create: pk missing first 2 chars
	submitExpect(t, env, ChannelCreate(alice, bob, xrp(1000), settleDelay, pk[2:]).Build(), "temMALFORMED")

	// Create: pk missing last 2 chars
	submitExpect(t, env, ChannelCreate(alice, bob, xrp(1000), settleDelay, pk[:len(pk)-2]).Build(), "temMALFORMED")

	// Create: bad prefix (ff)
	badPrefix := "ff" + pk[2:]
	submitExpect(t, env, ChannelCreate(alice, bob, xrp(1000), settleDelay, badPrefix).Build(), "temMALFORMED")

	// Create with valid pk → success
	seq := env.Seq(alice)
	chanK := chanKeylet(alice, bob, seq)
	chanIDHex := hex.EncodeToString(chanK.Key[:])
	result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()

	authAmt := xrp(100)
	sig := signClaimAuth(alice, chanIDHex, uint64(authAmt))

	// Claim: pk missing first 2 chars
	submitExpect(t, env, ChannelClaim(bob, chanIDHex).
		Balance(authAmt).Amount(authAmt).
		Signature(sig).PublicKey(pk[2:]).Build(), "temMALFORMED")

	// Claim: pk missing last 2 chars
	submitExpect(t, env, ChannelClaim(bob, chanIDHex).
		Balance(authAmt).Amount(authAmt).
		Signature(sig).PublicKey(pk[:len(pk)-2]).Build(), "temMALFORMED")

	// Claim: bad prefix (ff)
	submitExpect(t, env, ChannelClaim(bob, chanIDHex).
		Balance(authAmt).Amount(authAmt).
		Signature(sig).PublicKey(badPrefix).Build(), "temMALFORMED")

	// Claim: missing public key (has sig but no PK)
	submitExpect(t, env, ChannelClaim(bob, chanIDHex).
		Balance(authAmt).Amount(authAmt).
		Signature(sig).Build(), "temMALFORMED")
}

// TestPayChan_MetaAndOwnership tests metadata and ownership behavior.
// From rippled: PayChan_test::testMetaAndOwnership
func TestPayChan_MetaAndOwnership(t *testing.T) {
	pk := jtx.NewAccount("alice").PublicKeyHex()
	settleDelay := uint32(100)

	// Without fixPayChanRecipientOwnerDir: channel only in sender's dir
	t.Run("WithoutFixRecipientOwnerDir", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixPayChanRecipientOwnerDir")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])

		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.True(t, inOwnerDir(env, alice, chanK.Key))
		require.Equal(t, 1, ownerDirCount(env, alice))
		require.False(t, inOwnerDir(env, bob, chanK.Key))
		require.Equal(t, 0, ownerDirCount(env, bob))

		// Close the channel
		result = env.Submit(ChannelClaim(bob, chanIDHex).Close().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.False(t, chanExists(env, chanK))
		require.False(t, inOwnerDir(env, alice, chanK.Key))
		require.Equal(t, 0, ownerDirCount(env, alice))
		require.False(t, inOwnerDir(env, bob, chanK.Key))
		require.Equal(t, 0, ownerDirCount(env, bob))
	})

	// With fixPayChanRecipientOwnerDir: channel in both dirs
	t.Run("WithFixRecipientOwnerDir", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])

		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.True(t, inOwnerDir(env, alice, chanK.Key))
		require.Equal(t, 1, ownerDirCount(env, alice))
		require.True(t, inOwnerDir(env, bob, chanK.Key))
		require.Equal(t, 1, ownerDirCount(env, bob))

		// Close the channel
		result = env.Submit(ChannelClaim(bob, chanIDHex).Close().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.False(t, chanExists(env, chanK))
		require.False(t, inOwnerDir(env, alice, chanK.Key))
		require.Equal(t, 0, ownerDirCount(env, alice))
		require.False(t, inOwnerDir(env, bob, chanK.Key))
		require.Equal(t, 0, ownerDirCount(env, bob))
	})

	// Migration: created before fix, closed after fix
	t.Run("MigrationBeforeAfterFix", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixPayChanRecipientOwnerDir")

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		// Create channel before amendment activates
		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])

		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.True(t, inOwnerDir(env, alice, chanK.Key))
		require.Equal(t, 1, ownerDirCount(env, alice))
		require.False(t, inOwnerDir(env, bob, chanK.Key))
		require.Equal(t, 0, ownerDirCount(env, bob))

		// Enable the amendment
		env.EnableFeature("fixPayChanRecipientOwnerDir")
		env.Close()

		require.True(t, inOwnerDir(env, alice, chanK.Key))
		require.False(t, inOwnerDir(env, bob, chanK.Key))
		require.Equal(t, 0, ownerDirCount(env, bob))

		// Close the channel after the amendment activates
		result = env.Submit(ChannelClaim(bob, chanIDHex).Close().Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.False(t, chanExists(env, chanK))
		require.False(t, inOwnerDir(env, alice, chanK.Key))
		require.Equal(t, 0, ownerDirCount(env, alice))
		require.False(t, inOwnerDir(env, bob, chanK.Key))
		require.Equal(t, 0, ownerDirCount(env, bob))
	})
}

// TestPayChan_AccountDelete tests account delete with payment channels.
// From rippled: PayChan_test::testAccountDelete
func TestPayChan_AccountDelete(t *testing.T) {
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")

	for _, withOwnerDirFix := range []bool{false, true} {
		label := "WithoutOwnerDirFix"
		if withOwnerDirFix {
			label = "WithOwnerDirFix"
		}
		t.Run(label, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			if !withOwnerDirFix {
				env.DisableFeature("fixPayChanRecipientOwnerDir")
			}

			env.FundAmount(alice, uint64(jtx.XRP(10000)))
			env.FundAmount(bob, uint64(jtx.XRP(10000)))
			env.FundAmount(carol, uint64(jtx.XRP(10000)))
			env.Close()

			pk := alice.PublicKeyHex()
			settleDelay := uint32(100)

			seq := env.Seq(alice)
			chanK := chanKeylet(alice, bob, seq)
			chanIDHex := hex.EncodeToString(chanK.Key[:])

			result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			require.Equal(t, uint64(0), chanBalance(env, chanK))
			require.Equal(t, uint64(xrp(1000)), chanAmount(env, chanK))

			// Alice can't be deleted because she owns the channel
			rmAccount(t, env, alice, carol, "tecHAS_OBLIGATIONS")

			// Bob can only be removed if the channel isn't in their owner directory
			if withOwnerDirFix {
				rmAccount(t, env, bob, carol, "tecHAS_OBLIGATIONS")
			} else {
				rmAccount(t, env, bob, carol, "tesSUCCESS")
			}

			chanBal := chanBalance(env, chanK)
			chanAmt := chanAmount(env, chanK)
			require.Equal(t, uint64(0), chanBal)
			require.Equal(t, uint64(xrp(1000)), chanAmt)

			preBob := env.Balance(bob)
			delta := xrp(50)
			reqBal := int64(chanBal) + delta
			authAmt := reqBal + xrp(100)
			baseFee := uint64(10)

			// Claim should fail if the dst was removed
			if withOwnerDirFix {
				result = env.Submit(ChannelClaim(alice, chanIDHex).
					Balance(reqBal).Amount(authAmt).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				require.Equal(t, uint64(reqBal), chanBalance(env, chanK))
				require.Equal(t, chanAmt, chanAmount(env, chanK))
				require.Equal(t, preBob+uint64(delta), env.Balance(bob))
				chanBal = uint64(reqBal)
			} else {
				preAlice := env.Balance(alice)
				submitExpect(t, env, ChannelClaim(alice, chanIDHex).
					Balance(reqBal).Amount(authAmt).Build(), "tecNO_DST")
				env.Close()

				require.Equal(t, chanBal, chanBalance(env, chanK))
				require.Equal(t, chanAmt, chanAmount(env, chanK))
				require.Equal(t, preBob, env.Balance(bob))
				require.Equal(t, preAlice-baseFee, env.Balance(alice))
			}

			// Fund should fail if the dst was removed
			if withOwnerDirFix {
				preAlice := env.Balance(alice)
				result = env.Submit(ChannelFund(alice, chanIDHex, xrp(1000)).Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				require.Equal(t, preAlice-uint64(xrp(1000))-baseFee, env.Balance(alice))
				require.Equal(t, chanAmt+uint64(xrp(1000)), chanAmount(env, chanK))
				chanAmt = chanAmt + uint64(xrp(1000))
			} else {
				preAlice := env.Balance(alice)
				submitExpect(t, env, ChannelFund(alice, chanIDHex, xrp(1000)).Build(), "tecNO_DST")
				env.Close()

				require.Equal(t, preAlice-baseFee, env.Balance(alice))
				require.Equal(t, chanAmt, chanAmount(env, chanK))
			}

			// Owner closes, will close after settleDelay
			{
				result = env.Submit(ChannelClaim(alice, chanIDHex).Close().Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()

				// settle delay hasn't elapsed. Channel should exist.
				require.True(t, chanExists(env, chanK))

				// Advance past settle delay
				env.AdvanceTime(time.Duration(settleDelay+10) * time.Second)
				env.Close()

				result = env.Submit(ChannelClaim(alice, chanIDHex).Close().Build())
				jtx.RequireTxSuccess(t, result)
				env.Close()
				require.False(t, chanExists(env, chanK))
			}
		})
	}

	// Test resurrected account
	t.Run("ResurrectedAccount", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixPayChanRecipientOwnerDir")

		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.FundAmount(carol, uint64(jtx.XRP(10000)))
		env.Close()

		pk := alice.PublicKeyHex()
		settleDelay := uint32(100)

		seq := env.Seq(alice)
		chanK := chanKeylet(alice, bob, seq)
		chanIDHex := hex.EncodeToString(chanK.Key[:])

		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()

		require.Equal(t, uint64(0), chanBalance(env, chanK))
		require.Equal(t, uint64(xrp(1000)), chanAmount(env, chanK))

		// Since fixPayChanRecipientOwnerDir is not active, can remove bob
		rmAccount(t, env, bob, carol, "tesSUCCESS")
		require.False(t, env.Exists(bob))

		baseFee := uint64(10)
		chanBal := chanBalance(env, chanK)
		chanAmt := chanAmount(env, chanK)
		require.Equal(t, uint64(0), chanBal)
		require.Equal(t, uint64(xrp(1000)), chanAmt)
		preBob := env.Balance(bob)
		delta := xrp(50)
		reqBal := int64(chanBal) + delta
		authAmt := reqBal + xrp(100)

		// Claim should fail since bob doesn't exist
		{
			preAlice := env.Balance(alice)
			submitExpect(t, env, ChannelClaim(alice, chanIDHex).
				Balance(reqBal).Amount(authAmt).Build(), "tecNO_DST")
			env.Close()

			require.Equal(t, chanBal, chanBalance(env, chanK))
			require.Equal(t, chanAmt, chanAmount(env, chanK))
			require.Equal(t, preBob, env.Balance(bob))
			require.Equal(t, preAlice-baseFee, env.Balance(alice))
		}

		// Fund should fail since bob doesn't exist
		{
			preAlice := env.Balance(alice)
			submitExpect(t, env, ChannelFund(alice, chanIDHex, xrp(1000)).Build(), "tecNO_DST")
			env.Close()

			require.Equal(t, preAlice-baseFee, env.Balance(alice))
			require.Equal(t, chanAmt, chanAmount(env, chanK))
		}

		// Resurrect bob (needs at least reserve base = 200 XRP)
		env.Pay(bob, uint64(jtx.XRP(250)))
		env.Close()
		require.True(t, env.Exists(bob))

		// Alice should be able to claim
		{
			preBob = env.Balance(bob)
			reqBal = int64(chanBal) + delta
			authAmt = reqBal + xrp(100)

			result = env.Submit(ChannelClaim(alice, chanIDHex).
				Balance(reqBal).Amount(authAmt).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			require.Equal(t, uint64(reqBal), chanBalance(env, chanK))
			require.Equal(t, chanAmt, chanAmount(env, chanK))
			require.Equal(t, preBob+uint64(delta), env.Balance(bob))
			chanBal = uint64(reqBal)
		}

		// Bob should be able to claim with signature
		{
			preBob = env.Balance(bob)
			reqBal2 := int64(chanBal) + delta
			authAmt2 := reqBal2 + xrp(100)

			sig := signClaimAuth(alice, chanIDHex, uint64(authAmt2))
			result = env.Submit(ChannelClaim(bob, chanIDHex).
				Balance(reqBal2).Amount(authAmt2).
				Signature(sig).PublicKey(pk).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			require.Equal(t, uint64(reqBal2), chanBalance(env, chanK))
			require.Equal(t, chanAmt, chanAmount(env, chanK))
			require.Equal(t, preBob+uint64(delta)-baseFee, env.Balance(bob))
			chanBal = uint64(reqBal2)
		}

		// Alice should be able to fund
		{
			preAlice := env.Balance(alice)
			result = env.Submit(ChannelFund(alice, chanIDHex, xrp(1000)).Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			require.Equal(t, preAlice-uint64(xrp(1000))-baseFee, env.Balance(alice))
			require.Equal(t, chanAmt+uint64(xrp(1000)), chanAmount(env, chanK))
			chanAmt = chanAmt + uint64(xrp(1000))
		}

		// Owner closes, will close after settleDelay
		{
			result = env.Submit(ChannelClaim(alice, chanIDHex).Close().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()

			// settle delay hasn't elapsed
			require.True(t, chanExists(env, chanK))

			env.AdvanceTime(time.Duration(settleDelay+10) * time.Second)
			env.Close()

			result = env.Submit(ChannelClaim(alice, chanIDHex).Close().Build())
			jtx.RequireTxSuccess(t, result)
			env.Close()
			require.False(t, chanExists(env, chanK))
		}
	})
}

// TestPayChan_UsingTickets tests using tickets with payment channels.
// From rippled: PayChan_test::testUsingTickets
func TestPayChan_UsingTickets(t *testing.T) {
	env := jtx.NewTestEnv(t)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	// Alice and bob grab enough tickets
	aliceTicketSeq := env.CreateTickets(alice, 10)
	aliceSeq := env.Seq(alice)

	bobTicketSeq := env.CreateTickets(bob, 10)
	bobSeq := env.Seq(bob)

	env.Close()

	pk := alice.PublicKeyHex()
	settleDelay := uint32(100)
	chanK := chanKeylet(alice, bob, aliceTicketSeq)
	chanIDHex := hex.EncodeToString(chanK.Key[:])

	// Create channel with ticket
	result := env.Submit(ChannelCreate(alice, bob, xrp(1000), settleDelay, pk).
		Ticket(aliceTicketSeq).Build())
	jtx.RequireTxSuccess(t, result)
	env.Close()
	aliceTicketSeq++

	require.Equal(t, aliceSeq, env.Seq(alice), "Alice seq should not advance")
	require.Equal(t, uint64(0), chanBalance(env, chanK))
	require.Equal(t, uint64(xrp(1000)), chanAmount(env, chanK))

	// Fund with ticket
	{
		preAlice := env.Balance(alice)
		result := env.Submit(ChannelFund(alice, chanIDHex, xrp(1000)).
			Ticket(aliceTicketSeq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		aliceTicketSeq++

		require.Equal(t, aliceSeq, env.Seq(alice))
		require.Equal(t, preAlice-uint64(xrp(1000))-10, env.Balance(alice))
	}

	chanBal := chanBalance(env, chanK)
	chanAmt := chanAmount(env, chanK)
	require.Equal(t, uint64(0), chanBal)
	require.Equal(t, uint64(xrp(2000)), chanAmt)

	// No signature needed since the owner is claiming (with ticket)
	{
		preBob := env.Balance(bob)
		delta := xrp(500)
		reqBal := int64(chanBal) + delta
		authAmt := reqBal + xrp(100)

		result := env.Submit(ChannelClaim(alice, chanIDHex).
			Balance(reqBal).Amount(authAmt).
			Ticket(aliceTicketSeq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		aliceTicketSeq++

		require.Equal(t, aliceSeq, env.Seq(alice))
		require.Equal(t, uint64(reqBal), chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
		require.Equal(t, preBob+uint64(delta), env.Balance(bob))
		chanBal = uint64(reqBal)
	}

	// Claim with signature (bob uses ticket)
	{
		preBob := env.Balance(bob)
		delta := xrp(500)
		reqBal := int64(chanBal) + delta
		authAmt := reqBal + xrp(100)

		sig := signClaimAuth(alice, chanIDHex, uint64(authAmt))
		result := env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqBal).Amount(authAmt).
			Signature(sig).PublicKey(pk).
			Ticket(bobTicketSeq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		bobTicketSeq++

		require.Equal(t, bobSeq, env.Seq(bob))
		require.Equal(t, uint64(reqBal), chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
		require.Equal(t, preBob+uint64(delta)-10, env.Balance(bob))
		chanBal = uint64(reqBal)

		// Claim again same amount → tecUNFUNDED_PAYMENT (ticket consumed)
		preBob = env.Balance(bob)
		result = env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqBal).Amount(authAmt).
			Signature(sig).PublicKey(pk).
			Ticket(bobTicketSeq).Build())
		require.Equal(t, "tecUNFUNDED_PAYMENT", result.Code)
		env.Close()
		bobTicketSeq++

		require.Equal(t, bobSeq, env.Seq(bob))
		require.Equal(t, chanBal, chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
		require.Equal(t, preBob-10, env.Balance(bob))
	}

	// Try to claim more than authorized → temBAD_AMOUNT (ticket NOT consumed)
	{
		preBob := env.Balance(bob)
		authAmt := int64(chanBal) + xrp(500)
		reqAmt := authAmt + 1

		sig := signClaimAuth(alice, chanIDHex, uint64(authAmt))
		result := env.Submit(ChannelClaim(bob, chanIDHex).
			Balance(reqAmt).Amount(authAmt).
			Signature(sig).PublicKey(pk).
			Ticket(bobTicketSeq).Build())
		require.Equal(t, "temBAD_AMOUNT", result.Code)
		// Note: tem doesn't consume ticket, so we don't increment bobTicketSeq

		require.Equal(t, bobSeq, env.Seq(bob))
		require.Equal(t, chanBal, chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
		require.Equal(t, preBob, env.Balance(bob))
	}

	// Dst tries to fund the channel (ticket consumed)
	{
		result := env.Submit(ChannelFund(bob, chanIDHex, xrp(1000)).
			Ticket(bobTicketSeq).Build())
		require.Equal(t, "tecNO_PERMISSION", result.Code)
		env.Close()
		bobTicketSeq++

		require.Equal(t, bobSeq, env.Seq(bob))
		require.Equal(t, chanBal, chanBalance(env, chanK))
		require.Equal(t, chanAmt, chanAmount(env, chanK))
	}

	// Dst closes channel (with ticket)
	{
		preAlice := env.Balance(alice)
		preBob := env.Balance(bob)

		result := env.Submit(ChannelClaim(bob, chanIDHex).Close().
			Ticket(bobTicketSeq).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
		bobTicketSeq++

		require.Equal(t, bobSeq, env.Seq(bob))
		require.False(t, chanExists(env, chanK))
		delta := chanAmt - chanBal
		require.Equal(t, preAlice+delta, env.Balance(alice))
		require.Equal(t, preBob-10, env.Balance(bob))
	}

	require.Equal(t, aliceSeq, env.Seq(alice))
	require.Equal(t, bobSeq, env.Seq(bob))
}

// TestEnabled tests that PayChan operations are disabled without the PayChan amendment.
func TestEnabled(t *testing.T) {
	t.Run("Disabled", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		env.DisableFeature("PayChan")

		submitExpect(t, env, ChannelCreate(alice, bob, xrp(1000), 100, alice.PublicKeyHex()).Build(), "temDISABLED")

		fakeChannelID := "0000000000000000000000000000000000000000000000000000000000000000"
		submitExpect(t, env, ChannelFund(alice, fakeChannelID, xrp(100)).Build(), "temDISABLED")
		submitExpect(t, env, ChannelClaim(alice, fakeChannelID).Build(), "temDISABLED")
	})

	t.Run("Enabled", func(t *testing.T) {
		env := jtx.NewTestEnv(t)

		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(alice, uint64(jtx.XRP(10000)))
		env.FundAmount(bob, uint64(jtx.XRP(10000)))
		env.Close()

		result := env.Submit(ChannelCreate(alice, bob, xrp(1000), 100, alice.PublicKeyHex()).Build())
		jtx.RequireTxSuccess(t, result)
		env.Close()
	})
}

// submitExpect submits a transaction and asserts the expected result code.
func submitExpect(t *testing.T, env *jtx.TestEnv, txn tx.Transaction, expectedCode string) {
	t.Helper()
	result := env.Submit(txn)
	require.Equal(t, expectedCode, result.Code,
		fmt.Sprintf("expected %s but got %s", expectedCode, result.Code))
}
