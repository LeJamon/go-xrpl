package ledgertrie

import (
	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// TestLedger is a deterministic in-memory ledger for unit tests.
type TestLedger struct {
	id        consensus.LedgerID
	seq       uint32
	ancestors []consensus.LedgerID
}

func (l *TestLedger) ID() consensus.LedgerID { return l.id }
func (l *TestLedger) Seq() uint32            { return l.seq }
func (l *TestLedger) MinSeq() uint32         { return 0 }

func (l *TestLedger) Ancestor(s uint32) consensus.LedgerID {
	if s > l.seq {
		return consensus.LedgerID{}
	}
	return l.ancestors[s]
}

// TestLedgerBuilder constructs TestLedgers from path strings: Build("abc")
// is genesis → 'a' → 'b' → 'c'; "ab" and "abc" share the "ab" prefix.
// Each path rune is written into byte `depth` of the LedgerID so
// lexicographic byte-order on IDs agrees with rune-order on paths.
// Limited to ASCII paths shorter than 32 bytes.
type TestLedgerBuilder struct {
	genesis  *TestLedger
	children map[childKey]*TestLedger
}

type childKey struct {
	parent consensus.LedgerID
	r      rune
}

func NewTestLedgerBuilder() *TestLedgerBuilder {
	genesis := &TestLedger{
		seq:       0,
		ancestors: []consensus.LedgerID{{}},
	}
	return &TestLedgerBuilder{
		genesis:  genesis,
		children: make(map[childKey]*TestLedger),
	}
}

func (b *TestLedgerBuilder) Genesis() *TestLedger { return b.genesis }

// Build returns the memoized ledger for s; empty s returns genesis.
func (b *TestLedgerBuilder) Build(s string) *TestLedger {
	if len(s) >= 32 {
		panic("TestLedgerBuilder: path too long for 32-byte ID encoding")
	}
	curr := b.genesis
	for i, r := range s {
		key := childKey{parent: curr.id, r: r}
		child, ok := b.children[key]
		if !ok {
			child = b.extend(curr, r, i)
			b.children[key] = child
		}
		curr = child
	}
	return curr
}

func (b *TestLedgerBuilder) extend(parent *TestLedger, r rune, depth int) *TestLedger {
	var id consensus.LedgerID
	copy(id[:], parent.id[:])
	id[depth] = byte(r)

	ancestors := make([]consensus.LedgerID, parent.seq+2)
	copy(ancestors, parent.ancestors)
	ancestors[parent.seq+1] = id
	return &TestLedger{id: id, seq: parent.seq + 1, ancestors: ancestors}
}
