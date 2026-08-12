package engine

import (
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

type numberPhaseBarrier struct {
	arrived chan struct{}
	release chan struct{}
}

func newNumberPhaseBarrier() *numberPhaseBarrier {
	return &numberPhaseBarrier{
		arrived: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
}

func (b *numberPhaseBarrier) wait() {
	b.arrived <- struct{}{}
	<-b.release
}

type numberIsolationTx struct {
	*txcore.BaseTx
	preclaimBarrier *numberPhaseBarrier
	applyBarrier    *numberPhaseBarrier
	preclaimValue   string
	applyValue      string
}

func numberIsolationValue(ctx state.NumberContext) string {
	one := state.NewIssuedAmountFromValue(
		1_000_000_000_000_000,
		-15,
		"USD",
		"rIssuer",
	)
	small := state.NewIssuedAmountFromValue(
		6_000_000_000_000_000,
		-31,
		"USD",
		"rIssuer",
	)
	sum, err := one.AddWithNumberContext(small, ctx, state.RoundToNearest)
	if err != nil {
		panic(err)
	}
	return sum.Value()
}

func (t *numberIsolationTx) Preclaim(_ txcore.LedgerView, config txcore.EngineConfig) ter.Result {
	t.preclaimBarrier.wait()
	t.preclaimValue = numberIsolationValue(config.NumberContext())
	return ter.TesSUCCESS
}

func (t *numberIsolationTx) Apply(ctx *txcore.ApplyContext) ter.Result {
	t.applyBarrier.wait()
	t.applyValue = numberIsolationValue(ctx.NumberContext())
	return ter.TesSUCCESS
}

func TestConcurrentEnginesUseUniversalNumberForRetiredAmendment(t *testing.T) {
	preclaimBarrier := newNumberPhaseBarrier()
	applyBarrier := newNumberPhaseBarrier()

	universalView := newRecordingBaseView()
	omittedView := newRecordingBaseView()
	fundRecoveryAccount(t, universalView, 1_000_000, 1)
	fundRecoveryAccount(t, omittedView, 1_000_000, 1)

	universalRules := amendment.AllSupportedRules()
	omittedRules := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Disable(amendment.FeatureFixUniversalNumber).
		Build()

	newEngine := func(view *recordingBaseView, rules *amendment.Rules) *Engine {
		return NewEngine(view, txcore.EngineConfig{
			BaseFee:                   10,
			LedgerSequence:            100,
			Rules:                     rules,
			SkipSignatureVerification: true,
		})
	}

	universalTx := &numberIsolationTx{
		BaseTx:          recoveryTx(10, 1),
		preclaimBarrier: preclaimBarrier,
		applyBarrier:    applyBarrier,
	}
	omittedTx := &numberIsolationTx{
		BaseTx:          recoveryTx(10, 1),
		preclaimBarrier: preclaimBarrier,
		applyBarrier:    applyBarrier,
	}

	var universalResult, omittedResult txcore.ApplyResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		universalResult = newEngine(universalView, universalRules).Apply(universalTx)
	}()
	go func() {
		defer wg.Done()
		omittedResult = newEngine(omittedView, omittedRules).Apply(omittedTx)
	}()

	<-preclaimBarrier.arrived
	<-preclaimBarrier.arrived
	close(preclaimBarrier.release)
	<-applyBarrier.arrived
	<-applyBarrier.arrived
	close(applyBarrier.release)
	wg.Wait()

	if universalResult.Result != ter.TesSUCCESS || !universalResult.Applied {
		t.Fatalf(
			"universal engine result/applied = %s/%t",
			universalResult.Result,
			universalResult.Applied,
		)
	}
	if omittedResult.Result != ter.TesSUCCESS || !omittedResult.Applied {
		t.Fatalf(
			"omitted engine result/applied = %s/%t",
			omittedResult.Result,
			omittedResult.Applied,
		)
	}

	const universalWant = "1.000000000000001"
	if universalTx.preclaimValue != universalWant || universalTx.applyValue != universalWant {
		t.Fatalf(
			"universal preclaim/apply = %s/%s, want %s/%s",
			universalTx.preclaimValue,
			universalTx.applyValue,
			universalWant,
			universalWant,
		)
	}
	if omittedTx.preclaimValue != universalWant || omittedTx.applyValue != universalWant {
		t.Fatalf(
			"omitted preclaim/apply = %s/%s, want %s/%s",
			omittedTx.preclaimValue,
			omittedTx.applyValue,
			universalWant,
			universalWant,
		)
	}
}
