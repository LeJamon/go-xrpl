package ledger

import (
	"bytes"
	"testing"
)

func TestPrevKey(t *testing.T) {
	low := [32]byte{}
	low[31] = 0x05
	wantLow := [32]byte{}
	wantLow[31] = 0x04
	if got := prevKey(low); got != wantLow {
		t.Errorf("low-byte decrement: got %x, want %x", got, wantLow)
	}

	// 0x...0100 - 1 borrows across the low word to 0x...00FF.
	borrow := [32]byte{}
	borrow[30] = 0x01
	wantBorrow := [32]byte{}
	wantBorrow[31] = 0xFF
	if got := prevKey(borrow); got != wantBorrow {
		t.Errorf("borrow: got %x, want %x", got, wantBorrow)
	}

	// The marker must be strictly below its key so upper_bound resumes on it.
	k := [32]byte{0: 0xAB, 31: 0x10}
	if p := prevKey(k); bytes.Compare(p[:], k[:]) >= 0 {
		t.Errorf("prevKey(%x) = %x is not strictly less", k, p)
	}
}
