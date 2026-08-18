package tx

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

type freezeTestView struct {
	*mockBaseView
	readErrors map[[32]byte]error
}

func newFreezeTestView() *freezeTestView {
	return &freezeTestView{
		mockBaseView: newMockBaseView(),
		readErrors:   make(map[[32]byte]error),
	}
}

func (v *freezeTestView) Read(k keylet.Keylet) ([]byte, error) {
	if err := v.readErrors[k.Key]; err != nil {
		return nil, err
	}
	return v.mockBaseView.Read(k)
}

func freezeAccountID(value byte) [20]byte {
	var id [20]byte
	id[0] = value
	return id
}

func putFreezeLine(t *testing.T, view *freezeTestView, lowID, highID [20]byte, flags uint32) {
	t.Helper()
	lowAddress := state.EncodeAccountIDSafe(lowID)
	highAddress := state.EncodeAccountIDSafe(highID)
	raw, err := state.SerializeRippleState(&state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(0, -100, "USD", lowAddress),
		LowLimit:  state.NewIssuedAmountFromValue(0, -100, "USD", lowAddress),
		HighLimit: state.NewIssuedAmountFromValue(0, -100, "USD", highAddress),
		Flags:     flags,
	})
	if err != nil {
		t.Fatalf("serialize trust line: %v", err)
	}
	view.data[keylet.Line(lowID, highID, "USD").Key] = raw
}

func putFreezeAccount(t *testing.T, view *freezeTestView, accountID [20]byte, flags uint32) {
	t.Helper()
	raw, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  state.EncodeAccountIDSafe(accountID),
		Balance:  1_000_000_000,
		Sequence: 1,
		Flags:    flags,
	})
	if err != nil {
		t.Fatalf("serialize account root: %v", err)
	}
	view.data[keylet.Account(accountID).Key] = raw
}

func TestIsRippleStateFrozenBy(t *testing.T) {
	lowID := freezeAccountID(1)
	highID := freezeAccountID(2)

	tests := []struct {
		name           string
		line           *state.RippleState
		freezerID      [20]byte
		counterpartyID [20]byte
		want           bool
	}{
		{"low side", &state.RippleState{Flags: state.LsfLowFreeze}, lowID, highID, true},
		{"low ignores high flag", &state.RippleState{Flags: state.LsfHighFreeze}, lowID, highID, false},
		{"high side", &state.RippleState{Flags: state.LsfHighFreeze}, highID, lowID, true},
		{"high ignores low flag", &state.RippleState{Flags: state.LsfLowFreeze}, highID, lowID, false},
		{"nil line", nil, lowID, highID, false},
		{"self", &state.RippleState{Flags: state.LsfLowFreeze | state.LsfHighFreeze}, lowID, lowID, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsRippleStateFrozenBy(test.line, test.freezerID, test.counterpartyID); got != test.want {
				t.Fatalf("IsRippleStateFrozenBy() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestIsTrustlineFrozenByContracts(t *testing.T) {
	lowID := freezeAccountID(1)
	highID := freezeAccountID(2)
	view := newFreezeTestView()
	putFreezeLine(t, view, lowID, highID, state.LsfHighFreeze)

	frozen, err := IsTrustlineFrozenBy(view, highID, lowID, "USD")
	if err != nil || !frozen {
		t.Fatalf("high-side freeze = (%v, %v), want (true, nil)", frozen, err)
	}
	frozen, err = IsTrustlineFrozenBy(view, lowID, highID, "USD")
	if err != nil || frozen {
		t.Fatalf("low-side freeze = (%v, %v), want (false, nil)", frozen, err)
	}

	for _, test := range []struct {
		name      string
		accountID [20]byte
		peerID    [20]byte
		currency  string
	}{
		{"missing", highID, lowID, "EUR"},
		{"XRP", highID, lowID, "XRP"},
		{"self", highID, highID, "USD"},
	} {
		t.Run(test.name, func(t *testing.T) {
			frozen, err := IsTrustlineFrozenBy(view, test.accountID, test.peerID, test.currency)
			if err != nil || frozen {
				t.Fatalf("freeze lookup = (%v, %v), want (false, nil)", frozen, err)
			}
		})
	}

	lineKey := keylet.Line(lowID, highID, "USD")
	readErr := errors.New("read failed")
	view.readErrors[lineKey.Key] = readErr
	if frozen, err := IsTrustlineFrozenBy(view, highID, lowID, "USD"); !errors.Is(err, readErr) || frozen {
		t.Fatalf("read failure = (%v, %v), want (false, read error)", frozen, err)
	}
	if IsTrustlineFrozen(view, lowID, highID, "USD") {
		t.Fatal("boolean wrapper reported frozen after read failure")
	}

	delete(view.readErrors, lineKey.Key)
	view.data[lineKey.Key] = []byte{0xff}
	if frozen, err := IsTrustlineFrozenBy(view, highID, lowID, "USD"); err == nil || frozen {
		t.Fatalf("parse failure = (%v, %v), want (false, error)", frozen, err)
	}
	if IsTrustlineFrozen(view, lowID, highID, "USD") {
		t.Fatal("boolean wrapper reported frozen after parse failure")
	}
}

func TestIsIOUFrozenContracts(t *testing.T) {
	accountID := freezeAccountID(1)
	issuerID := freezeAccountID(2)
	issuerAddress := state.EncodeAccountIDSafe(issuerID)
	asset := Asset{Currency: "USD", Issuer: issuerAddress}
	view := newFreezeTestView()

	putFreezeAccount(t, view, issuerID, state.LsfGlobalFreeze)
	frozen, err := IsIOUFrozen(view, issuerID, issuerID, "USD")
	if err != nil || !frozen {
		t.Fatalf("self-issued global freeze = (%v, %v), want (true, nil)", frozen, err)
	}
	if IsIndividualFrozen(view, issuerID, asset) {
		t.Fatal("self-issued asset reported individually frozen")
	}
	if !IsFrozen(view, issuerID, asset) {
		t.Fatal("self-issued asset did not retain global freeze")
	}

	putFreezeAccount(t, view, issuerID, 0)
	putFreezeLine(t, view, accountID, issuerID, state.LsfHighFreeze)
	frozen, err = IsIOUFrozen(view, accountID, issuerID, "USD")
	if err != nil || !frozen {
		t.Fatalf("individual freeze = (%v, %v), want (true, nil)", frozen, err)
	}
	if !IsIndividualFrozen(view, accountID, asset) {
		t.Fatal("asset did not report issuer-side individual freeze")
	}

	accountKey := keylet.Account(issuerID)
	readErr := errors.New("read failed")
	view.readErrors[accountKey.Key] = readErr
	if frozen, err := IsIOUFrozen(view, accountID, issuerID, "USD"); !errors.Is(err, readErr) || frozen {
		t.Fatalf("global read failure = (%v, %v), want (false, read error)", frozen, err)
	}
	if !IsFrozen(view, accountID, asset) {
		t.Fatal("boolean full helper failed to check the trust line after a global-freeze read failure")
	}

	delete(view.readErrors, accountKey.Key)
	view.data[accountKey.Key] = []byte{0xff}
	if frozen, err := IsIOUFrozen(view, accountID, issuerID, "USD"); err == nil || frozen {
		t.Fatalf("global parse failure = (%v, %v), want (false, error)", frozen, err)
	}
	if !IsFrozen(view, accountID, asset) {
		t.Fatal("boolean full helper failed to check the trust line after a global-freeze parse failure")
	}
}
