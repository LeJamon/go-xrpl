package pathfinder

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
)

func TestFlowCalculationSettingsUseUniversalDefaultNumberContext(t *testing.T) {
	ledger := &configuredPathfinderLedger{
		mockLedgerView: newMockLedger(),
		rules:          amendment.NewRules(nil),
	}
	settings := newFlowCalculationSettings(ledger, 0)
	want := tx.NumberContextForRules(nil)
	if settings.numberContext != want {
		t.Fatalf(
			"number context = (scale %d, universal %t), want (scale %d, universal %t)",
			settings.numberContext.Scale(),
			settings.numberContext.UniversalNumberEnabled(),
			want.Scale(),
			want.UniversalNumberEnabled(),
		)
	}
}

func TestComputeQualityUsesLedgerNumberContext(t *testing.T) {
	out := payment.ToEitherAmount(state.NewXRPAmountFromInt(3))
	in := payment.ToEitherAmount(state.NewXRPAmountFromInt(1))
	legacy := state.NewNumberContext(state.MantissaScaleSmall, false)
	universal := state.NewNumberContext(state.MantissaScaleSmall, true)

	gotLegacy := computeQuality(out, in, legacy)
	gotUniversal := computeQuality(out, in, universal)
	wantLegacy := state.GetRateWithNumberContext(
		state.NewXRPAmountFromInt(3),
		state.NewXRPAmountFromInt(1),
		legacy,
	)
	wantUniversal := state.GetRateWithNumberContext(
		state.NewXRPAmountFromInt(3),
		state.NewXRPAmountFromInt(1),
		universal,
	)
	if gotLegacy != wantLegacy {
		t.Fatalf("legacy quality = %#x, want %#x", gotLegacy, wantLegacy)
	}
	if gotUniversal != wantUniversal {
		t.Fatalf("universal quality = %#x, want %#x", gotUniversal, wantUniversal)
	}
	if gotLegacy == gotUniversal {
		t.Fatalf("quality ignored number context: both regimes returned %#x", gotLegacy)
	}
}

func TestLargestAmountUsesExactMaximumIOU(t *testing.T) {
	got := largestAmount(state.NewIssuedAmountFromValue(
		state.MinMantissa,
		-15,
		"USD",
		"rIssuer",
	))
	if got.Mantissa() != state.MaxMantissa || got.Exponent() != state.MaxExponent {
		t.Fatalf(
			"largest IOU = (%d, %d), want (%d, %d)",
			got.Mantissa(),
			got.Exponent(),
			state.MaxMantissa,
			state.MaxExponent,
		)
	}
}
