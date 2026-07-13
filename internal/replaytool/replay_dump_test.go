package replaytool

import (
	"reflect"
	"testing"
)

func TestCompareStates(t *testing.T) {
	before := map[string]ComparableStateEntry{
		"aa": {Index: "AA", DataHex: "00", Decoded: map[string]any{"Balance": "1"}},
		"bb": {Index: "BB", DataHex: "11"},
		"dd": {Index: "DD", DataHex: "aBcD"},
	}
	after := map[string]ComparableStateEntry{
		"AA": {Index: "AA", DataHex: "01", Decoded: map[string]any{"Balance": "2"}},
		"cc": {Index: "CC", DataHex: "22"},
		"dd": {Index: "DD", DataHex: "AbCd"},
	}

	got := CompareStates(before, after)
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

func TestChangedStateKeysUndecodable(t *testing.T) {
	if got := ChangedStateKeys(nil, map[string]any{"A": 1}); got != nil {
		t.Fatalf("ChangedStateKeys(nil, value) = %v, want nil", got)
	}
}
