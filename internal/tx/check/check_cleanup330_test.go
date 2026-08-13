package check

import (
	"context"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	xrpllog "github.com/LeJamon/go-xrpl/log"
)

func TestCheckZeroIDCleanup330Gate(t *testing.T) {
	zero := strings.Repeat("0", 64)
	off := amendment.EmptyRules()
	on := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_3_0})
	amount := tx.NewXRPAmount(1)

	cash := NewCheckCash("rBob", zero)
	cash.Amount = &amount
	cancel := NewCheckCancel("rBob", zero)

	if err := cash.PreflightWithRules(off); err != nil {
		t.Fatalf("legacy CheckCash preflight = %v", err)
	}
	if err := cancel.PreflightWithRules(off); err != nil {
		t.Fatalf("legacy CheckCancel preflight = %v", err)
	}
	assertCheckResultError(t, cash.PreflightWithRules(on), ter.TemMALFORMED)
	assertCheckResultError(t, cancel.PreflightWithRules(on), ter.TemMALFORMED)

	ctx := &tx.ApplyContext{View: newCheckMPTView(), Log: xrpllog.Discard(), Ctx: context.Background()}
	if got := cash.Apply(ctx); got != ter.TecNO_ENTRY {
		t.Fatalf("legacy CheckCash zero ID = %v, want tecNO_ENTRY", got)
	}
	if got := cancel.Preclaim(ctx.View, tx.EngineConfig{}); got != ter.TecNO_ENTRY {
		t.Fatalf("legacy CheckCancel zero ID = %v, want tecNO_ENTRY", got)
	}
}

func TestCheckCashZeroIDPrecedesBodyValidation(t *testing.T) {
	cash := NewCheckCash("rBob", strings.Repeat("0", 64))
	rules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_3_0})
	assertCheckResultError(t, cash.PreflightWithRules(rules), ter.TemMALFORMED)

	if err := cash.PreflightWithRules(amendment.EmptyRules()); err == nil || err.Error() != "temMALFORMED: must specify exactly one of Amount or DeliverMin" {
		t.Fatalf("legacy body validation = %v", err)
	}
}
