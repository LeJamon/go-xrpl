package vault

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type holdingErrorView struct {
	tx.LedgerView
	holdingKey [32]byte
	err        error
}

func (v holdingErrorView) Exists(k keylet.Keylet) (bool, error) {
	if k.Key == v.holdingKey {
		return false, v.err
	}
	return v.LedgerView.Exists(k)
}

func TestVaultCleanupHoldingReadErrorPrecedesFreeze(t *testing.T) {
	issuer, holder := fill20(1), fill20(2)
	issuerAddress := state.EncodeAccountIDSafe(issuer)
	rules := amendment.NewRules([][32]byte{amendment.FeatureFixCleanup3_4_0})
	view := holdingErrorView{
		LedgerView: roView{
			data: map[[32]byte][]byte{
				keylet.Account(issuer).Key: mustAccountRoot(t, issuer, state.LsfGlobalFreeze),
			},
			rules: rules,
		},
		holdingKey: keylet.Line(issuer, holder, "USD").Key,
		err:        errors.New("holding read failed"),
	}
	ctx := &tx.ApplyContext{View: view, Config: tx.EngineConfig{Rules: rules}}
	delta, result := addEmptyHolding(ctx, holder, tx.Asset{Currency: "USD", Issuer: issuerAddress}, 0)
	if delta != 0 || result != ter.TefINTERNAL {
		t.Fatalf("holding read failure = %d/%v, want 0/tefINTERNAL", delta, result)
	}
}
