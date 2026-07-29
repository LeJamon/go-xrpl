package ledgertrietest

import "github.com/LeJamon/go-xrpl/internal/consensus"

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

type TestLedgerBuilder struct {
	genesis  *TestLedger
	children map[childKey]*TestLedger
}

type childKey struct {
	parent consensus.LedgerID
	edge   byte
}

func NewTestLedgerBuilder() *TestLedgerBuilder {
	genesis := &TestLedger{
		ancestors: []consensus.LedgerID{{}},
	}
	return &TestLedgerBuilder{
		genesis:  genesis,
		children: make(map[childKey]*TestLedger),
	}
}

func (b *TestLedgerBuilder) Genesis() *TestLedger { return b.genesis }

func (b *TestLedgerBuilder) Build(path string) *TestLedger {
	if len(path) >= len(consensus.LedgerID{}) {
		panic("TestLedgerBuilder: path too long for 32-byte ID encoding")
	}
	for i := range len(path) {
		if path[i] == 0 || path[i] > 0x7f {
			panic("TestLedgerBuilder: path must contain non-NUL ASCII bytes")
		}
	}

	curr := b.genesis
	for depth := range len(path) {
		edge := path[depth]
		key := childKey{parent: curr.id, edge: edge}
		child, ok := b.children[key]
		if !ok {
			child = extend(curr, edge, depth)
			b.children[key] = child
		}
		curr = child
	}
	return curr
}

func extend(parent *TestLedger, edge byte, depth int) *TestLedger {
	id := parent.id
	id[depth] = edge

	ancestors := make([]consensus.LedgerID, parent.seq+2)
	copy(ancestors, parent.ancestors)
	ancestors[parent.seq+1] = id
	return &TestLedger{id: id, seq: parent.seq + 1, ancestors: ancestors}
}
