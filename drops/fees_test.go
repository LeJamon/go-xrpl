package drops

import "testing"

func TestDefaultFees(t *testing.T) {
	want := Fees{Base: 10, Reserve: 10_000_000, Increment: 2_000_000}
	if got := DefaultFees(); got != want {
		t.Fatalf("DefaultFees() = %+v, want %+v", got, want)
	}
}
