package entry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAMM_Type verifies AMM returns correct type
func TestAMM_Type(t *testing.T) {
	amm := &AMM{}
	assert.Equal(t, TypeAMM, amm.Type())
	assert.Equal(t, "AMM", amm.Type().String())
}

// TestAMM_Validate tests AMM validation logic
// Reference: rippled/src/test/app/AMM_test.cpp
func TestAMM_Validate(t *testing.T) {
	validAccount := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}

	t.Run("Valid AMM with minimum fields", func(t *testing.T) {
		amm := &AMM{
			Account:    validAccount,
			TradingFee: 100, // 0.1%
			Asset:      Issue{Currency: [20]byte{}},
			Asset2:     Issue{Currency: [20]byte{1}},
		}
		err := amm.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid AMM with zero trading fee", func(t *testing.T) {
		amm := &AMM{
			Account:    validAccount,
			TradingFee: 0,
		}
		err := amm.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid AMM with max trading fee (1%)", func(t *testing.T) {
		// Max trading fee is 1000 basis points = 1%
		amm := &AMM{
			Account:    validAccount,
			TradingFee: 1000,
		}
		err := amm.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid AMM with vote slots", func(t *testing.T) {
		amm := &AMM{
			Account:    validAccount,
			TradingFee: 100,
			VoteSlots: []VoteSlot{
				{
					Account:    validAccount,
					TradingFee: 50,
					VoteWeight: 100,
				},
			},
		}
		err := amm.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid AMM with auction slot", func(t *testing.T) {
		amm := &AMM{
			Account:    validAccount,
			TradingFee: 100,
			AuctionSlot: &AuctionSlot{
				Account:       validAccount,
				DiscountedFee: 10,
				Expiration:    1000000,
			},
		}
		err := amm.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid AMM with empty account", func(t *testing.T) {
		amm := &AMM{
			Account: [20]byte{},
		}
		err := amm.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "account")
	})

	t.Run("Invalid AMM with trading fee exceeding 1%", func(t *testing.T) {
		// Reference: AMM cannot have trading fee > 1%
		amm := &AMM{
			Account:    validAccount,
			TradingFee: 1001, // > 1%
		}
		err := amm.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "trading fee")
	})
}

// TestAMM_Hash tests AMM hash computation
func TestAMM_Hash(t *testing.T) {
	amm := &AMM{
		Account:    [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		TradingFee: 100,
	}

	hash1, err := amm.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)

	// Different account should produce different hash
	amm2 := &AMM{
		Account:    [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		TradingFee: 100,
	}
	hash2, err := amm2.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, hash1, hash2)
}

// TestVoteSlot tests the VoteSlot struct
func TestVoteSlot(t *testing.T) {
	vs := VoteSlot{
		Account:    [20]byte{1, 2, 3},
		TradingFee: 50,
		VoteWeight: 1000,
	}

	assert.Equal(t, uint16(50), vs.TradingFee)
	assert.Equal(t, uint32(1000), vs.VoteWeight)
}

// TestAuctionSlot tests the AuctionSlot struct
func TestAuctionSlot(t *testing.T) {
	as := AuctionSlot{
		Account:       [20]byte{1, 2, 3},
		AuthAccounts:  [][20]byte{{4, 5, 6}, {7, 8, 9}},
		DiscountedFee: 10,
		Expiration:    1000000,
		Price: Amount{
			Drops:    1000000,
			IsNative: true,
		},
	}

	assert.Equal(t, uint32(10), as.DiscountedFee)
	assert.Equal(t, uint32(1000000), as.Expiration)
	assert.Len(t, as.AuthAccounts, 2)
}

// TestAmount tests the Amount struct
func TestAmount(t *testing.T) {
	t.Run("Native XRP amount", func(t *testing.T) {
		amt := Amount{
			Drops:    1000000,
			IsNative: true,
		}
		assert.True(t, amt.IsNative)
		assert.Equal(t, uint64(1000000), amt.Drops)
	})

	t.Run("IOU amount", func(t *testing.T) {
		amt := Amount{
			Value:    "100.5",
			Currency: [20]byte{1, 2, 3},
			Issuer:   [20]byte{4, 5, 6},
			IsNative: false,
		}
		assert.False(t, amt.IsNative)
		assert.Equal(t, "100.5", amt.Value)
	})
}
