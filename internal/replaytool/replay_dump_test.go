package replaytool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/statecompare"
	"github.com/LeJamon/go-xrpl/shamap"
)

func TestCompareStates(t *testing.T) {
	before := map[string]statecompare.ComparableStateEntry{
		"aa": {Index: "AA", DataHex: "00", Decoded: map[string]any{"Balance": "1"}},
		"bb": {Index: "BB", DataHex: "11"},
		"dd": {Index: "DD", DataHex: "aBcD"},
	}
	after := map[string]statecompare.ComparableStateEntry{
		"AA": {Index: "AA", DataHex: "01", Decoded: map[string]any{"Balance": "2"}},
		"cc": {Index: "CC", DataHex: "22"},
		"dd": {Index: "DD", DataHex: "AbCd"},
	}

	got, err := statecompare.CompareStates(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Added) != 1 || got.Added[0].Index != "CC" {
		t.Fatalf("added = %+v", got.Added)
	}
	if len(got.Removed) != 1 || got.Removed[0].Index != "BB" {
		t.Fatalf("removed = %+v", got.Removed)
	}
	if len(got.Modified) != 1 || got.Modified[0].Index != "AA" {
		t.Fatalf("modified = %+v", got.Modified)
	}
	if !reflect.DeepEqual(got.Modified[0].ChangedKeys, []string{"Balance"}) {
		t.Fatalf("changed keys = %v", got.Modified[0].ChangedKeys)
	}
	if len(got.Unchanged) != 1 || got.Unchanged[0].Index != "DD" {
		t.Fatalf("unchanged = %+v", got.Unchanged)
	}
}

func TestCompareStatesRejectsCanonicalKeyCollision(t *testing.T) {
	entries := map[string]statecompare.ComparableStateEntry{
		"AA": {Index: "AA"},
		"aa": {Index: "aa"},
	}
	if _, err := statecompare.CompareStates(entries, nil); err == nil {
		t.Fatal("canonical key collision was accepted")
	}
}

func TestChangedStateKeysUndecodable(t *testing.T) {
	if got := statecompare.ChangedStateKeys(nil, map[string]any{"A": 1}); got != nil {
		t.Fatalf("ChangedStateKeys(nil, value) = %v, want nil", got)
	}
}

func TestWriteStateArtifactsStreamValidJSON(t *testing.T) {
	pre := shamap.New(shamap.TypeState)
	post := shamap.New(shamap.TypeState)
	keys := [][32]byte{{1}, {2}, {3}}
	if err := pre.Put(keys[0], []byte("aaaaaaaaaaaa")); err != nil {
		t.Fatal(err)
	}
	if err := pre.Put(keys[1], []byte("bbbbbbbbbbbb")); err != nil {
		t.Fatal(err)
	}
	if err := post.Put(keys[1], []byte("cccccccccccc")); err != nil {
		t.Fatal(err)
	}
	if err := post.Put(keys[2], []byte("dddddddddddd")); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	count, err := writeStateArtifact(context.Background(), statePath, post)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("state count = %d, want 2", count)
	}
	var stateEntries []map[string]any
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &stateEntries); err != nil {
		t.Fatalf("invalid state JSON: %v", err)
	}
	if len(stateEntries) != 2 {
		t.Fatalf("state entries = %d, want 2", len(stateEntries))
	}

	diffPath := filepath.Join(dir, "diff.json")
	counts, err := writeStateDiffArtifact(context.Background(), diffPath, pre, post)
	if err != nil {
		t.Fatal(err)
	}
	if counts != (stateDiffCounts{Added: 1, Modified: 1, Removed: 1}) {
		t.Fatalf("diff counts = %+v", counts)
	}
	var diff map[string][]json.RawMessage
	data, err = os.ReadFile(diffPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &diff); err != nil {
		t.Fatalf("invalid diff JSON: %v", err)
	}
	for _, name := range []string{"added", "modified", "removed"} {
		if len(diff[name]) != 1 {
			t.Fatalf("%s entries = %d, want 1", name, len(diff[name]))
		}
	}
}

func TestWriteAllRejectsShortWriter(t *testing.T) {
	if err := writeAll(shortWriter{}, []byte("artifact")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeAll error = %v, want %v", err, io.ErrShortWrite)
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}
