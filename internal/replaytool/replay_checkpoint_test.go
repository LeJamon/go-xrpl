package replaytool

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/LeJamon/go-xrpl/shamap/backend"
)

type checkpointFaultFamily struct {
	shamap.Family
	fetches int
	err     error
}

func (f *checkpointFaultFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.fetches++
	if f.fetches > 1 {
		return nil, f.err
	}
	return f.Family.Fetch(ctx, hash)
}

func TestCheckpointRejectsCorruptStructure(t *testing.T) {
	dir := t.TempDir()
	stateMap := shamap.New(shamap.TypeState)
	for i := byte(1); i <= 2; i++ {
		var key [32]byte
		key[31] = i
		if err := stateMap.Put(key, []byte{i, i + 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeCheckpoint(context.Background(), dir, 10, stateMap); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(checkpointPath(dir, 10))
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func([]byte) []byte{
		"checksum": func(data []byte) []byte {
			data[checkpointHeaderSize] ^= 0xff
			return data
		},
		"impossible count": func(data []byte) []byte {
			binary.BigEndian.PutUint64(data[len(data)-int(checkpointFooterSize):], math.MaxUint64)
			sealCheckpoint(data)
			return data
		},
		"oversized entry": func(data []byte) []byte {
			binary.BigEndian.PutUint32(data[checkpointHeaderSize+32:], checkpointMaxEntrySize+1)
			sealCheckpoint(data)
			return data
		},
		"duplicate key": func(data []byte) []byte {
			firstRecord := int(checkpointHeaderSize)
			firstLength := int(binary.BigEndian.Uint32(data[firstRecord+32:]))
			secondRecord := firstRecord + int(checkpointRecordHeaderSize) + firstLength
			copy(data[secondRecord:secondRecord+32], data[firstRecord:firstRecord+32])
			sealCheckpoint(data)
			return data
		},
		"trailing record byte": func(data []byte) []byte {
			footer := len(data) - int(checkpointFooterSize)
			withExtra := make([]byte, 0, len(data)+1)
			withExtra = append(withExtra, data[:footer]...)
			withExtra = append(withExtra, 0)
			withExtra = append(withExtra, data[footer:]...)
			sealCheckpoint(withExtra)
			return withExtra
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			data := mutate(append([]byte(nil), valid...))
			path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".dat")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadCheckpoint(context.Background(), path); err == nil {
				t.Fatal("expected corrupt checkpoint to fail")
			}
		})
	}
}

func TestCheckpointRejectsLegacyVersion(t *testing.T) {
	data := make([]byte, checkpointHeaderSize+checkpointFooterSize)
	copy(data, checkpointMagic)
	binary.BigEndian.PutUint32(data[len(checkpointMagic):], 1)
	sealCheckpoint(data)
	path := filepath.Join(t.TempDir(), "legacy.dat")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadCheckpoint(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "not integrity protected") {
		t.Fatalf("load legacy checkpoint error = %v", err)
	}
}

func TestCheckpointCancellationPreservesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := checkpointPath(dir, 77)
	prior := []byte("prior checkpoint")
	if err := os.WriteFile(path, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := writeCheckpoint(ctx, dir, 77, shamap.New(shamap.TypeState))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeCheckpoint error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(prior) {
		t.Fatalf("existing checkpoint changed: %q", got)
	}
	temps, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary checkpoints remain: %v", temps)
	}
	if _, _, err := loadCheckpoint(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("loadCheckpoint error = %v", err)
	}
}

func TestCheckpointBackedTraversalFailurePreservesExistingFile(t *testing.T) {
	store := backend.NewMemory()
	t.Cleanup(func() { _ = store.Close() })
	source, err := shamap.NewBacked(shamap.TypeState, store)
	if err != nil {
		t.Fatal(err)
	}
	for i := byte(1); i <= 32; i++ {
		var key [32]byte
		key[0], key[31] = i, i
		if err := source.Put(key, append([]byte{i}, make([]byte, 11)...)); err != nil {
			t.Fatal(err)
		}
	}
	root, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	batch, err := source.FlushDirtyAndRelease()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StoreBatch(context.Background(), batch.Entries); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	goodLazy, err := shamap.NewFromRootHash(shamap.TypeState, root, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCheckpoint(context.Background(), dir, 87, goodLazy); err != nil {
		t.Fatalf("backed checkpoint round trip write: %v", err)
	}
	loaded, _, err := loadCheckpoint(context.Background(), checkpointPath(dir, 87))
	if err != nil {
		t.Fatalf("backed checkpoint round trip load: %v", err)
	}
	loadedRoot, err := loaded.Hash()
	if err != nil || loadedRoot != root {
		t.Fatalf("backed checkpoint root = %x, %v; want %x", loadedRoot, err, root)
	}
	sentinel := errors.New("faulting backed family")
	lazy, err := shamap.NewFromRootHash(shamap.TypeState, root, &checkpointFaultFamily{Family: store, err: sentinel})
	if err != nil {
		t.Fatal(err)
	}

	path := checkpointPath(dir, 88)
	prior := []byte("prior checkpoint")
	if err := os.WriteFile(path, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeCheckpoint(context.Background(), dir, 88, lazy); !errors.Is(err, sentinel) {
		t.Fatalf("writeCheckpoint error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(prior) {
		t.Fatalf("existing checkpoint changed: %q", got)
	}
}

func sealCheckpoint(data []byte) {
	digest := sha256.Sum256(data[:len(data)-sha256.Size])
	copy(data[len(data)-sha256.Size:], digest[:])
}
