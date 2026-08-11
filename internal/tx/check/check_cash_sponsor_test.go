package check

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func TestSponsoredCheckDoesNotReleaseWriterReserve(t *testing.T) {
	creator := &state.AccountRoot{
		Balance:             14,
		OwnerCount:          5,
		SponsoredOwnerCount: 3,
	}
	ctx := &tx.ApplyContext{Config: tx.EngineConfig{
		ReserveBase: 10, ReserveIncrement: 2,
		Rules: amendment.NewRules([][32]byte{amendment.FeatureSponsor}),
	}}

	if got := xrpAvailableFunds(creator, ctx, true); got != 0 {
		t.Fatalf("sponsored available funds = %d, want 0", got)
	}
	if got := xrpLiquidAfterCheck(creator, ctx, true); got != 0 {
		t.Fatalf("sponsored post-check liquid = %d, want 0", got)
	}
	if got := xrpAvailableFunds(creator, ctx, false); got != 2 {
		t.Fatalf("unsponsored available funds = %d, want 2", got)
	}
	if got := xrpLiquidAfterCheck(creator, ctx, false); got != 2 {
		t.Fatalf("unsponsored post-check liquid = %d, want 2", got)
	}
}
