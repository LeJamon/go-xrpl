package peermanagement

import "testing"

func TestReservationTablePersistence(t *testing.T) {
	dir := t.TempDir()
	tbl := NewReservationTable(dir)

	if prev := tbl.Insert(&PeerReservation{NodeID: "nABC", Description: "first"}); prev != nil {
		t.Fatalf("first insert should have no previous, got %+v", prev)
	}
	if prev := tbl.Insert(&PeerReservation{NodeID: "nABC", Description: "second"}); prev == nil || prev.Description != "first" {
		t.Fatalf("replace should return previous 'first', got %+v", prev)
	}
	if !tbl.Contains("nABC") {
		t.Fatal("Contains should be true after insert")
	}

	// A fresh table loads the persisted entry from disk.
	reloaded := NewReservationTable(dir)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	list := reloaded.List()
	if len(list) != 1 || list[0].NodeID != "nABC" || list[0].Description != "second" {
		t.Fatalf("reloaded list mismatch: %+v", list)
	}

	// Erase persists too.
	if prev := reloaded.Erase("nABC"); prev == nil || prev.Description != "second" {
		t.Fatalf("erase should return previous 'second', got %+v", prev)
	}
	final := NewReservationTable(dir)
	if err := final.Load(); err != nil {
		t.Fatalf("Load after erase: %v", err)
	}
	if len(final.List()) != 0 {
		t.Fatalf("expected empty after erase+reload, got %+v", final.List())
	}
}

// A table with no data directory persists nothing and never errors.
func TestReservationTableInMemory(t *testing.T) {
	tbl := NewReservationTable("")
	tbl.Insert(&PeerReservation{NodeID: "nXYZ", Description: "mem"})
	if !tbl.Contains("nXYZ") {
		t.Fatal("in-memory reservation should be present")
	}
	if err := tbl.Save(); err != nil {
		t.Fatalf("Save with no dir should be a no-op, got %v", err)
	}
}
