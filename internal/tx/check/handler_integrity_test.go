package check

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestCheckCreateFreezeReadFailuresAreInternal(t *testing.T) {
	issuerID := checkMPTAccountID(0x11)
	sourceID := checkMPTAccountID(0x22)
	destinationID := checkMPTAccountID(0x33)
	view := newCheckMPTView()
	lineKey := keylet.Line(sourceID, issuerID, "USD")
	view.readErrors[lineKey.Key] = errors.New("storage failure")

	if frozen, err := tx.IsTrustlineFrozenBy(view, issuerID, sourceID, "USD"); err == nil || frozen {
		t.Fatalf("issuer freeze lookup = (%v, %v), want (false, error)", frozen, err)
	}
	delete(view.readErrors, lineKey.Key)
	if frozen, err := tx.IsTrustlineFrozenBy(view, issuerID, sourceID, "USD"); err != nil || frozen {
		t.Fatalf("missing issuer line = (%v, %v), want (false, nil)", frozen, err)
	}

	lineKey = keylet.Line(destinationID, issuerID, "USD")
	view.readErrors[lineKey.Key] = errors.New("storage failure")
	if frozen, err := tx.IsTrustlineFrozenBy(view, destinationID, issuerID, "USD"); err == nil || frozen {
		t.Fatalf("self freeze lookup = (%v, %v), want (false, error)", frozen, err)
	}
}

func TestCheckCashReadFailuresAreNotAbsence(t *testing.T) {
	sourceID := checkMPTAccountID(0x41)
	destinationID := checkMPTAccountID(0x42)
	view := newCheckMPTView()
	destination := putCheckMPTAccount(t, view, destinationID, 0)
	putCheckMPTAccount(t, view, sourceID, 1)
	checkKey := keylet.Check(sourceID, 7)
	checkData, err := state.SerializeCheckFromData(&state.CheckData{
		Account:         sourceID,
		DestinationID:   destinationID,
		SendMax:         100,
		SendMaxAmount:   tx.NewXRPAmount(100),
		IsNativeSendMax: true,
		Sequence:        7,
	})
	if err != nil {
		t.Fatal(err)
	}
	view.data[checkKey.Key] = checkData
	cash := NewCheckCash(state.EncodeAccountIDSafe(destinationID), encodeCheckKey(checkKey.Key))
	cash.SetExactAmount(tx.NewXRPAmount(10))
	ctx := checkMPTContext(view, destination, destinationID)

	for _, key := range []keylet.Keylet{checkKey, keylet.Account(sourceID), keylet.Account(destinationID)} {
		view.readErrors[key.Key] = errors.New("storage failure")
		if got := cash.Apply(ctx); got != ter.TefINTERNAL {
			t.Fatalf("read failure for %x: got %v, want tefINTERNAL", key.Key, got)
		}
		delete(view.readErrors, key.Key)
	}

	delete(view.data, checkKey.Key)
	if got := cash.Apply(ctx); got != ter.TecNO_ENTRY {
		t.Fatalf("missing check: got %v, want tecNO_ENTRY", got)
	}
}

func TestCheckCancelCreatorReadFailureIsInternal(t *testing.T) {
	sourceID := checkMPTAccountID(0x51)
	destinationID := checkMPTAccountID(0x52)
	view := newCheckMPTView()
	destination := putCheckMPTAccount(t, view, destinationID, 0)
	putCheckMPTAccount(t, view, sourceID, 1)
	checkKey := keylet.Check(sourceID, 9)

	for _, owner := range [][20]byte{sourceID, destinationID} {
		if _, err := state.DirInsert(view, keylet.OwnerDir(owner), checkKey.Key, false, func(dir *state.DirectoryNode) {
			dir.Owner = owner
		}); err != nil {
			t.Fatal(err)
		}
	}
	checkData, err := state.SerializeCheckFromData(&state.CheckData{
		Account:         sourceID,
		DestinationID:   destinationID,
		SendMax:         100,
		SendMaxAmount:   tx.NewXRPAmount(100),
		IsNativeSendMax: true,
		Sequence:        9,
		HasDestNode:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	view.data[checkKey.Key] = checkData
	cancel := NewCheckCancel(state.EncodeAccountIDSafe(destinationID), encodeCheckKey(checkKey.Key))
	view.readErrors[checkKey.Key] = errors.New("storage failure")
	if got := cancel.Preclaim(view, tx.EngineConfig{}); got != ter.TefINTERNAL {
		t.Fatalf("check preclaim read failure: got %v, want tefINTERNAL", got)
	}
	if got := cancel.Apply(checkMPTContext(view, destination, destinationID)); got != ter.TefINTERNAL {
		t.Fatalf("check apply read failure: got %v, want tefINTERNAL", got)
	}
	delete(view.readErrors, checkKey.Key)
	view.readErrors[keylet.Account(sourceID).Key] = errors.New("storage failure")

	if got := cancel.Apply(checkMPTContext(view, destination, destinationID)); got != ter.TefINTERNAL {
		t.Fatalf("creator read failure: got %v, want tefINTERNAL", got)
	}
	if view.data[checkKey.Key] == nil {
		t.Fatal("check erased after creator read failure")
	}
	for _, owner := range [][20]byte{sourceID, destinationID} {
		found := false
		if err := state.DirForEach(view, keylet.OwnerDir(owner), func(item [32]byte) error {
			found = found || item == checkKey.Key
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatalf("check removed from %x owner directory after read failure", owner)
		}
	}
}

func encodeCheckKey(key [32]byte) string {
	const digits = "0123456789abcdef"
	encoded := make([]byte, len(key)*2)
	for i, value := range key {
		encoded[2*i] = digits[value>>4]
		encoded[2*i+1] = digits[value&0x0f]
	}
	return string(encoded)
}
