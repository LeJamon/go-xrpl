package paychan

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
)

// TestPaymentChannelCreateFundingBoundary pins PayChanCreate's reserve and
// funding checks to the pre-fee balance. rippled runs them in preclaim against
// the pre-fee ReadView (PayChan.cpp:209-213), so a base fee that straddles
// reserve(OwnerCount+1) or reserve(OwnerCount+1)+amount must not flip the TER.
func TestPaymentChannelCreateFundingBoundary(t *testing.T) {
	const settleDelay = uint32(3600)

	// create funds a normally-provisioned destination and a source funded to
	// balanceFor(env), then has the source open a channel for amount drops.
	create := func(t *testing.T, amount int64, balanceFor func(env *jtx.TestEnv) uint64) jtx.TxResult {
		t.Helper()
		env := jtx.NewTestEnv(t)
		alice := jtx.NewAccount("alice")
		bob := jtx.NewAccount("bob")
		env.FundAmount(bob, uint64(xrp(10000)))
		env.Close()
		env.FundAmount(alice, balanceFor(env))
		env.Close()
		return env.Submit(ChannelCreate(alice, bob, amount, settleDelay, alice.PublicKeyHex()).Build())
	}

	t.Run("FeeStraddlesFundingBoundary", func(t *testing.T) {
		// Pre-fee balance clears reserve(1)+amount by 8 drops; the base fee would
		// push the post-fee balance just under it. Reading the post-fee balance
		// would return a spurious tecUNFUNDED — the bug.
		amount := xrp(10)
		result := create(t, amount, func(env *jtx.TestEnv) uint64 {
			return env.ReserveBase() + env.ReserveIncrement() + uint64(amount) + 8
		})
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("FeeStraddlesReserveBoundary", func(t *testing.T) {
		// Pre-fee balance clears reserve(1) by 8 drops for a 1-drop channel; the
		// base fee would push the post-fee balance under reserve(1), returning a
		// spurious tecINSUFFICIENT_RESERVE.
		result := create(t, drops(1), func(env *jtx.TestEnv) uint64 {
			return env.ReserveBase() + env.ReserveIncrement() + 8
		})
		jtx.RequireTxSuccess(t, result)
	})

	t.Run("Unfunded", func(t *testing.T) {
		// Pre-fee balance is genuinely below reserve(1)+amount.
		amount := xrp(10)
		result := create(t, amount, func(env *jtx.TestEnv) uint64 {
			return env.ReserveBase() + env.ReserveIncrement() + uint64(amount) - uint64(xrp(1))
		})
		jtx.RequireTxClaimed(t, result, "tecUNFUNDED")
	})
}
