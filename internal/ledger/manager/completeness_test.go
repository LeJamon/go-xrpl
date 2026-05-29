package manager

import (
	"reflect"
	"testing"
)

func TestLedgerRangeContains(t *testing.T) {
	r := LedgerRange{Start: 5, End: 10}
	tests := []struct {
		seq  uint32
		want bool
	}{
		{4, false},  // below
		{5, true},   // at start boundary
		{7, true},   // inside
		{10, true},  // at end boundary
		{11, false}, // above
		{0, false},
	}
	for _, tt := range tests {
		if got := r.Contains(tt.seq); got != tt.want {
			t.Errorf("LedgerRange{5,10}.Contains(%d) = %v, want %v", tt.seq, got, tt.want)
		}
	}
}

func TestLedgerRangeLength(t *testing.T) {
	tests := []struct {
		r    LedgerRange
		want uint32
	}{
		{LedgerRange{Start: 5, End: 5}, 1},
		{LedgerRange{Start: 5, End: 10}, 6},
		{LedgerRange{Start: 1, End: 100}, 100},
	}
	for _, tt := range tests {
		if got := tt.r.Length(); got != tt.want {
			t.Errorf("%v.Length() = %d, want %d", tt.r, got, tt.want)
		}
	}
}

func TestLedgerRangeString(t *testing.T) {
	tests := []struct {
		r    LedgerRange
		want string
	}{
		{LedgerRange{Start: 5, End: 5}, "5"},
		{LedgerRange{Start: 5, End: 10}, "5-10"},
	}
	for _, tt := range tests {
		if got := tt.r.String(); got != tt.want {
			t.Errorf("%v.String() = %q, want %q", tt.r, got, tt.want)
		}
	}
}

// TestAddRangeMerging exercises the range merge engine exhaustively. After each
// scenario it asserts both the canonical String() (sorted, comma-joined) and the
// total Count().
func TestAddRangeMerging(t *testing.T) {
	type op struct{ start, end uint32 }
	tests := []struct {
		name      string
		ops       []op
		wantStr   string
		wantCount uint32
	}{
		{
			name:      "single range",
			ops:       []op{{1, 5}},
			wantStr:   "1-5",
			wantCount: 5,
		},
		{
			name:      "disjoint ranges stay separate",
			ops:       []op{{1, 5}, {10, 15}},
			wantStr:   "1-5,10-15",
			wantCount: 11,
		},
		{
			name:      "overlapping ranges merge",
			ops:       []op{{1, 5}, {3, 8}},
			wantStr:   "1-8",
			wantCount: 8,
		},
		{
			name:      "adjacent ranges merge (end+1 == next start)",
			ops:       []op{{1, 5}, {6, 10}},
			wantStr:   "1-10",
			wantCount: 10,
		},
		{
			name:      "adjacent in reverse order merge",
			ops:       []op{{6, 10}, {1, 5}},
			wantStr:   "1-10",
			wantCount: 10,
		},
		{
			name:      "out of order produces sorted minimal set",
			ops:       []op{{20, 25}, {1, 5}, {10, 15}},
			wantStr:   "1-5,10-15,20-25",
			wantCount: 17,
		},
		{
			name:      "new range bridges and swallows multiple ranges",
			ops:       []op{{1, 5}, {10, 15}, {20, 25}, {3, 22}},
			wantStr:   "1-25",
			wantCount: 25,
		},
		{
			name:      "new range bridges via adjacency between two ranges",
			ops:       []op{{1, 5}, {10, 15}, {6, 9}},
			wantStr:   "1-15",
			wantCount: 15,
		},
		{
			name:      "fully contained range is a no-op",
			ops:       []op{{1, 20}, {5, 10}},
			wantStr:   "1-20",
			wantCount: 20,
		},
		{
			name:      "duplicate range is a no-op",
			ops:       []op{{1, 5}, {1, 5}},
			wantStr:   "1-5",
			wantCount: 5,
		},
		{
			name:      "start > end is ignored",
			ops:       []op{{5, 1}},
			wantStr:   "empty",
			wantCount: 0,
		},
		{
			name:      "start > end ignored among valid ranges",
			ops:       []op{{1, 5}, {10, 8}, {10, 15}},
			wantStr:   "1-5,10-15",
			wantCount: 11,
		},
		{
			name:      "extend an existing range on the left",
			ops:       []op{{5, 10}, {1, 7}},
			wantStr:   "1-10",
			wantCount: 10,
		},
		{
			name:      "extend an existing range on the right",
			ops:       []op{{1, 7}, {5, 10}},
			wantStr:   "1-10",
			wantCount: 10,
		},
		{
			name:      "single ledger adds via Add path",
			ops:       []op{{3, 3}, {4, 4}, {5, 5}},
			wantStr:   "3-5",
			wantCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCompleteLedgerSet()
			for _, o := range tt.ops {
				c.AddRange(o.start, o.end)
			}
			if got := c.String(); got != tt.wantStr {
				t.Errorf("String() = %q, want %q", got, tt.wantStr)
			}
			if got := c.Count(); got != tt.wantCount {
				t.Errorf("Count() = %d, want %d", got, tt.wantCount)
			}
		})
	}
}

func TestAddSingle(t *testing.T) {
	c := NewCompleteLedgerSet()
	c.Add(7)
	if !c.Contains(7) {
		t.Errorf("Contains(7) = false after Add(7)")
	}
	if got := c.String(); got != "7" {
		t.Errorf("String() = %q, want %q", got, "7")
	}
	if got := c.Count(); got != 1 {
		t.Errorf("Count() = %d, want 1", got)
	}
}

func TestContains(t *testing.T) {
	c := NewCompleteLedgerSet()
	c.AddRange(1, 5)
	c.AddRange(10, 15)
	c.AddRange(20, 20)

	tests := []struct {
		seq  uint32
		want bool
	}{
		{1, true}, {5, true}, // boundaries of first range
		{6, false}, {9, false}, // gap
		{10, true}, {12, true}, {15, true}, // second range
		{16, false}, {19, false}, // gap
		{20, true},  // single-ledger range
		{21, false}, // just outside last range
		{0, false},
	}
	for _, tt := range tests {
		if got := c.Contains(tt.seq); got != tt.want {
			t.Errorf("Contains(%d) = %v, want %v", tt.seq, got, tt.want)
		}
	}
}

func TestRange(t *testing.T) {
	t.Run("empty set", func(t *testing.T) {
		c := NewCompleteLedgerSet()
		min, max, hasAny := c.Range()
		if hasAny || min != 0 || max != 0 {
			t.Errorf("Range() = (%d, %d, %v), want (0, 0, false)", min, max, hasAny)
		}
	})

	t.Run("populated set", func(t *testing.T) {
		c := NewCompleteLedgerSet()
		c.AddRange(10, 15)
		c.AddRange(1, 5)
		c.AddRange(20, 25)
		min, max, hasAny := c.Range()
		if !hasAny || min != 1 || max != 25 {
			t.Errorf("Range() = (%d, %d, %v), want (1, 25, true)", min, max, hasAny)
		}
	})
}

func TestFindMissing(t *testing.T) {
	tests := []struct {
		name       string
		ranges     [][2]uint32
		start, end uint32
		want       []uint32
	}{
		{
			name:   "empty set returns whole range",
			ranges: nil,
			start:  1, end: 5,
			want: []uint32{1, 2, 3, 4, 5},
		},
		{
			name:   "fully covered returns nil",
			ranges: [][2]uint32{{1, 10}},
			start:  3, end: 7,
			want: nil,
		},
		{
			name:   "gap between ranges",
			ranges: [][2]uint32{{1, 3}, {7, 10}},
			start:  1, end: 10,
			want: []uint32{4, 5, 6},
		},
		{
			name:   "partial overlap at both ends",
			ranges: [][2]uint32{{3, 7}},
			start:  1, end: 10,
			want: []uint32{1, 2, 8, 9, 10},
		},
		{
			name:   "missing before first range only",
			ranges: [][2]uint32{{5, 10}},
			start:  1, end: 10,
			want: []uint32{1, 2, 3, 4},
		},
		{
			name:   "missing after last range only",
			ranges: [][2]uint32{{1, 5}},
			start:  1, end: 10,
			want: []uint32{6, 7, 8, 9, 10},
		},
		{
			name:   "multiple gaps",
			ranges: [][2]uint32{{2, 3}, {6, 7}},
			start:  1, end: 9,
			want: []uint32{1, 4, 5, 8, 9},
		},
		{
			name:   "start > end returns nil",
			ranges: nil,
			start:  10, end: 1,
			want: nil,
		},
		{
			name:   "single missing sequence",
			ranges: [][2]uint32{{1, 4}, {6, 10}},
			start:  1, end: 10,
			want: []uint32{5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCompleteLedgerSet()
			for _, r := range tt.ranges {
				c.AddRange(r[0], r[1])
			}
			got := c.FindMissing(tt.start, tt.end)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FindMissing(%d, %d) = %v, want %v", tt.start, tt.end, got, tt.want)
			}
		})
	}
}

func TestFindNextMissing(t *testing.T) {
	t.Run("returns first gap after the given sequence", func(t *testing.T) {
		c := NewCompleteLedgerSet()
		c.AddRange(1, 5)
		c.AddRange(7, 10)
		next, found := c.FindNextMissing(1)
		if !found || next != 6 {
			t.Errorf("FindNextMissing(1) = (%d, %v), want (6, true)", next, found)
		}
	})

	t.Run("skips over a complete tail", func(t *testing.T) {
		c := NewCompleteLedgerSet()
		c.AddRange(1, 10)
		// next missing after 5 is 11 (first uncovered after the range)
		next, found := c.FindNextMissing(5)
		if !found || next != 11 {
			t.Errorf("FindNextMissing(5) = (%d, %v), want (11, true)", next, found)
		}
	})

	t.Run("false when look-ahead window fully complete", func(t *testing.T) {
		c := NewCompleteLedgerSet()
		// FindNextMissing looks ahead after+1 .. after+1000.
		c.AddRange(1, 2000)
		next, found := c.FindNextMissing(100)
		if found {
			t.Errorf("FindNextMissing(100) = (%d, true), want (_, false)", next)
		}
	})
}

func TestClear(t *testing.T) {
	c := NewCompleteLedgerSet()
	c.AddRange(1, 10)
	c.Clear()

	if got := c.String(); got != "empty" {
		t.Errorf("String() after Clear = %q, want %q", got, "empty")
	}
	if got := c.Count(); got != 0 {
		t.Errorf("Count() after Clear = %d, want 0", got)
	}
	if _, _, hasAny := c.Range(); hasAny {
		t.Errorf("Range() after Clear reports hasAny=true, want false")
	}

	// Reusable after Clear.
	c.AddRange(5, 8)
	if got := c.String(); got != "5-8" {
		t.Errorf("String() after reuse = %q, want %q", got, "5-8")
	}
	if got := c.Count(); got != 4 {
		t.Errorf("Count() after reuse = %d, want 4", got)
	}
}

func TestStringEmpty(t *testing.T) {
	c := NewCompleteLedgerSet()
	if got := c.String(); got != "empty" {
		t.Errorf("String() on empty set = %q, want %q", got, "empty")
	}
}
