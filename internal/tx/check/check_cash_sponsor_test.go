package check

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
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

	fixture := newCheckCashPreclaimFixture(t, 101)
	checkData, err := state.SerializeCheckFromData(fixture.check)
	if err != nil {
		t.Fatal(err)
	}
	sponsoredCheck, err := tx.SetLedgerEntrySponsor(checkData, "Sponsor", fixture.cash.Account)
	if err != nil {
		t.Fatal(err)
	}
	requested := tx.NewXRPAmount(1)
	if got := checkCashSourceFunds(fixture.view, ctx.Config, creator, fixture.source, requested, sponsoredCheck); got != ter.TecPATH_PARTIAL {
		t.Fatalf("sponsored preclaim funding = %s, want tecPATH_PARTIAL", got)
	}
	if got := xrpLiquidAfterCheck(creator, ctx, true); got != 0 {
		t.Fatalf("sponsored post-check liquid = %d, want 0", got)
	}
	if got := checkCashSourceFunds(fixture.view, ctx.Config, creator, fixture.source, requested, checkData); got != ter.TesSUCCESS {
		t.Fatalf("unsponsored preclaim funding = %s, want tesSUCCESS", got)
	}
	if got := xrpLiquidAfterCheck(creator, ctx, false); got != 2 {
		t.Fatalf("unsponsored post-check liquid = %d, want 2", got)
	}
}
