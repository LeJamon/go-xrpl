package amm_test

import (
	"fmt"
	"testing"
	"time"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/stretchr/testify/require"
)

func TestAMMBidCleanup340Floor(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, expired := range []bool{false, true} {
			for _, fee := range []uint16{0, 1, 1000} {
				t.Run(fmt.Sprintf("cleanup=%t/expired=%t/fee=%d", enabled, expired, fee), func(t *testing.T) {
					amm.TestAMM(t, nil, fee, func(env *amm.AMMTestEnv, pool *jtx.Account) {
						if enabled {
							env.EnableFeature("fixCleanup3_4_0")
							env.Close()
						}
						if expired {
							env.AdvanceTime(86401 * time.Second)
							env.Close()
						}
						before := env.ReadAMMData(amm.XRP(), env.USD)
						require.NotNil(t, before)
						require.Equal(t, "10000000", before.LPTokenBalance.Value())
						amount := float64(fee) * 4
						if enabled && fee == 0 {
							amount = 4
						}
						result := env.Submit(amm.AMMBid(env.Alice, amm.XRP(), env.USD).Build())
						jtx.RequireTxSuccess(t, result)
						after := env.ReadAMMData(amm.XRP(), env.USD)
						require.NotNil(t, after)
						require.NotNil(t, after.AuctionSlot)
						require.Equal(t, fmt.Sprintf("%g", amount), after.AuctionSlot.Price.Value())
						require.Equal(t, fmt.Sprintf("%.0f", 10_000_000-amount), after.LPTokenBalance.Value())
						require.Equal(t, env.Alice.ID, after.AuctionSlot.Account)
						require.Equal(t, fee/10, after.AuctionSlot.DiscountedFee)
						require.Equal(t, uint64(10_000_000_000), env.Balance(pool))
						jtx.RequireIOUBalance(t, env.TestEnv, pool, env.GW, "USD", 10000)
						if fee == 0 && enabled {
							result = env.Submit(amm.AMMBid(env.Alice, amm.XRP(), env.USD).Build())
							jtx.RequireTxSuccess(t, result)
							after = env.ReadAMMData(amm.XRP(), env.USD)
							require.Equal(t, "4.2", after.AuctionSlot.Price.Value())
							require.Equal(t, "9999995.6", after.LPTokenBalance.Value())
						}
					})
				})
			}
		}
	}
}

func TestAMMBidCleanup340Limits(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, limit := range []string{"tiny minimum", "low maximum", "exact maximum", "high minimum", "range"} {
			t.Run(fmt.Sprintf("cleanup=%t/%s", enabled, limit), func(t *testing.T) {
				amm.TestAMM(t, nil, 0, func(env *amm.AMMTestEnv, _ *jtx.Account) {
					if enabled {
						env.EnableFeature("fixCleanup3_4_0")
						env.Close()
					}
					bid := amm.AMMBid(env.Alice, amm.XRP(), env.USD)
					price := float64(0)
					if enabled {
						price = 4
					}
					switch limit {
					case "tiny minimum":
						bid.BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 1))
						if !enabled {
							price = 1
						}
					case "low maximum":
						bid.BidMax(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 3))
					case "exact maximum":
						bid.BidMax(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 4))
					case "high minimum":
						bid.BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 5))
						price = 5
					case "range":
						bid.BidMin(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 1))
						bid.BidMax(env.LPTokenAmountFromLedger(amm.XRP(), env.USD, 3))
						if !enabled {
							price = 1
						}
					}
					before := env.ReadAMMData(amm.XRP(), env.USD)
					result := env.Submit(bid.Build())
					after := env.ReadAMMData(amm.XRP(), env.USD)
					if enabled && (limit == "low maximum" || limit == "range") {
						jtx.RequireTxClaimed(t, result, "tecAMM_FAILED")
						require.Equal(t, before.LPTokenBalance, after.LPTokenBalance)
						require.Equal(t, before.AuctionSlot, after.AuctionSlot)
					} else {
						jtx.RequireTxSuccess(t, result)
						require.Equal(t, fmt.Sprintf("%g", price), after.AuctionSlot.Price.Value())
					}
				})
			})
		}
	}
}
