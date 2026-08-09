package service

import (
	"context"
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

func TestGetBookOffersMPTFunding(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, issuer := addressFromBytes(t, 0x61)
	ownerAddr, owner := addressFromBytes(t, 0x71)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000, 0)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000, 0)

	maximum := uint64(1_000)
	id := keylet.MakeMPTID(7, issuer)
	issuance := &state.MPTokenIssuanceData{
		Issuer:            issuer,
		Sequence:          7,
		OutstandingAmount: 80,
		TransferFee:       25_000,
		MaximumAmount:     &maximum,
	}
	issuanceData, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, svc.openLedger.Insert(keylet.MPTIssuance(id), issuanceData))

	holdingData, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           owner,
		MPTokenIssuanceID: id,
		MPTAmount:         80,
	})
	require.NoError(t, err)
	require.NoError(t, svc.openLedger.Insert(keylet.MPTokenByID(id, owner), holdingData))

	idString := mptutil.EncodeID(id)
	mptModel := state.NewMPTAmountWithIssuanceID(0, issuerAddr, idString)
	insertOffer(t, svc, ownerAddr, 1,
		tx.NewXRPAmount(10_000_000),
		state.NewMPTAmountWithIssuanceID(100, issuerAddr, idString),
	)

	result, err := svc.GetBookOffers(
		context.Background(), mptModel, tx.NewXRPAmount(0), "", "", "current", 10, "", false,
	)
	require.NoError(t, err)
	require.Len(t, result.Offers, 1)
	offer := result.Offers[0]
	require.Equal(t, "80", offer.OwnerFunds)
	require.Equal(t, map[string]string{
		"mpt_issuance_id": idString,
		"value":           "80",
	}, offer.TakerGetsFunded)
	require.Equal(t, "8000000", offer.TakerPaysFunded)
}

func TestGetBookOffersMPTFundedPays(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, issuer := addressFromBytes(t, 0x62)
	ownerAddr, _ := addressFromBytes(t, 0x72)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000, 0)
	insertAccountRoot(t, svc, ownerAddr, 10_000_080, 0)

	maximum := uint64(1_000)
	id := keylet.MakeMPTID(8, issuer)
	issuanceData, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:        issuer,
		Sequence:      8,
		MaximumAmount: &maximum,
		Flags:         entry.LsfMPTLocked,
	})
	require.NoError(t, err)
	require.NoError(t, svc.openLedger.Insert(keylet.MPTIssuance(id), issuanceData))

	idString := mptutil.EncodeID(id)
	mptModel := state.NewMPTAmountWithIssuanceID(0, issuerAddr, idString)
	insertOffer(t, svc, ownerAddr, 1,
		state.NewMPTAmountWithIssuanceID(1_000, issuerAddr, idString),
		tx.NewXRPAmount(100),
	)

	result, err := svc.GetBookOffers(
		context.Background(), tx.NewXRPAmount(0), mptModel, "", "", "current", 10, "", false,
	)
	require.NoError(t, err)
	require.Len(t, result.Offers, 1)
	offer := result.Offers[0]
	require.Equal(t, "80", offer.OwnerFunds)
	require.Equal(t, "80", offer.TakerGetsFunded)
	require.Equal(t, map[string]string{
		"mpt_issuance_id": idString,
		"value":           "800",
	}, offer.TakerPaysFunded)
}

func TestGetBookOffersIssuerOwnedMPTIsFullyFunded(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, issuer := addressFromBytes(t, 0x63)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000, 0)

	maximum := uint64(100)
	id := keylet.MakeMPTID(9, issuer)
	issuanceData, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:            issuer,
		Sequence:          9,
		OutstandingAmount: maximum,
		MaximumAmount:     &maximum,
	})
	require.NoError(t, err)
	require.NoError(t, svc.openLedger.Insert(keylet.MPTIssuance(id), issuanceData))

	idString := mptutil.EncodeID(id)
	mptModel := state.NewMPTAmountWithIssuanceID(0, issuerAddr, idString)
	insertOffer(t, svc, issuerAddr, 1,
		tx.NewXRPAmount(10_000_000),
		state.NewMPTAmountWithIssuanceID(100, issuerAddr, idString),
	)

	result, err := svc.GetBookOffers(
		context.Background(), mptModel, tx.NewXRPAmount(0), "", "", "current", 10, "", false,
	)
	require.NoError(t, err)
	require.Len(t, result.Offers, 1)
	require.Equal(t, "100", result.Offers[0].OwnerFunds)
	require.Nil(t, result.Offers[0].TakerGetsFunded)
	require.Nil(t, result.Offers[0].TakerPaysFunded)
}

func TestMultiplyByDirRateUsesRPCNumberSemantics(t *testing.T) {
	prototype := state.NewMPTAmountWithIssuanceID(
		100,
		"",
		"00000004AE123A8556F3CF91154711376AFB0F894F832B3D",
	)
	tests := []struct {
		name       string
		getsFunded int64
		rate       int64
		want       int64
	}{
		{name: "below half", getsFunded: 3, rate: 4, want: 1},
		{name: "tie to even", getsFunded: 5, rate: 5, want: 2},
		{name: "tie to odd", getsFunded: 3, rate: 5, want: 2},
		{name: "above half", getsFunded: 3, rate: 6, want: 2},
	}

	for _, test := range tests {
		dirRate := tx.NewIssuedAmount(test.rate, -1, "", "")
		got, ok := multiplyByDirRate(tx.NewXRPAmount(test.getsFunded), dirRate, prototype).MPTRaw()
		require.True(t, ok)
		require.Equalf(t, test.want, got, "%s", test.name)
	}
}

func TestMultiplyByDirRatePreservesLargeMPTPrecision(t *testing.T) {
	issuanceID := "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"
	amount := state.NewMPTAmountWithIssuanceID(math.MaxInt64, "", issuanceID)
	prototype := state.NewMPTAmountWithIssuanceID(0, "", issuanceID)
	dirRate := tx.NewIssuedAmount(1, 0, "", "")

	got, ok := multiplyByDirRate(amount, dirRate, prototype).MPTRaw()
	require.True(t, ok)
	require.Equal(t, int64(math.MaxInt64), got)
}

func TestNewDirRateUsesRPCNumberSemantics(t *testing.T) {
	const mantissa = uint64(10_000_000_000_000_015)
	quality := uint64(84)<<56 | mantissa // exponent -16
	got := newDirRate(quality, tx.NewXRPAmount(0))

	require.Equal(t, int64(1_000_000_000_000_002), got.Mantissa())
	require.Equal(t, -15, got.Exponent())
}

func TestGetBookOffersMPTFundedPaysNearestEven(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, issuer := addressFromBytes(t, 0x64)
	ownerAddr, _ := addressFromBytes(t, 0x74)
	insertAccountRoot(t, svc, ownerAddr, 10_000_003, 0)

	id := keylet.MakeMPTID(10, issuer)
	idString := mptutil.EncodeID(id)
	mptModel := state.NewMPTAmountWithIssuanceID(0, issuerAddr, idString)
	insertOffer(t, svc, ownerAddr, 1,
		state.NewMPTAmountWithIssuanceID(3, issuerAddr, idString),
		tx.NewXRPAmount(6),
	)

	result, err := svc.GetBookOffers(
		context.Background(), tx.NewXRPAmount(0), mptModel, "", "", "current", 10, "", false,
	)
	require.NoError(t, err)
	require.Len(t, result.Offers, 1)
	require.Equal(t, "3", result.Offers[0].TakerGetsFunded)
	require.Equal(t, map[string]string{
		"mpt_issuance_id": idString,
		"value":           "2",
	}, result.Offers[0].TakerPaysFunded)
}
