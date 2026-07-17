package service

import (
	"bytes"
	"errors"
	"testing"

	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/shamap"
)

func TestSnapshotViewChecksKeyletType(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	key := [32]byte{1}
	data := bytes.Repeat([]byte{1}, 12)
	data[0] = 0x11
	data[1] = byte(entry.TypeAccountRoot >> 8)
	data[2] = byte(entry.TypeAccountRoot)
	if err := stateMap.Put(key, data); err != nil {
		t.Fatalf("seed state map: %v", err)
	}
	view := newSnapshotView(stateMap, nil)
	wrongType := keylet.Keylet{Type: entry.TypePermissionedDomain, Key: key}

	if got, err := view.Read(wrongType); err != nil || got != nil {
		t.Fatalf("Read wrong-type entry = %x, %v", got, err)
	}
	if exists, err := view.Exists(wrongType); err != nil || !exists {
		t.Fatalf("Exists wrong-type entry = %v, %v", exists, err)
	}
	if got, err := view.Read(keylet.Keylet{Key: key}); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Read ltANY entry = %x, %v", got, err)
	}
}

func TestSnapshotViewApplyAtomically(t *testing.T) {
	stateMap := shamap.New(shamap.TypeState)
	existing := keylet.Keylet{Key: [32]byte{1}}
	inserted := keylet.Keylet{Key: [32]byte{2}}
	original := bytes.Repeat([]byte{1}, 12)
	updated := bytes.Repeat([]byte{2}, 12)
	created := bytes.Repeat([]byte{3}, 12)
	if err := stateMap.Put(existing.Key, original); err != nil {
		t.Fatalf("seed state map: %v", err)
	}
	view := newSnapshotView(stateMap, nil)
	injected := errors.New("injected apply failure")

	err := view.ApplyAtomically(func(writer ledgercore.Writer) error {
		if err := writer.Update(existing, updated); err != nil {
			return err
		}
		if err := writer.Insert(inserted, created); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("ApplyAtomically error = %v, want %v", err, injected)
	}
	if got, _ := view.Read(existing); !bytes.Equal(got, original) {
		t.Fatalf("failed apply changed existing entry: got %v want %v", got, original)
	}
	if exists, _ := view.Exists(inserted); exists {
		t.Fatal("failed apply committed inserted entry")
	}

	err = view.ApplyAtomically(func(writer ledgercore.Writer) error {
		if err := writer.Update(existing, updated); err != nil {
			return err
		}
		return writer.Insert(inserted, created)
	})
	if err != nil {
		t.Fatalf("ApplyAtomically success: %v", err)
	}
	if got, _ := view.Read(existing); !bytes.Equal(got, updated) {
		t.Fatalf("successful apply existing entry = %v, want %v", got, updated)
	}
	if got, _ := view.Read(inserted); !bytes.Equal(got, created) {
		t.Fatalf("successful apply inserted entry = %v, want %v", got, created)
	}
}
