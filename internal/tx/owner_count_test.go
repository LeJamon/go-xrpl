package tx

import (
	"errors"
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

type ownerCountTestView struct {
	*mockBaseView
	readErr     error
	updateErr   error
	updateCalls int
}

func newOwnerCountTestView() *ownerCountTestView {
	return &ownerCountTestView{mockBaseView: newMockBaseView()}
}

func (v *ownerCountTestView) Read(k keylet.Keylet) ([]byte, error) {
	if v.readErr != nil {
		return nil, v.readErr
	}
	return v.mockBaseView.Read(k)
}

func (v *ownerCountTestView) Update(k keylet.Keylet, data []byte) error {
	v.updateCalls++
	if v.updateErr != nil {
		return v.updateErr
	}
	return v.mockBaseView.Update(k, data)
}

func TestConfineOwnerCount(t *testing.T) {
	tests := []struct {
		name       string
		current    uint32
		adjustment int
		want       uint32
	}{
		{"increment", 5, 3, 8},
		{"decrement", 5, -3, 2},
		{"zero adjustment", 5, 0, 5},
		{"decrement to zero", 5, -5, 0},
		{"underflow clamps to zero", 2, -5, 0},
		{"underflow from zero", 0, -1, 0},
		{"increment to max", math.MaxUint32 - 1, 1, math.MaxUint32},
		{"overflow saturates to max", math.MaxUint32, 1, math.MaxUint32},
		{"large overflow saturates", math.MaxUint32 - 1, 100, math.MaxUint32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := confineOwnerCount(tt.current, tt.adjustment); got != tt.want {
				t.Errorf("confineOwnerCount(%d, %d) = %d, want %d", tt.current, tt.adjustment, got, tt.want)
			}
		})
	}
}

func TestAdjustOwnerCount(t *testing.T) {
	var accountID [20]byte
	accountID[0] = 1
	accountKey := keylet.Account(accountID)

	t.Run("read failure", func(t *testing.T) {
		readErr := errors.New("storage unavailable")
		view := newOwnerCountTestView()
		view.readErr = readErr

		err := AdjustOwnerCount(view, accountID, 1)
		if !errors.Is(err, readErr) {
			t.Fatalf("AdjustOwnerCount error = %v, want wrapped read error", err)
		}
		if err == readErr {
			t.Fatal("AdjustOwnerCount returned the read error without context")
		}
		if view.updateCalls != 0 {
			t.Fatalf("Update calls = %d, want 0", view.updateCalls)
		}
	})

	t.Run("missing account", func(t *testing.T) {
		view := newOwnerCountTestView()

		if err := AdjustOwnerCount(view, accountID, 1); err != nil {
			t.Fatalf("AdjustOwnerCount: %v", err)
		}
		if view.updateCalls != 0 {
			t.Fatalf("Update calls = %d, want 0", view.updateCalls)
		}
	})

	t.Run("parse failure", func(t *testing.T) {
		view := newOwnerCountTestView()
		view.data[accountKey.Key] = []byte{0xff}

		if err := AdjustOwnerCount(view, accountID, 1); err == nil {
			t.Fatal("AdjustOwnerCount succeeded with malformed account data")
		}
		if view.updateCalls != 0 {
			t.Fatalf("Update calls = %d, want 0", view.updateCalls)
		}
	})

	t.Run("update failure", func(t *testing.T) {
		updateErr := errors.New("storage unavailable")
		view := newOwnerCountTestView()
		view.updateErr = updateErr
		view.data[accountKey.Key] = serializeOwnerCountAccount(t, accountID, 3)

		err := AdjustOwnerCount(view, accountID, 1)
		if !errors.Is(err, updateErr) {
			t.Fatalf("AdjustOwnerCount error = %v, want update error", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		view := newOwnerCountTestView()
		view.data[accountKey.Key] = serializeOwnerCountAccount(t, accountID, 3)

		if err := AdjustOwnerCount(view, accountID, 2); err != nil {
			t.Fatalf("AdjustOwnerCount: %v", err)
		}
		account, err := state.ParseAccountRoot(view.data[accountKey.Key])
		if err != nil {
			t.Fatalf("ParseAccountRoot: %v", err)
		}
		if account.OwnerCount != 5 {
			t.Fatalf("OwnerCount = %d, want 5", account.OwnerCount)
		}
		if view.updateCalls != 1 {
			t.Fatalf("Update calls = %d, want 1", view.updateCalls)
		}
	})
}

func serializeOwnerCountAccount(t *testing.T, accountID [20]byte, ownerCount uint32) []byte {
	t.Helper()
	accountAddress, err := state.EncodeAccountID(accountID)
	if err != nil {
		t.Fatalf("EncodeAccountID: %v", err)
	}
	data, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:    accountAddress,
		OwnerCount: ownerCount,
	})
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	return data
}
