package payment

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

type keyletReadView interface {
	Read(keylet.Keylet) ([]byte, error)
	Exists(keylet.Keylet) (bool, error)
}

func sandboxEntryData(typ entry.Type, tag byte) []byte {
	return []byte{0x11, byte(typ >> 8), byte(typ), tag}
}

func requireWrongTypeReadAbsent(t *testing.T, view keyletReadView, k keylet.Keylet) {
	t.Helper()
	if got, err := view.Read(k); err != nil || got != nil {
		t.Fatalf("Read wrong-type entry = %x, %v", got, err)
	}
}

func requireWrongTypeAbsent(t *testing.T, view keyletReadView, k keylet.Keylet) {
	t.Helper()
	requireWrongTypeReadAbsent(t, view, k)
	if exists, err := view.Exists(k); err != nil || exists {
		t.Fatalf("Exists wrong-type entry = %v, %v", exists, err)
	}
}

func TestPaymentSandboxChecksKeyletType(t *testing.T) {
	view := newPaymentMockLedgerView()
	baseKey := [32]byte{1}
	baseData := sandboxEntryData(entry.TypeAccountRoot, 1)
	view.data[baseKey] = baseData
	sandbox := NewPaymentSandbox(view)
	wrongBase := keylet.Keylet{Type: entry.TypePermissionedDomain, Key: baseKey}

	requireWrongTypeReadAbsent(t, sandbox, wrongBase)
	if exists, err := sandbox.Exists(wrongBase); err != nil || !exists {
		t.Fatalf("Exists wrong-type base entry = %v, %v", exists, err)
	}
	if got, err := sandbox.Read(keylet.Keylet{Key: baseKey}); err != nil || !bytes.Equal(got, baseData) {
		t.Fatalf("Read ltANY base entry = %x, %v", got, err)
	}

	modifiedData := sandboxEntryData(entry.TypeAccountRoot, 2)
	if err := sandbox.Update(keylet.Keylet{Type: entry.TypeAccountRoot, Key: baseKey}, modifiedData); err != nil {
		t.Fatalf("Update: %v", err)
	}
	requireWrongTypeAbsent(t, sandbox, wrongBase)

	insertedKey := [32]byte{2}
	insertedData := sandboxEntryData(entry.TypeAccountRoot, 3)
	if err := sandbox.Insert(keylet.Keylet{Type: entry.TypeAccountRoot, Key: insertedKey}, insertedData); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	wrongInserted := keylet.Keylet{Type: entry.TypePermissionedDomain, Key: insertedKey}
	requireWrongTypeAbsent(t, sandbox, wrongInserted)
	if got, err := sandbox.Read(keylet.Keylet{Key: insertedKey}); err != nil || !bytes.Equal(got, insertedData) {
		t.Fatalf("Read ltANY inserted entry = %x, %v", got, err)
	}

	child := NewChildSandbox(sandbox)
	requireWrongTypeAbsent(t, child, wrongInserted)
}
