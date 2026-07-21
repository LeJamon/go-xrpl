package escrow

import (
	"encoding/hex"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestDivideAmountByRateUsesCanonicalIOURounding(t *testing.T) {
	const issuer = "rDC7wGzpzUjS2qTASSzGWkUytS7FD9xyVK"
	amount := state.NewIssuedAmountFromValue(1_000_000_000_000_000, -13, "USD", issuer)

	want := state.NewIssuedAmountFromValue(9_900_990_099_009_901, -14, "USD", issuer)
	require.Equal(t, want, divideAmountByRate(amount, 1_010_000_000))
	require.Equal(t, amount, divideAmountByRate(amount, parityRate))
}

func TestComputeMPTTransferFeeUsesCanonicalRounding(t *testing.T) {
	const originalAmount = uint64(10_000)

	var issuerID, senderID, receiverID [20]byte
	issuerID[19] = 0xab
	senderID[19] = 0xcd
	receiverID[19] = 0xef

	mptID := keylet.MakeMPTID(1, issuerID)
	mptHexID := hex.EncodeToString(mptID[:])
	issuer := state.EncodeAccountIDSafe(issuerID)
	rate := state.NewIssuedAmountFromValue(1_001_000_000, -9, "", "")

	withoutIssuanceID := state.NewMPTAmountDirect(int64(originalAmount), "", issuer)
	require.False(t, withoutIssuanceID.IsMPT())
	require.Equal(t, int64(9_991), state.DivRoundMPT(withoutIssuanceID, rate, true))

	withIssuanceID := state.NewMPTAmountWithIssuanceID(
		int64(originalAmount),
		issuer,
		mptHexID,
	)
	require.True(t, withIssuanceID.IsMPT())
	require.Equal(t, int64(9_990), state.DivRoundMPT(withIssuanceID, rate, true))

	tests := []struct {
		name         string
		lockedRate   uint32
		currentFee   uint16
		issuerIsDest bool
		finalAmount  uint64
	}{
		{
			name:        "issue 1402 rounding",
			lockedRate:  getMPTTransferRate(100),
			currentFee:  100,
			finalAmount: 9_990,
		},
		{
			name:        "parity rate",
			lockedRate:  parityRate,
			currentFee:  100,
			finalAmount: originalAmount,
		},
		{
			name:         "issuer is destination",
			lockedRate:   getMPTTransferRate(100),
			currentFee:   100,
			issuerIsDest: true,
			finalAmount:  originalAmount,
		},
		{
			name:        "locked rate is lower",
			lockedRate:  getMPTTransferRate(100),
			currentFee:  200,
			finalAmount: 9_990,
		},
		{
			name:        "current rate is lower",
			lockedRate:  getMPTTransferRate(200),
			currentFee:  100,
			finalAmount: 9_990,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := newMapView()
			issuance, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
				Issuer:      issuerID,
				Sequence:    1,
				TransferFee: tt.currentFee,
			})
			require.NoError(t, err)
			require.NoError(t, view.Insert(keylet.MPTIssuance(mptID), issuance))

			destinationID := receiverID
			if tt.issuerIsDest {
				destinationID = issuerID
			}

			original, final := computeMPTTransferFee(
				view,
				tt.lockedRate,
				mptHexID,
				senderID,
				destinationID,
				originalAmount,
			)
			require.Equal(t, originalAmount, original)
			require.Equal(t, tt.finalAmount, final)
		})
	}

	t.Run("canonical intermediate overflow", func(t *testing.T) {
		view := newMapView()
		issuance, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
			Issuer:      issuerID,
			Sequence:    1,
			TransferFee: 100,
		})
		require.NoError(t, err)
		require.NoError(t, view.Insert(keylet.MPTIssuance(mptID), issuance))

		require.Panics(t, func() {
			computeMPTTransferFee(
				view,
				getMPTTransferRate(100),
				mptHexID,
				senderID,
				receiverID,
				math.MaxInt64,
			)
		})
	})
}
