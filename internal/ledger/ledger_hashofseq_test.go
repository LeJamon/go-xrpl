package ledger

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
)

// TestLedger_HashOfSeq verifies seq → hash resolution against a ledger's own
// identity, its parent, and the rolling 256-entry skip list, plus the
// out-of-range cases. Mirrors rippled's hashOfSeq.
func TestLedger_HashOfSeq(t *testing.T) {
	res, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}

	parent := FromGenesis(res.Header, res.StateMap, res.TxMap, drops.Fees{})
	hashes := map[uint32][32]byte{parent.Sequence(): parent.Hash()}

	const target = uint32(10)
	for parent.Sequence() < target {
		closeTime := parent.CloseTime().Add(10 * time.Second)
		child, err := NewOpen(parent, closeTime)
		if err != nil {
			t.Fatalf("NewOpen at seq %d: %v", parent.Sequence()+1, err)
		}
		if err := child.Close(closeTime, 0); err != nil {
			t.Fatalf("Close at seq %d: %v", child.Sequence(), err)
		}
		hashes[child.Sequence()] = child.Hash()
		parent = child
	}
	tip := parent // seq 10; rolling skip list covers ancestors 1..9

	// Own identity.
	if got, ok, err := tip.HashOfSeq(tip.Sequence()); err != nil || !ok || got != tip.Hash() {
		t.Fatalf("HashOfSeq(self): got=%x ok=%v err=%v, want %x", got, ok, err, tip.Hash())
	}

	// Parent.
	if got, ok, err := tip.HashOfSeq(tip.Sequence() - 1); err != nil || !ok || got != hashes[9] {
		t.Fatalf("HashOfSeq(parent): got=%x ok=%v err=%v, want %x", got, ok, err, hashes[9])
	}

	// Every ancestor inside the rolling window (seq 1..9).
	for seq := uint32(1); seq <= 9; seq++ {
		got, ok, err := tip.HashOfSeq(seq)
		if err != nil || !ok {
			t.Fatalf("HashOfSeq(%d): ok=%v err=%v, want resolvable", seq, ok, err)
		}
		if got != hashes[seq] {
			t.Fatalf("HashOfSeq(%d): got=%x, want %x", seq, got, hashes[seq])
		}
	}

	// Out of range.
	if _, ok, _ := tip.HashOfSeq(tip.Sequence() + 1); ok {
		t.Fatalf("HashOfSeq(future) must be unresolvable")
	}
	if _, ok, _ := tip.HashOfSeq(0); ok {
		t.Fatalf("HashOfSeq(0) must be unresolvable")
	}
}
