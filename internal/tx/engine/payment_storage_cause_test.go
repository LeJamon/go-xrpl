package engine

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

type paymentDestinationReadErrorView struct {
	*mockBaseView
	destinationKey [32]byte
	err            error
}

func (v *paymentDestinationReadErrorView) Read(k keylet.Keylet) ([]byte, error) {
	if k.Key == v.destinationKey {
		return nil, v.err
	}
	return v.mockBaseView.Read(k)
}

func TestApplyPaymentPreservesDestinationMissingNodeCause(t *testing.T) {
	const source = "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS"
	var destination [20]byte
	destination[19] = 7
	destinationAddress := state.EncodeAccountIDSafe(destination)
	destinationKey := keylet.Account(destination).Key
	missingHash := [32]byte{0x42}

	base := newMockBaseView()
	sourceID, err := state.DecodeAccountID(source)
	if err != nil {
		t.Fatal(err)
	}
	sourceData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  source,
		Balance:  1_000_000,
		Sequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	base.data[keylet.Account(sourceID).Key] = sourceData
	view := &paymentDestinationReadErrorView{
		mockBaseView:   base,
		destinationKey: destinationKey,
		err:            &shamap.MissingNodeError{Hash: missingHash},
	}

	paymentTx := payment.NewPayment(source, destinationAddress, txcore.NewXRPAmount(20_000))
	paymentTx.Common.Fee = "10"
	sequence := uint32(1)
	paymentTx.Common.Sequence = &sequence

	engine := NewEngine(view, txcore.EngineConfig{
		BaseFee:                   10,
		LedgerSequence:            100,
		Rules:                     amendment.AllSupportedRules(),
		SkipSignatureVerification: true,
	})
	result := engine.Apply(paymentTx)

	if result.Result != ter.TefINTERNAL || result.Applied {
		t.Fatalf("apply result = (%s, applied=%t), want tefINTERNAL and not applied", result.Result, result.Applied)
	}
	if !errors.Is(result.Cause, shamap.ErrNodeNotInStore) {
		t.Fatalf("apply cause = %v, want ErrNodeNotInStore", result.Cause)
	}
	var missing *shamap.MissingNodeError
	if !errors.As(result.Cause, &missing) || missing.Hash != missingHash {
		t.Fatalf("apply cause = %v, want missing node %x", result.Cause, missingHash)
	}
}

func TestApplyPaymentProtocolFailureHasNoStorageCause(t *testing.T) {
	const source = "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS"
	var destination [20]byte
	destination[19] = 8

	base := newMockBaseView()
	sourceID, err := state.DecodeAccountID(source)
	if err != nil {
		t.Fatal(err)
	}
	sourceData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  source,
		Balance:  1_000_000_000,
		Sequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	base.data[keylet.Account(sourceID).Key] = sourceData
	view := &paymentDestinationReadErrorView{
		mockBaseView:   base,
		destinationKey: keylet.Account(destination).Key,
	}

	paymentTx := payment.NewPayment(source, state.EncodeAccountIDSafe(destination), txcore.NewXRPAmount(20_000))
	paymentTx.Common.Fee = "10"
	sequence := uint32(1)
	paymentTx.Common.Sequence = &sequence

	engine := NewEngine(view, txcore.EngineConfig{
		BaseFee:                   10,
		ReserveBase:               200_000_000,
		LedgerSequence:            100,
		Rules:                     amendment.AllSupportedRules(),
		SkipSignatureVerification: true,
	})
	result := engine.Apply(paymentTx)

	if result.Result != ter.TecNO_DST_INSUF_XRP || !result.Applied {
		t.Fatalf("apply result = (%s, applied=%t), want applied tecNO_DST_INSUF_XRP", result.Result, result.Applied)
	}
	if result.Cause != nil {
		t.Fatalf("protocol result carried storage cause: %v", result.Cause)
	}
}
