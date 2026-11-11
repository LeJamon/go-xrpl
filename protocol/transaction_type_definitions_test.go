package protocol_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TestTxTypeNamesMatchDefinitions keeps the transaction type name table in
// lockstep with the codec's TRANSACTION_TYPES.
func TestTxTypeNamesMatchDefinitions(t *testing.T) {
	want := 0
	for name, code := range definitions.Get().TransactionTypes() {
		if code < 0 {
			continue
		}
		want++
		if got, ok := protocol.TxTypeFromName(name); !ok || got != protocol.TxType(code) {
			t.Errorf("TxTypeFromName(%q) = (%d, %t), definitions code %d", name, got, ok, code)
		}
		if got := protocol.TxType(code).String(); got != name {
			t.Errorf("TxType(%d).String() = %q, definitions name %q", code, got, name)
		}
	}
	got := 0
	for code := range 0xFFFF {
		if _, ok := protocol.TxTypeFromName(protocol.TxType(code).String()); ok {
			got++
		}
	}
	if got != want {
		t.Errorf("protocol names %d transaction types, definitions %d", got, want)
	}
}
