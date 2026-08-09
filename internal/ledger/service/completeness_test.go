package service

import "testing"

func TestCompleteLedgerSet(t *testing.T) {
	set := newCompleteLedgerSet()
	set.addRange(5, 10)
	set.addRange(1, 3)
	set.add(4)
	set.addRange(12, 15)
	set.add(11)

	if got := set.String(); got != "1-15" {
		t.Fatalf("String() = %q, want %q", got, "1-15")
	}
	for seq := uint32(1); seq <= 15; seq++ {
		if !set.contains(seq) {
			t.Fatalf("Contains(%d) = false, want true", seq)
		}
	}

	set.removeRange(4, 12)
	if got := set.String(); got != "1-3,13-15" {
		t.Fatalf("String() after RemoveRange = %q, want %q", got, "1-3,13-15")
	}
	set.remove(1)
	if got := set.String(); got != "2-3,13-15" {
		t.Fatalf("String() after Remove = %q, want %q", got, "2-3,13-15")
	}
}

func TestCompleteLedgerSetIgnoresInvalidRanges(t *testing.T) {
	set := newCompleteLedgerSet()
	set.addRange(9, 3)
	set.removeRange(9, 3)

	if got := set.String(); got != "empty" {
		t.Fatalf("String() = %q, want empty", got)
	}
}
