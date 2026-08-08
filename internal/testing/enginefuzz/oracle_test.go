package enginefuzz

import (
	"math"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

func TestClassifySafetyOutcomeRejectsCatastrophicAndUnknownResults(t *testing.T) {
	for _, result := range []ter.Result{
		ter.TefINTERNAL,
		ter.TefEXCEPTION,
		ter.TefBAD_LEDGER,
		ter.TefFAILURE,
		ter.TecINTERNAL,
		ter.TecINVARIANT_FAILED,
		ter.TefINVARIANT_FAILED,
		ter.Result(12_345),
	} {
		t.Run(result.String(), func(t *testing.T) {
			got := jtx.TxResult{Result: result, Code: result.String()}
			if err := classifySafetyOutcome("negative-control", got, profileV320, 7); err == nil {
				t.Fatalf("result %d (%q) was accepted", result, result.String())
			}
		})
	}

	if err := classifySafetyOutcome("negative-control", jtx.TxResult{Result: ter.TesSUCCESS}, profileV320, 8); err == nil {
		t.Fatal("empty result code was accepted")
	}
}

func TestAddXRPRejectsOverflow(t *testing.T) {
	if _, err := addXRP(math.MaxUint64, 1); err == nil {
		t.Fatal("overflow was accepted")
	}
	if got, err := addXRP(math.MaxUint64-1, 1); err != nil || got != math.MaxUint64 {
		t.Fatalf("non-overflowing boundary = %d, %v", got, err)
	}
}

func TestHarnessRejectsForcedInvariantViolation(t *testing.T) {
	sc := newScenario(t, profileV320)
	firstPass := true
	sc.env.SetInvariantViolationHook(func(_ ter.Result, _ *applystate.ApplyStateTable) *txengine.InvariantViolationValue {
		if firstPass {
			firstPass = false
			return txengine.NewInvariantViolation("forced", "negative control")
		}
		return nil
	})

	payer, destination := sc.pair(0, 1)
	_, err := sc.submitAndCheck(0, "forced-invariant", payer, payment.Pay(payer, destination, 1).Build(), 0, func(result jtx.TxResult) error {
		return classifySafetyOutcome("forced-invariant", result, profileV320, 0)
	})
	if err == nil {
		t.Fatal("forced invariant violation was accepted")
	}
	if !strings.Contains(err.Error(), ter.TecINVARIANT_FAILED.String()) {
		t.Fatalf("forced invariant error = %v", err)
	}
}
