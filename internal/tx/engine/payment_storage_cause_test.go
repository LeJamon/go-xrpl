package engine

import (
	"bytes"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

type paymentDestinationReadErrorView struct {
	*mockBaseView
	readErrorKey [32]byte
	err          error
}

type paymentApplyStorageView struct {
	*mockBaseView
	readErrorKey [32]byte
	err          error
	destroyed    drops.XRPAmount
}

func (v *paymentApplyStorageView) Read(k keylet.Keylet) ([]byte, error) {
	if k.Key == v.readErrorKey {
		return nil, v.err
	}
	return v.mockBaseView.Read(k)
}

func (v *paymentApplyStorageView) AdjustDropsDestroyed(amount drops.XRPAmount) error {
	v.destroyed += amount
	return nil
}

func (v *paymentApplyStorageView) ApplyAtomically(apply func(ledgercore.Writer) error) error {
	staged := &paymentApplyStorageView{
		mockBaseView: newMockBaseView(),
		readErrorKey: v.readErrorKey,
		err:          v.err,
		destroyed:    v.destroyed,
	}
	for key, data := range v.data {
		staged.data[key] = bytes.Clone(data)
	}
	if err := apply(staged); err != nil {
		return err
	}
	v.mockBaseView = staged.mockBaseView
	v.destroyed = staged.destroyed
	return nil
}

func (v *paymentDestinationReadErrorView) Read(k keylet.Keylet) ([]byte, error) {
	if k.Key == v.readErrorKey {
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
		mockBaseView: base,
		readErrorKey: destinationKey,
		err:          &shamap.MissingNodeError{Hash: missingHash},
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

	if result.Result != ter.TefEXCEPTION || result.Applied {
		t.Fatalf("apply result = (%s, applied=%t), want tefEXCEPTION and not applied", result.Result, result.Applied)
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
		mockBaseView: base,
		readErrorKey: keylet.Account(destination).Key,
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

func TestApplyPaymentPreservesSourceMissingNodeCause(t *testing.T) {
	const source = "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS"
	var destination [20]byte
	destination[19] = 9
	sourceID, err := state.DecodeAccountID(source)
	if err != nil {
		t.Fatal(err)
	}
	missingHash := [32]byte{0x43}
	view := &paymentDestinationReadErrorView{
		mockBaseView: newMockBaseView(),
		readErrorKey: keylet.Account(sourceID).Key,
		err:          &shamap.MissingNodeError{Hash: missingHash},
	}

	paymentTx := payment.NewPayment(source, state.EncodeAccountIDSafe(destination), txcore.NewXRPAmount(20_000))
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

	if result.Result != ter.TefEXCEPTION || result.Applied {
		t.Fatalf("apply result = (%s, applied=%t), want tefEXCEPTION and not applied", result.Result, result.Applied)
	}
	if !errors.Is(result.Cause, shamap.ErrNodeNotInStore) {
		t.Fatalf("apply cause = %v, want ErrNodeNotInStore", result.Cause)
	}
	var missing *shamap.MissingNodeError
	if !errors.As(result.Cause, &missing) || missing.Hash != missingHash {
		t.Fatalf("apply cause = %v, want missing node %x", result.Cause, missingHash)
	}
}

func TestApplyIOUPaymentPreservesApplyTrustlineMissingNodeCause(t *testing.T) {
	const source = "rMRxj8jED6ZCjtjgFxB4cz1MGVNtYqCEyS"
	var destination [20]byte
	destination[19] = 10
	destinationAddress := state.EncodeAccountIDSafe(destination)
	sourceID, err := state.DecodeAccountID(source)
	if err != nil {
		t.Fatal(err)
	}

	base := newMockBaseView()
	for id, balance := range map[string]uint64{source: 1_000_000_000, destinationAddress: 1_000_000_000} {
		accountID, decodeErr := state.DecodeAccountID(id)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		data, serializeErr := state.SerializeAccountRoot(&state.AccountRoot{
			Account:  id,
			Balance:  balance,
			Sequence: 1,
		})
		if serializeErr != nil {
			t.Fatal(serializeErr)
		}
		base.data[keylet.Account(accountID).Key] = data
	}

	missingHash := [32]byte{0x44}
	view := &paymentApplyStorageView{
		mockBaseView: base,
		readErrorKey: keylet.Line(sourceID, destination, "USD").Key,
		err:          &shamap.MissingNodeError{Hash: missingHash},
	}
	sourceKey := keylet.Account(sourceID)
	destinationKey := keylet.Account(destination)
	beforeSource := bytes.Clone(base.data[sourceKey.Key])
	beforeDestination := bytes.Clone(base.data[destinationKey.Key])
	beforeDestroyed := view.destroyed
	paymentTx := payment.NewPayment(
		source,
		destinationAddress,
		txcore.NewIssuedAmountFromFloat64(1, "USD", source),
	)
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

	if result.Result != ter.TefEXCEPTION || result.Applied {
		t.Fatalf("apply result = (%s, applied=%t), want tefEXCEPTION and not applied", result.Result, result.Applied)
	}
	if !errors.Is(result.Cause, shamap.ErrNodeNotInStore) {
		t.Fatalf("apply cause = %v, want ErrNodeNotInStore", result.Cause)
	}
	var missing *shamap.MissingNodeError
	if !errors.As(result.Cause, &missing) || missing.Hash != missingHash {
		t.Fatalf("apply cause = %v, want missing node %x", result.Cause, missingHash)
	}
	if got := base.data[sourceKey.Key]; !bytes.Equal(got, beforeSource) {
		t.Fatal("apply-phase storage failure changed the source account")
	}
	if got := base.data[destinationKey.Key]; !bytes.Equal(got, beforeDestination) {
		t.Fatal("apply-phase storage failure changed the destination account")
	}
	if view.destroyed != beforeDestroyed {
		t.Fatalf("destroyed drops = %d, want %d after failed apply", view.destroyed, beforeDestroyed)
	}
}
