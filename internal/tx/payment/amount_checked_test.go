package payment

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestCheckedAmountAdditionOracle(t *testing.T) {
	raw, err := os.ReadFile("testdata/checked_add_oracle.json")
	require.NoError(t, err)
	var fixture struct {
		Commit string
		Cases  []struct {
			Name   string
			Values []string
			Sum    *string
		}
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.Equal(t, "d147fccf54a500fce586522f28d6044c37fd8d29", fixture.Commit)
	id := [24]byte{1}
	for _, asset := range []struct {
		name string
		make func(int64) EitherAmount
	}{
		{"XRP", NewXRPEitherAmount},
		{"MPT", func(v int64) EitherAmount { return NewMPTEitherAmount(v, id) }},
	} {
		for _, tc := range fixture.Cases {
			t.Run(asset.name+"/"+tc.Name, func(t *testing.T) {
				ctx := state.NewNumberContext(state.MantissaScaleSmall, false)
				amounts := make([]EitherAmount, 0, len(tc.Values))
				var book amountMultiset
				for _, value := range tc.Values {
					n, err := strconv.ParseInt(value, 10, 64)
					require.NoError(t, err)
					amount := asset.make(n)
					amounts = append(amounts, amount)
					book.insert(amount)
				}
				got, ok := sumAmountsWithNumberContext(amounts, ctx)
				require.Equal(t, tc.Sum != nil, ok)
				if tc.Sum == nil {
					require.True(t, got.IsZero(), "overflow must not expose a partial sum")
					failure := recoverFlowError(t, func() { book.sum(asset.make(0), ctx) })
					require.Equal(t, ter.TecPATH_DRY, failure.ter)
					require.False(t, failure.fatal)
				} else {
					n, err := strconv.ParseInt(*tc.Sum, 10, 64)
					require.NoError(t, err)
					want := asset.make(n)
					if len(amounts) == 0 {
						require.True(t, got.IsZero())
					} else {
						require.Equal(t, want, got)
					}
					require.Equal(t, want, book.sum(asset.make(0), ctx))
				}
				if len(amounts) == 2 {
					got, ok := amounts[0].checkedAddWithNumberContext(amounts[1], ctx)
					require.Equal(t, tc.Sum != nil, ok)
					if ok {
						require.Equal(t, got, amounts[0].AddWithNumberContext(amounts[1], ctx))
					} else {
						failure := recoverFlowError(t, func() { amounts[0].Add(amounts[1]) })
						require.Equal(t, ter.TecPATH_DRY, failure.ter)
					}
				}
			})
		}
	}
}

func TestCheckedAggregationPreservesIOUOrder(t *testing.T) {
	for _, ctx := range []state.NumberContext{
		state.NewNumberContext(state.MantissaScaleSmall, false),
		state.NewNumberContext(state.MantissaScaleLargeLegacy, false),
		state.NewNumberContext(state.MantissaScaleLarge320, true),
	} {
		large := NewIOUEitherAmount(tx.NewIssuedAmount(1_000_000_000_000_000, -15, "USD", "issuer"))
		small := NewIOUEitherAmount(tx.NewIssuedAmount(6_000_000_000_000_000, -31, "USD", "issuer"))
		wantSmall, err := small.IOU.AddWithNumberContext(small.IOU, ctx, state.RoundToNearest)
		require.NoError(t, err)
		want, err := wantSmall.AddWithNumberContext(large.IOU, ctx, state.RoundToNearest)
		require.NoError(t, err)
		amounts := []EitherAmount{large, small, small}
		got, ok := sumAmountsWithNumberContext(amounts, ctx)
		require.True(t, ok)
		require.Equal(t, NewIOUEitherAmount(want), got)
		require.Equal(t, large, amounts[0], "summation must not reorder the caller's amounts")
		var book amountMultiset
		for _, amount := range amounts {
			book.insert(amount)
		}
		require.Equal(t, got, book.sum(ZeroIOUEitherAmount("USD", "issuer"), ctx))
	}
}
