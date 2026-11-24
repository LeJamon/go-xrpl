package engine

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

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

func TestLoanSetCounterpartyPreflight(t *testing.T) {
	rules := amendment.NewRulesBuilder().Enable(amendment.FeatureLendingProtocol).
		Enable(amendment.FeatureSingleAssetVault).Enable(amendment.FeatureMPTokensV1)
	legacy := preflightEngine(rules.Build())
	current := preflightEngine(rules.Enable(amendment.FeatureBatchV1_1).Build())
	tests := []struct {
		name         string
		engine       *Engine
		inner        bool
		counterparty string
		signature    bool
		principal    string
		want         ter.Result
	}{
		{name: "inner requires counterparty", engine: current, inner: true, principal: "1", want: ter.TemBAD_SIGNER},
		{name: "counterparty precedes principal check", engine: current, inner: true, signature: true, principal: "0", want: ter.TemBAD_SIGNER},
		{name: "inner with counterparty", engine: current, inner: true, counterparty: precedenceGenesisAddr, principal: "1", want: ter.TesSUCCESS},
		{name: "before BatchV1_1", engine: legacy, inner: true, principal: "1", want: ter.TesSUCCESS},
		{name: "standalone requires signature", engine: current, counterparty: precedenceGenesisAddr, principal: "1", want: ter.TemBAD_SIGNER},
		{name: "standalone with signature", engine: current, signature: true, principal: "1", want: ter.TesSUCCESS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loan := lending.NewLoanSet(precedenceSourceAddr, strings.Repeat("1", 64), tc.principal)
			loan.SetSequence(1)
			loan.Fee = "20"
			loan.Counterparty = tc.counterparty
			if tc.signature {
				loan.CounterpartySignature = &txcore.CounterpartySignature{}
			}
			preflight := tc.engine.preflight
			if tc.inner {
				loan.SetFlags(txcore.TfInnerBatchTxn)
				loan.Fee = "0"
				preflight = tc.engine.preflightInner
			}
			if got := preflight(loan); got != tc.want {
				t.Fatalf("preflight = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestLoanSetCounterpartySignaturePreflight(t *testing.T) {
	rules := allRules()
	regular := preflightEngine(rules)
	simulate := preflightEngine(rules)
	simulate.config.ApplyFlags = txcore.TapDRY_RUN
	key := "02" + strings.Repeat("11", 32)
	mixed := &txcore.CounterpartySignature{SigningPubKey: key}
	mixed.MarkFieldPresent("Signers")
	tests := []struct {
		name      string
		signature *txcore.CounterpartySignature
		principal string
		zeroID    bool
		regular   ter.Result
		simulate  ter.Result
	}{
		{"bad key precedes principal", &txcore.CounterpartySignature{SigningPubKey: "04"}, "0", false, ter.TemBAD_SIGNATURE, ter.TemBAD_SIGNATURE},
		{"unsigned counterparty", &txcore.CounterpartySignature{SigningPubKey: key}, "1", false, ter.TesSUCCESS, ter.TesSUCCESS},
		{"counterparty signature bytes", &txcore.CounterpartySignature{TxnSignature: "AB"}, "1", false, ter.TesSUCCESS, ter.TemINVALID},
		{"signer signature bytes", &txcore.CounterpartySignature{Signers: []txcore.SignerWrapper{
			{Signer: txcore.Signer{Account: precedenceGenesisAddr, SigningPubKey: key, TxnSignature: "AB"}},
		}}, "1", false, ter.TesSUCCESS, ter.TemINVALID},
		{"signing key with present Signers", mixed, "1", false, ter.TesSUCCESS, ter.TemINVALID},
		{"simulation finishes before broker ID", &txcore.CounterpartySignature{}, "1", true, ter.TemINVALID, ter.TesSUCCESS},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := strings.Repeat("1", 64)
			if tc.zeroID {
				id = strings.Repeat("0", 64)
			}
			loan := lending.NewLoanSet(precedenceSourceAddr, id, tc.principal)
			loan.SetSequence(1)
			loan.Fee = "20"
			loan.CounterpartySignature = tc.signature
			if got := regular.preflight(loan); got != tc.regular {
				t.Fatalf("normal preflight = %s, want %s", got, tc.regular)
			}
			if got := simulate.preflight(loan); got != tc.simulate {
				t.Fatalf("simulation preflight = %s, want %s", got, tc.simulate)
			}
			if got := regular.preflight(loan); got != tc.regular {
				t.Fatalf("normal preflight after simulation = %s, want %s", got, tc.regular)
			}
		})
	}
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
