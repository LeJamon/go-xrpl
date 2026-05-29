package tx

import (
	"math"
	"testing"
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
