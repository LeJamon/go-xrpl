package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// batchRules builds a rules set from the all-supported preset with the named
// amendments additionally enabled. Batch and fixBatchInnerSigs are Supported::no
// upstream, so they are absent from the preset and must be enabled explicitly.
func batchRules(names ...string) *amendment.Rules {
	b := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
	for _, n := range names {
		b.EnableByName(n)
	}
	return b.Build()
}

func innerFlaggedTx() *txcore.Common {
	tx := txcore.NewBaseTx(txcore.TypePayment, precedenceSourceAddr)
	flags := txcore.TfInnerBatchTxn
	tx.Flags = &flags
	return tx.GetCommon()
}

// TestPreflightInnerBatchFlag exercises the tfInnerBatchTxn rejection on a
// directly-submitted transaction across the Batch and fixBatchInnerSigs gates,
// mirroring rippled's Transactor::preflight1 (temINVALID_FLAG when Batch is
// disabled) and apply.cpp checkValidity (fixBatchInnerSigs, PR #6069).
func TestPreflightInnerBatchFlag(t *testing.T) {
	tests := []struct {
		name  string
		rules *amendment.Rules
		want  ter.Result
	}{
		{
			// The flag is undefined without Batch → invalid flag.
			name:  "batch disabled",
			rules: batchRules(),
			want:  ter.TemINVALID_FLAG,
		},
		{
			// Pre-fix: the transaction reached the engine and failed with
			// temINVALID_FLAG (checkValidity short-circuited it to Valid).
			name:  "batch enabled, fix disabled",
			rules: batchRules("Batch"),
			want:  ter.TemINVALID_FLAG,
		},
		{
			// Post-fix: an inner-flagged transaction never has a valid
			// signature, so it is rejected as invalid before applying.
			name:  "batch enabled, fix enabled",
			rules: batchRules("Batch", "fixBatchInnerSigs"),
			want:  ter.TemINVALID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewEngine(newMockBaseView(), txcore.EngineConfig{Rules: tt.rules})
			if got := e.preflightInnerBatchFlag(innerFlaggedTx()); got != tt.want {
				t.Fatalf("preflightInnerBatchFlag = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPreflightInnerBatchFlag_AbsentFlag confirms an ordinary transaction (no
// tfInnerBatchTxn) is unaffected by the gate regardless of the amendments.
func TestPreflightInnerBatchFlag_AbsentFlag(t *testing.T) {
	tx := txcore.NewBaseTx(txcore.TypePayment, precedenceSourceAddr)
	e := NewEngine(newMockBaseView(), txcore.EngineConfig{Rules: batchRules("Batch", "fixBatchInnerSigs")})
	if got := e.preflightInnerBatchFlag(tx.GetCommon()); got != ter.TesSUCCESS {
		t.Fatalf("preflightInnerBatchFlag(no flag) = %v, want TesSUCCESS", got)
	}
}
