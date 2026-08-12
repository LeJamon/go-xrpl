package engine

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/check"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func checkCleanupEngine(t *testing.T, rules *amendment.Rules) *Engine {
	t.Helper()
	view := newMockBaseView()
	id, err := state.DecodeAccountID(precedenceSourceAddr)
	if err != nil {
		t.Fatal(err)
	}
	data, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  precedenceSourceAddr,
		Balance:  1_000_000,
		Sequence: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	view.data[keylet.Account(id).Key] = data
	return NewEngine(view, txcore.EngineConfig{
		BaseFee:                   10,
		OpenLedger:                true,
		LedgerSequence:            100,
		Rules:                     rules,
		SkipSignatureVerification: true,
	})
}

func TestCheckZeroIDCleanup330EnginePreflight(t *testing.T) {
	zero := strings.Repeat("0", 64)
	amount := txcore.NewXRPAmount(1)
	tests := []struct {
		name string
		tx   txcore.Transaction
	}{
		{
			name: "cash",
			tx: func() txcore.Transaction {
				cash := check.NewCheckCash(precedenceSourceAddr, zero)
				cash.Amount = &amount
				cash.Fee = "10"
				cash.Sequence = u32(5)
				return cash
			}(),
		},
		{
			name: "cancel",
			tx: func() txcore.Transaction {
				cancel := check.NewCheckCancel(precedenceSourceAddr, zero)
				cancel.Fee = "10"
				cancel.Sequence = u32(5)
				return cancel
			}(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			off := amendment.EmptyRules()
			if got := checkCleanupEngine(t, off).Preflight(tc.tx); got != ter.TesSUCCESS {
				t.Fatalf("legacy preflight = %v, want tesSUCCESS", got)
			}
			rules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_3_0})
			if got := checkCleanupEngine(t, rules).Preflight(tc.tx); got != ter.TemMALFORMED {
				t.Fatalf("fixed preflight = %v, want temMALFORMED", got)
			}
			if got := checkCleanupEngine(t, off).Apply(tc.tx).Result; got != ter.TecNO_ENTRY {
				t.Fatalf("legacy apply = %v, want tecNO_ENTRY", got)
			}
			if got := checkCleanupEngine(t, rules).Apply(tc.tx).Result; got != ter.TemMALFORMED {
				t.Fatalf("fixed apply = %v, want temMALFORMED", got)
			}
		})
	}
}

func TestCheckZeroIDCleanup330Precedence(t *testing.T) {
	zero := strings.Repeat("0", 64)
	rules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_3_0})
	amount := txcore.NewXRPAmount(1)

	badFee := check.NewCheckCash(precedenceSourceAddr, zero)
	badFee.Amount = &amount
	badFee.Fee = "-1"
	badFee.Sequence = u32(5)
	if got := checkCleanupEngine(t, rules).Preflight(badFee); got != ter.TemBAD_FEE {
		t.Fatalf("zero ID with bad fee = %v, want temBAD_FEE", got)
	}

	badFlags := check.NewCheckCancel(precedenceSourceAddr, zero)
	badFlags.Fee = "10"
	badFlags.Sequence = u32(5)
	flags := uint32(0x00020000)
	badFlags.Flags = &flags
	if got := checkCleanupEngine(t, rules).Preflight(badFlags); got != ter.TemINVALID_FLAG {
		t.Fatalf("zero ID with bad flags = %v, want temINVALID_FLAG", got)
	}
}
