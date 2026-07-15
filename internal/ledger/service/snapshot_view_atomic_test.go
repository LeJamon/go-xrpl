package service

import (
	"bytes"
	"errors"
	"testing"

	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

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
