package engine

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// batchRules builds a rules set containing exactly the named amendments.
func batchRules(names ...string) *amendment.Rules {
	b := amendment.NewRulesBuilder()
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
// directly-submitted transaction across the BatchV1_1 gate.
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
			name:  "batch enabled",
			rules: batchRules("BatchV1_1"),
			want:  ter.TemINVALID_INNER_BATCH,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := preflightInnerBatchFlag(innerFlaggedTx(), tt.rules); got != tt.want {
				t.Fatalf("preflightInnerBatchFlag = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPreflightInnerBatchFlag_AbsentFlag confirms an ordinary transaction (no
// tfInnerBatchTxn) is unaffected by the gate regardless of the amendments.
func TestPreflightInnerBatchFlag_AbsentFlag(t *testing.T) {
	tx := txcore.NewBaseTx(txcore.TypePayment, precedenceSourceAddr)
	rules := batchRules("BatchV1_1")
	if got := preflightInnerBatchFlag(tx.GetCommon(), rules); got != ter.TesSUCCESS {
		t.Fatalf("preflightInnerBatchFlag(no flag) = %v, want TesSUCCESS", got)
	}
}

// rulesPreflightTx adopts RulesPreflighter with a fixed verdict.
type rulesPreflightTx struct {
	*txcore.BaseTx
	err error
}

func (t *rulesPreflightTx) PreflightRules(*amendment.Rules) error { return t.err }

// TestPreflightInner_RunsPerTypeSeams pins that inner-batch preflight runs the
// full per-type preflight body, not just Validate(). rippled routes inner txs
// through the same invokePreflight (Batch.cpp → preflight(stx, tapBATCH)), so
// FlagsMasker, CheckExtraFeatures and RulesPreflighter all apply — otherwise a
// type carrying half its preflight in PreflightRules (Clawback, MPTokenIssuanceSet)
// would let a malformed inner slip through to apply.
func TestPreflightInner_RunsPerTypeSeams(t *testing.T) {
	e := preflightEngine(allRules())

	t.Run("PreflightRules runs for inner tx", func(t *testing.T) {
		base := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
		tx := &rulesPreflightTx{BaseTx: base, err: ter.Errorf(ter.TemMALFORMED, "bad")}
		if got := e.preflightInner(tx); got != ter.TemMALFORMED {
			t.Fatalf("preflightInner = %v, want TemMALFORMED", got)
		}
	})

	t.Run("FlagsMasker runs for inner tx", func(t *testing.T) {
		bit := uint32(0x00010000)
		base := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
		base.Flags = &bit
		tx := &flagMaskTx{BaseTx: base, mask: bit}
		if got := e.preflightInner(tx); got != ter.TemINVALID_FLAG {
			t.Fatalf("preflightInner = %v, want TemINVALID_FLAG", got)
		}
	})

	t.Run("CheckExtraFeatures runs for inner tx", func(t *testing.T) {
		base := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
		tx := &extraFeaturesTx{BaseTx: base, err: ter.Errorf(ter.TemDISABLED, "disabled")}
		if got := e.preflightInner(tx); got != ter.TemDISABLED {
			t.Fatalf("preflightInner = %v, want TemDISABLED", got)
		}
	})

	t.Run("clean inner tx passes", func(t *testing.T) {
		base := txcore.NewBaseTx(txcore.TypeAccountSet, precedenceSourceAddr)
		if got := e.preflightInner(base); got != ter.TesSUCCESS {
			t.Fatalf("preflightInner = %v, want TesSUCCESS", got)
		}
	})
}
