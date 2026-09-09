package lmath

import "testing"

func TestIsPaymentLateBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		now, due    uint32
		legacy, fix bool
	}{
		{name: "at epoch", now: 0, due: 0, legacy: true, fix: false},
		{name: "after epoch", now: 1, due: 0, legacy: true, fix: true},
		{name: "before due", now: 99, due: 100, legacy: false, fix: false},
		{name: "at due", now: 100, due: 100, legacy: true, fix: false},
		{name: "after due", now: 101, due: 100, legacy: true, fix: true},
		{name: "before maximum", now: ^uint32(0) - 1, due: ^uint32(0), legacy: false, fix: false},
		{name: "at maximum", now: ^uint32(0), due: ^uint32(0), legacy: true, fix: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPaymentLate(tc.now, tc.due, false); got != tc.legacy {
				t.Errorf("legacy lateness = %t, want %t", got, tc.legacy)
			}
			if got := IsPaymentLate(tc.now, tc.due, true); got != tc.fix {
				t.Errorf("cleanup lateness = %t, want %t", got, tc.fix)
			}
		})
	}
}
