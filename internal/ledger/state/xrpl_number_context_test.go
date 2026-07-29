package state

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNumberContextCreatesValuesInSelectedScale(t *testing.T) {
	t.Parallel()

	small := NewNumberContext(MantissaScaleSmall, true)
	large := NewNumberContext(MantissaScaleLarge, true)

	require.Equal(t, MantissaScaleSmall, small.Scale())
	require.Equal(t, MantissaScaleLarge, large.Scale())
	require.Equal(t, MantissaScaleSmall, small.FromInt(1, RoundToNearest).MantissaScale())
	require.Equal(t, MantissaScaleLarge, large.New(1, 0, RoundToNearest).MantissaScale())
}

func TestNumberContextAmountConversions(t *testing.T) {
	t.Parallel()

	ctx := NewNumberContext(MantissaScaleLarge, true)
	issuer := "rIssuer"

	xrp := NewXRPAmountFromInt(5_000_000)
	require.Equal(t, int64(5_000_000), ctx.FromAmount(xrp, RoundToNearest).ToInt64WithMode(RoundToNearest))

	iou := NewIssuedAmountFromValue(1_234_567_890_123_456, -15, "USD", issuer)
	iouNumber := ctx.FromAmount(iou, RoundToNearest)
	iouResult := ctx.ToAmount(iouNumber, iou, RoundToNearest)
	require.False(t, iouResult.IsNative())
	require.Equal(t, iou.Currency, iouResult.Currency)
	require.Equal(t, iou.Issuer, iouResult.Issuer)
	require.Equal(t, iou.Value(), iouResult.Value())

	mpt := NewMPTAmountWithIssuanceID(42, issuer, "0123456789abcdef0123456789abcdef0123456789abcdef")
	mptResult := ctx.ToAmount(ctx.FromAmount(mpt, RoundToNearest), mpt, RoundToNearest)
	require.True(t, mptResult.IsMPT())
	require.Equal(t, mpt.MPTIssuanceID(), mptResult.MPTIssuanceID())
	require.Equal(t, issuer, mptResult.Issuer)
	require.Equal(t, int64(42), contextMPTRaw(t, mptResult))
}

func TestNumberContextToIntegralAmountUsesExplicitRounding(t *testing.T) {
	t.Parallel()

	ctx := NewNumberContext(MantissaScaleLarge, true)
	prototype := NewXRPAmountFromInt(0)
	positive := ctx.New(15, -1, RoundToNearest)
	negative := ctx.New(-15, -1, RoundToNearest)

	require.Equal(t, int64(2), ctx.ToAmount(positive, prototype, RoundToNearest).Drops())
	require.Equal(t, int64(1), ctx.ToAmount(positive, prototype, RoundTowardsZero).Drops())
	require.Equal(t, int64(-2), ctx.ToAmount(negative, prototype, RoundDownward).Drops())
	require.Equal(t, int64(-1), ctx.ToAmount(negative, prototype, RoundUpward).Drops())
}

func TestNumberContextToIOUAmountNormalizesBeforeExponentBounds(t *testing.T) {
	t.Parallel()

	ctx := NewNumberContext(MantissaScaleLarge, true)
	prototype := NewIssuedAmountFromValue(0, 0, "USD", "rIssuer")
	number := ctx.Number(1_000_000_000_000_000_000, -99, RoundToNearest)

	result := ctx.ToAmount(number, prototype, RoundToNearest)
	require.Equal(t, MinMantissa, result.Mantissa())
	require.Equal(t, MinExponent, result.Exponent())
}

func TestNumberContextToIOUAmountUsesExplicitRounding(t *testing.T) {
	t.Parallel()

	ctx := NewNumberContext(MantissaScaleLarge, true)
	prototype := NewIssuedAmountFromValue(0, 0, "USD", "rIssuer")
	number := ctx.Number(1_234_567_890_123_456_500, 0, RoundToNearest)

	nearest := ctx.ToAmount(number, prototype, RoundToNearest)
	upward := ctx.ToAmount(number, prototype, RoundUpward)
	require.Equal(t, int64(1_234_567_890_123_456), nearest.Mantissa())
	require.Equal(t, int64(1_234_567_890_123_457), upward.Mantissa())
}

func TestNumberContextExplicitAssetRoundingOnlyAppliesToXRP(t *testing.T) {
	t.Parallel()

	ctx := NewNumberContext(MantissaScaleLarge, true)
	fractional := ctx.Number(11, -1, RoundToNearest)

	xrp := ctx.ToAmountWithNativeRounding(
		fractional,
		NewXRPAmountFromInt(0),
		RoundUpward,
		RoundToNearest,
	)
	require.Equal(t, int64(2), xrp.Drops())

	mptPrototype := NewMPTAmountWithIssuanceID(0, "rIssuer", "0123456789abcdef0123456789abcdef0123456789abcdef")
	mpt := ctx.ToAmountWithNativeRounding(fractional, mptPrototype, RoundUpward, RoundToNearest)
	require.Equal(t, int64(1), contextMPTRaw(t, mpt))

	iouPrototype := NewIssuedAmountFromValue(0, 0, "USD", "rIssuer")
	iouNumber := ctx.Number(1_234_567_890_123_456_500, 0, RoundToNearest)
	iou := ctx.ToAmountWithNativeRounding(iouNumber, iouPrototype, RoundUpward, RoundToNearest)
	require.Equal(t, int64(1_234_567_890_123_456), iou.Mantissa())
}

func TestNumberContextsAreIndependentUnderConcurrency(t *testing.T) {
	t.Parallel()

	contexts := []NumberContext{
		NewNumberContext(MantissaScaleSmall, true),
		NewNumberContext(MantissaScaleLargeLegacy, true),
		NewNumberContext(MantissaScaleLarge, true),
	}

	var wg sync.WaitGroup
	mismatches := make(chan MantissaScale, len(contexts))
	for _, ctx := range contexts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1_000 {
				n := ctx.New(9_223_372_036_854_775_807, 0, RoundToNearest)
				if n.MantissaScale() != ctx.Scale() || n.Root2().MantissaScale() != ctx.Scale() {
					mismatches <- ctx.Scale()
					return
				}
			}
		}()
	}
	wg.Wait()
	close(mismatches)
	for scale := range mismatches {
		t.Errorf("Number operations did not retain mantissa scale %d", scale)
	}
}

func contextMPTRaw(t *testing.T, amount Amount) int64 {
	t.Helper()
	value, ok := amount.MPTRaw()
	require.True(t, ok)
	return value
}
