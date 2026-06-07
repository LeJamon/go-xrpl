package escrow

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// feeOnlyView is a minimal LedgerView that serves one FeeSettings entry (for
// keylet.Fees()) and is empty otherwise — enough to drive the Preclaim
// extension-limit bounds against custom voted values without a full ledger.
type feeOnlyView struct{ feeData []byte }

func (v *feeOnlyView) Read(k keylet.Keylet) ([]byte, error) {
	if k.Key == keylet.Fees().Key {
		return v.feeData, nil
	}
	return nil, nil
}
func (v *feeOnlyView) Exists(keylet.Keylet) (bool, error)                 { return false, nil }
func (v *feeOnlyView) Insert(keylet.Keylet, []byte) error                 { return nil }
func (v *feeOnlyView) Update(keylet.Keylet, []byte) error                 { return nil }
func (v *feeOnlyView) Erase(keylet.Keylet) error                          { return nil }
func (v *feeOnlyView) AdjustDropsDestroyed(drops.XRPAmount)               {}
func (v *feeOnlyView) ForEach(func(key [32]byte, data []byte) bool) error { return nil }
func (v *feeOnlyView) Succ([32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v *feeOnlyView) TxExists([32]byte) bool  { return false }
func (v *feeOnlyView) Rules() *amendment.Rules { return nil }
func (v *feeOnlyView) LedgerSeq() uint32       { return 0 }

func feeViewWith(t *testing.T, fs *state.FeeSettings) *feeOnlyView {
	t.Helper()
	data, err := state.SerializeFeeSettings(fs)
	if err != nil {
		t.Fatalf("serialize fee settings: %v", err)
	}
	return &feeOnlyView{feeData: data}
}

func smartEscrowRules() *amendment.Rules {
	return amendment.NewRulesBuilder().
		Enable(amendment.FeatureSmartEscrow).
		Enable(amendment.FeatureFix1571).
		Build()
}

func finishFnEscrow(ff string) *EscrowCreate {
	code := ff
	return &EscrowCreate{
		BaseTx:         *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
		Amount:         tx.NewXRPAmount(1000000000),
		Destination:    "rBob",
		CancelAfter:    ptrUint32(700000000),
		FinishFunction: &code,
	}
}

// TestEscrowCreatePreclaim_FinishFunctionTooLarge: a FinishFunction larger than
// the voted extension size limit is temMALFORMED.
// Reference: rippled EscrowCreate.cpp preflight lines 222-228.
func TestEscrowCreatePreclaim_FinishFunctionTooLarge(t *testing.T) {
	view := feeViewWith(t, &state.FeeSettings{
		XRPFeesMode: true, BaseFeeDrops: 10,
		HasExtensionFees: true, ExtensionComputeLimit: 1_000_000, ExtensionSizeLimit: 10, GasPrice: 1_000_000,
	})
	cfg := tx.EngineConfig{BaseFee: 10, Rules: smartEscrowRules()}
	// feeTestFinishFn is 39 bytes, over the 10-byte limit.
	if got := finishFnEscrow(feeTestFinishFn).Preclaim(view, cfg); got != tx.TemMALFORMED {
		t.Fatalf("got %v, want temMALFORMED", got)
	}
}

// TestEscrowCreatePreclaim_RuntimeDisabled: a zero extension limit (WASM disabled
// by fee voting) makes a FinishFunction create temTEMP_DISABLED.
// Reference: rippled EscrowCreate.cpp preflight lines 216-220.
func TestEscrowCreatePreclaim_RuntimeDisabled(t *testing.T) {
	view := feeViewWith(t, &state.FeeSettings{
		XRPFeesMode: true, BaseFeeDrops: 10,
		HasExtensionFees: true, ExtensionComputeLimit: 0, ExtensionSizeLimit: 100_000, GasPrice: 1_000_000,
	})
	cfg := tx.EngineConfig{BaseFee: 10, Rules: smartEscrowRules()}
	if got := finishFnEscrow(feeTestFinishFn).Preclaim(view, cfg); got != tx.TemTEMP_DISABLED {
		t.Fatalf("got %v, want temTEMP_DISABLED", got)
	}
}
