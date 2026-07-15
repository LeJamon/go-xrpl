package applystate

import (
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
)

func TestMetadataBuildersRejectUnregisteredEntryType(t *testing.T) {
	data := []byte{0x11, 0x12, 0x34}
	entryType := state.EntryType(data)
	if entryType == "" {
		t.Fatal("test data did not encode a ledger entry type")
	}
	if ledgerfields.HasTyped(entryType) {
		t.Fatalf("test ledger entry type %q is registered", entryType)
	}

	table := &ApplyStateTable{}
	var key [32]byte
	tests := map[string]func() (tx.AffectedNode, error){
		"created":  func() (tx.AffectedNode, error) { return table.buildCreatedNode(key, data) },
		"modified": func() (tx.AffectedNode, error) { return table.buildModifiedNode(key, data, data) },
		"deleted":  func() (tx.AffectedNode, error) { return table.buildDeletedNode(key, data, data) },
	}

	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := build()
			if !errors.Is(err, errUnregisteredLedgerEntryType) {
				t.Fatalf("error = %v, want %v", err, errUnregisteredLedgerEntryType)
			}
			if !strings.Contains(err.Error(), entryType) {
				t.Fatalf("error %q does not identify entry type %q", err, entryType)
			}
		})
	}
}
