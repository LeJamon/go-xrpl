package tx

import (
	"math"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

func TestConfineOwnerCount(t *testing.T) {
	tests := []struct {
		name       string
		current    uint32
		adjustment int
		want       uint32
	}{
		{"increment", 5, 3, 8},
		{"decrement", 5, -3, 2},
		{"zero adjustment", 5, 0, 5},
		{"decrement to zero", 5, -5, 0},
		{"underflow clamps to zero", 2, -5, 0},
		{"underflow from zero", 0, -1, 0},
		{"increment to max", math.MaxUint32 - 1, 1, math.MaxUint32},
		{"overflow saturates to max", math.MaxUint32, 1, math.MaxUint32},
		{"large overflow saturates", math.MaxUint32 - 1, 100, math.MaxUint32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := confineOwnerCount(tt.current, tt.adjustment); got != tt.want {
				t.Errorf("confineOwnerCount(%d, %d) = %d, want %d", tt.current, tt.adjustment, got, tt.want)
			}
		})
	}
}

func TestOwnerCountsEffectiveReserve(t *testing.T) {
	tests := []struct {
		name   string
		counts OwnerCounts
		want   uint32
	}{
		{"plain", OwnerCounts{OwnerCount: 3}, 3},
		{"sponsored object", OwnerCounts{OwnerCount: 3, SponsoredOwnerCount: 1}, 2},
		{"sponsoring objects", OwnerCounts{OwnerCount: 1, SponsoringOwnerCount: 2}, 3},
		{"malformed sponsored underflow clamps", OwnerCounts{OwnerCount: 1, SponsoredOwnerCount: 2}, 0},
		{"overflow saturates", OwnerCounts{OwnerCount: math.MaxUint32, SponsoringOwnerCount: 1}, math.MaxUint32},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.counts.Count(); got != test.want {
				t.Fatalf("Count() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestAccountCountForReserve(t *testing.T) {
	if got := AccountCountForReserve(&state.AccountRoot{}); got != 1 {
		t.Fatalf("plain account count = %d, want 1", got)
	}
	if got := AccountCountForReserve(&state.AccountRoot{HasSponsor: true}); got != 0 {
		t.Fatalf("sponsored account count = %d, want 0", got)
	}
	if got := AccountCountForReserve(&state.AccountRoot{HasSponsor: true, SponsoringAccountCount: 2}); got != 2 {
		t.Fatalf("sponsored sponsor account count = %d, want 2", got)
	}
}
