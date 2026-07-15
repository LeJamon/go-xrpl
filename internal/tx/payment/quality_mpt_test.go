package payment

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualityMPTCeilUsesIntegralRounding(t *testing.T) {
	var id [24]byte
	id[23] = 1

	in := NewMPTEitherAmount(5, id)
	out := NewMPTEitherAmount(10, id)
	q := QualityFromAmounts(in, out)
	roundedIn, limitedOut := q.CeilOutStrict(in, out, NewMPTEitherAmount(5, id), true)
	require.Equal(t, int64(3), roundedIn.MPT)
	require.Equal(t, int64(5), limitedOut.MPT)
	require.Equal(t, id, roundedIn.MPTID)
	roundedIn, _ = q.CeilOutStrict(in, out, NewMPTEitherAmount(5, id), false)
	require.Equal(t, int64(2), roundedIn.MPT)

	in = NewMPTEitherAmount(10, id)
	out = NewMPTEitherAmount(5, id)
	q = QualityFromAmounts(in, out)
	limitedIn, roundedOut := q.CeilIn(in, out, NewMPTEitherAmount(5, id))
	require.Equal(t, int64(5), limitedIn.MPT)
	require.Equal(t, int64(3), roundedOut.MPT)
	require.Equal(t, id, roundedOut.MPTID)
	_, roundedOut = q.CeilInStrict(in, out, NewMPTEitherAmount(5, id), false)
	require.Equal(t, int64(2), roundedOut.MPT)
}
