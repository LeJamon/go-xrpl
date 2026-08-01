package feevote

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSignedAndExplicitZeroVotesAreNotNoVote(t *testing.T) {
	current := Stance{BaseFee: 10, ReserveBase: 10, ReserveIncrement: 10}
	target := Stance{BaseFee: 20, ReserveBase: 10, ReserveIncrement: 10}

	for _, test := range []struct {
		name     string
		value    uint64
		negative bool
	}{
		{name: "explicit zero"},
		{name: "legal negative", value: 1, negative: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := test.value
			blob, err := DoVoting(256, current, target, []Vote{{
				BaseFee:         &value,
				BaseFeeNegative: test.negative,
			}}, true)
			require.NoError(t, err)
			require.NotEmpty(t, blob)
		})
	}

	blob, err := DoVoting(256, current, target, []Vote{{}}, true)
	require.NoError(t, err)
	require.Empty(t, blob, "missing vote counts for current and ties the local target")
}
