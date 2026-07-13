package state

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckDataMPTokenSendMaxRoundTrip(t *testing.T) {
	const issuanceID = "00000001ABCDEF0123456789ABCDEF0123456789ABCDEF12"
	check := &CheckData{
		Account:         [20]byte{1},
		DestinationID:   [20]byte{2},
		Sequence:        7,
		OwnerNode:       3,
		DestinationNode: 4,
		HasDestNode:     true,
		SendMaxAmount:   NewMPTAmountWithIssuanceID(9_223_372_036_854_775_000, "", issuanceID),
		IsNativeSendMax: false,
	}

	encoded, err := SerializeCheckFromData(check)
	require.NoError(t, err)

	parsed, err := ParseCheck(encoded)
	require.NoError(t, err)
	require.False(t, parsed.IsNativeSendMax)
	require.True(t, parsed.SendMaxAmount.IsMPT())
	require.Equal(t, issuanceID, parsed.SendMaxAmount.MPTIssuanceID())
	raw, ok := parsed.SendMaxAmount.MPTRaw()
	require.True(t, ok)
	require.Equal(t, int64(9_223_372_036_854_775_000), raw)

	reencoded, err := SerializeCheckFromData(parsed)
	require.NoError(t, err)
	require.Equal(t, encoded, reencoded)
}
