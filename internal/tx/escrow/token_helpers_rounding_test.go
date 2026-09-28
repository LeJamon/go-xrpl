package escrow

import (
	"encoding/hex"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestDivideAmountByRateUsesCanonicalIOURounding(t *testing.T) {
	const issuer = "rDC7wGzpzUjS2qTASSzGWkUytS7FD9xyVK"
	amount := state.NewIssuedAmountFromValue(1_000_000_000_000_000, -13, "USD", issuer)
	numberContext := state.NewNumberContext(state.MantissaScaleLarge, true)

	want := state.NewIssuedAmountFromValue(9_900_990_099_009_901, -14, "USD", issuer)
	require.Equal(t, want, divideAmountByRate(amount, 1_010_000_000, numberContext))
	require.Equal(t, amount, divideAmountByRate(amount, parityRate, numberContext))
}

func TestComputeMPTTransferFeeUsesCanonicalRounding(t *testing.T) {
	const originalAmount = uint64(10_000)
	numberContext := state.NewNumberContext(state.MantissaScaleLarge, true)

	var issuerID, senderID, receiverID [20]byte
	issuerID[19] = 0xab
	senderID[19] = 0xcd
	receiverID[19] = 0xef

	mptID := keylet.MakeMPTID(1, issuerID)
	mptHexID := hex.EncodeToString(mptID[:])
	issuer := state.EncodeAccountIDSafe(issuerID)
	rate := state.NewIssuedAmountFromValue(1_001_000_000, -9, "", "")
	legacyContext := state.NewNumberContext(state.MantissaScaleSmall, false)

	withoutIssuanceID := state.NewMPTAmountDirect(int64(originalAmount), "", issuer)
	require.False(t, withoutIssuanceID.IsMPT())
	require.Equal(t, int64(9_991), state.DivRoundMPTWithNumberContext(withoutIssuanceID, rate, legacyContext, true))

	withIssuanceID := state.NewMPTAmountWithIssuanceID(
		int64(originalAmount),
		issuer,
		mptHexID,
	)
	require.True(t, withIssuanceID.IsMPT())
	require.Equal(t, int64(9_990), state.DivRoundMPTWithNumberContext(withIssuanceID, rate, legacyContext, true))

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

			original, final, result := computeMPTTransferFee(
				view,
				tt.lockedRate,
				mptHexID,
				senderID,
				destinationID,
				originalAmount,
				numberContext,
			)
			require.Equal(t, ter.TesSUCCESS, result)
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
				numberContext,
			)
		})
	})
}

type cleanupEscrowView struct {
	*mapView
	rules *amendment.Rules
}

func (v *cleanupEscrowView) Rules() *amendment.Rules { return v.rules }

func TestComputeMPTTransferFeeCleanup(t *testing.T) {
	var issuer, sender, receiver [20]byte
	issuer[19], sender[19], receiver[19] = 1, 2, 3
	id := keylet.MakeMPTID(1, issuer)
	hexID := hex.EncodeToString(id[:])
	rules := amendment.NewRulesBuilder().Enable(amendment.FeatureFixCleanup3_4_0).Build()
	for _, tc := range []struct {
		name                         string
		amount                       uint64
		locked                       uint32
		fee                          uint16
		senderIssuer, receiverIssuer bool
		want                         uint64
	}{
		{name: "maximum supply", amount: math.MaxInt64, locked: 1_001_000_000, fee: 100, want: 9_214_157_878_975_800_006},
		{name: "fractional fee", amount: 4, locked: 1_500_000_000, fee: 50_000, want: 2},
		{name: "dust", amount: 1, locked: 1_500_000_000, fee: 50_000, want: 0},
		{name: "locked rate lower", amount: 7, locked: 1_250_000_000, fee: 50_000, want: 5},
		{name: "current rate lower", amount: 7, locked: 1_500_000_000, fee: 25_000, want: 5},
		{name: "unlocked rate", amount: 7, fee: 25_000, want: 5},
		{name: "sender issuer", amount: 7, locked: 1_500_000_000, fee: 50_000, senderIssuer: true, want: 7},
		{name: "receiver issuer", amount: 7, locked: 1_500_000_000, fee: 50_000, receiverIssuer: true, want: 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := &cleanupEscrowView{mapView: newMapView(), rules: rules}
			raw, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{Issuer: issuer, Sequence: 1, TransferFee: tc.fee})
			require.NoError(t, err)
			require.NoError(t, view.Insert(keylet.MPTIssuance(id), raw))
			src, dst := sender, receiver
			if tc.senderIssuer {
				src = issuer
			}
			if tc.receiverIssuer {
				dst = issuer
			}
			original, final, result := computeMPTTransferFee(view, tc.locked, hexID, src, dst, tc.amount, state.NewNumberContext(state.MantissaScaleLarge, true))
			require.Equal(t, ter.TesSUCCESS, result)
			require.Equal(t, tc.amount, original)
			require.Equal(t, tc.want, final)
		})
	}
}
