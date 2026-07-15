package state

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestLedgerOfferMPTAmountRoundTrip(t *testing.T) {
	var issuer, owner [20]byte
	copy(issuer[:], []byte("issuer12345678901234"))
	copy(owner[:], []byte("owner123456789012345"))
	id := keylet.MakeMPTID(7, issuer)
	mptID := strings.ToUpper(hex.EncodeToString(id[:]))

	offer := &LedgerOffer{
		Account:       EncodeAccountIDSafe(owner),
		Sequence:      9,
		TakerPays:     NewMPTAmountWithIssuanceID(123, EncodeAccountIDSafe(issuer), mptID),
		TakerGets:     NewXRPAmountFromInt(456),
		BookDirectory: [32]byte{1, 2, 3},
	}

	raw, err := SerializeLedgerOffer(offer)
	require.NoError(t, err)
	parsed, err := ParseLedgerOffer(raw)
	require.NoError(t, err)

	value, ok := parsed.TakerPays.MPTRaw()
	require.True(t, ok)
	require.Equal(t, int64(123), value)
	require.Equal(t, mptID, parsed.TakerPays.MPTIssuanceID())
	require.Equal(t, int64(456), parsed.TakerGets.Drops())
}
