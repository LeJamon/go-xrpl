package payment

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/stretchr/testify/require"
)

const ammMPTID = "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"

func TestAMMMPTNumberConversionsPreserveIntegralIssue(t *testing.T) {
	original := state.NewMPTAmountWithIssuanceID(7, "rIssuer", ammMPTID)
	half := state.NewIssuedAmountFromValue(25, -1, "", "")

	nearest := fromNumber(half, original)
	value, ok := nearest.MPTRaw()
	require.True(t, ok)
	require.Equal(t, int64(2), value)
	require.Equal(t, ammMPTID, nearest.MPTIssuanceID())

	upward := fromNumberWithGuard(half, original, state.RoundUpward)
	value, ok = upward.MPTRaw()
	require.True(t, ok)
	require.Equal(t, int64(3), value)
	require.Equal(t, ammMPTID, upward.MPTIssuanceID())
}

func TestAMMMPTAmountFactoriesPreserveIssue(t *testing.T) {
	original := state.NewMPTAmountWithIssuanceID(7, "rIssuer", ammMPTID)

	zero := zeroLikeAmount(original)
	value, ok := zero.MPTRaw()
	require.True(t, ok)
	require.Zero(t, value)
	require.Equal(t, ammMPTID, zero.MPTIssuanceID())

	maximum := maxAmountLike(original)
	value, ok = maximum.MPTRaw()
	require.True(t, ok)
	require.Equal(t, int64(math.MaxInt64), value)
	require.Equal(t, ammMPTID, maximum.MPTIssuanceID())

	either := toEitherAmt(original)
	require.True(t, either.IsMPT)
	require.Equal(t, int64(7), either.MPT)
}
