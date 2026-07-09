package amm

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// TestInitializeFeeAuctionVote_StaleAuthAccounts verifies the fixCleanup3_2_0
// behaviour: when an emptied AMM is re-initialized (AMMDeposit tfTwoAssetIfEmpty),
// stale AuthAccounts from the previous auction slot are cleared with the
// amendment enabled and preserved without it.
// Reference: rippled AMMHelpers.cpp initializeFeeAuctionVote (PR 6996).
func TestInitializeFeeAuctionVote_StaleAuthAccounts(t *testing.T) {
	stale := [][20]byte{{0x01}, {0x02}}
	var creator [20]byte
	creator[0] = 0xBB

	newAMM := func() *AMMData {
		return &AMMData{
			Asset:  tx.Asset{Currency: "USD", Issuer: "rIssuerPlaceholderrrrrrrrrrrrr"},
			Asset2: tx.Asset{Currency: "XRP"},
			AuctionSlot: &AuctionSlotData{
				Account:             [20]byte{0xAA},
				AuthAccounts:        stale,
				AuthAccountsPresent: true,
			},
		}
	}

	t.Run("amendment_on_clears", func(t *testing.T) {
		amm := newAMM()
		initializeFeeAuctionVote(amm, creator, "USD", "rAMMaddr", 500, 1000, true)
		require.False(t, amm.AuctionSlot.AuthAccountsPresent)
		require.Empty(t, amm.AuctionSlot.AuthAccounts)
		require.Equal(t, creator, amm.AuctionSlot.Account)
		require.Equal(t, uint16(50), amm.AuctionSlot.DiscountedFee)
	})

	t.Run("amendment_off_preserves", func(t *testing.T) {
		amm := newAMM()
		initializeFeeAuctionVote(amm, creator, "USD", "rAMMaddr", 500, 1000, false)
		require.True(t, amm.AuctionSlot.AuthAccountsPresent)
		require.Equal(t, stale, amm.AuctionSlot.AuthAccounts)
		require.Equal(t, creator, amm.AuctionSlot.Account)
	})
}
