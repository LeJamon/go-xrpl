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

// Mirrors rippled's RangeSet prevMissing test.
func TestCompleteLedgerSetPrevMissing(t *testing.T) {
	set := newCompleteLedgerSet()
	if _, ok := set.prevMissing(10, 0); ok {
		t.Fatal("empty set reported a missing ledger")
	}
	for i := uint32(0); i < 10; i++ {
		set.addRange(10*i, 10*i+5)
	}
	for i := uint32(1); i < 100; i++ {
		var want uint32
		wantOK := i > 6
		if wantOK {
			want = 10*(i/10) - 1
			if i%10 > 6 {
				want = i - 1
			}
		}
		if got, ok := set.prevMissing(i, 0); got != want || ok != wantOK {
			t.Fatalf("prevMissing(%d) = %d, %v; want %d, %v", i, got, ok, want, wantOK)
		}
	}
	if got, ok := set.prevMissing(26, 19); got != 19 || !ok {
		t.Fatalf("prevMissing(26, 19) = %d, %v; want 19, true", got, ok)
	}
	if _, ok := set.prevMissing(26, 20); ok {
		t.Fatal("prevMissing(26, 20) reported a ledger below the minimum")
	}
}

func TestCompleteLedgerSetPrevPresent(t *testing.T) {
	set := newCompleteLedgerSet()
	if _, ok := set.lowest(); ok {
		t.Fatal("empty set reported a lowest ledger")
	}
	set.addRange(3, 5)
	set.addRange(10, 15)
	for _, tc := range []struct {
		below, want uint32
		ok          bool
	}{{3, 0, false}, {4, 3, true}, {9, 5, true}, {10, 5, true}, {13, 12, true}, {40, 15, true}} {
		if got, ok := set.prevPresent(tc.below); got != tc.want || ok != tc.ok {
			t.Fatalf("prevPresent(%d) = %d, %v; want %d, %v", tc.below, got, ok, tc.want, tc.ok)
		}
	}
	if lowest, ok := set.lowest(); lowest != 3 || !ok {
		t.Fatalf("lowest() = %d, %v; want 3, true", lowest, ok)
	}
}
