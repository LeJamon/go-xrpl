package ledgertrie

import (
	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// TestLedger is a deterministic in-memory ledger useful for unit
// tests of the trie and any future consensus components that need
// ancestry. It stores its full ancestor chain so Ancestor(s) is O(1).
//
// Modeled on rippled's csf::Ledger (src/test/csf/ledgers.h) which
// serves the same purpose for the C++ test harness.
type TestLedger struct {
	id        consensus.LedgerID
	seq       uint32
	ancestors []consensus.LedgerID // ancestors[i] is the ID at seq i; ancestors[seq] == id
}

// ID implements Ledger.
func (l *TestLedger) ID() consensus.LedgerID { return l.id }

// Seq implements Ledger.
func (l *TestLedger) Seq() uint32 { return l.seq }

// MinSeq implements Ledger. TestLedger retains its full ancestry, so
// it can answer Ancestor(s) for any s in [0, Seq()].
func (l *TestLedger) MinSeq() uint32 { return 0 }

// Ancestor implements Ledger. For s outside [MinSeq(), Seq()] returns
// the zero LedgerID, mirroring rippled's RCLValidatedLedger::operator[]
// out-of-range fallback (RCLValidations.cpp:79-95).
func (l *TestLedger) Ancestor(s uint32) consensus.LedgerID {
	if s > l.seq {
		return consensus.LedgerID{}
	}
	return l.ancestors[s]
}

// TestLedgerBuilder constructs TestLedger instances from short string
// notation. Each call to Build("abc") returns a ledger with ancestors
// genesis → 'a' → 'b' → 'c' (seq 3). Repeated characters in the same
// position are supported — "ab" and "abc" share "ab" prefix. Mirrors
// rippled's LedgerHistoryHelper (LedgerTrie_test.cpp:~96) which does
// the same via `h["abc"]`.
//
// IDs are derived by zero-padding the path bytes themselves into the
// 32-byte LedgerID, so lexicographic byte-order on IDs matches
// lexicographic rune-order on paths. That preserves rippled tests'
// implicit assumption that `h["abce"].id() > h["abcd"].id()`. Only
// ASCII paths shorter than 32 bytes are supported — plenty for the
// tests we port.
type TestLedgerBuilder struct {
	genesis *TestLedger
	// children maps (parent.id, next-rune) -> child
	children map[childKey]*TestLedger
}

type childKey struct {
	parent consensus.LedgerID
	r      rune
}

// NewTestLedgerBuilder returns a builder whose genesis ledger is the
// all-zero-ID seq-0 ledger.
func NewTestLedgerBuilder() *TestLedgerBuilder {
	genesis := &TestLedger{
		seq:       0,
		ancestors: []consensus.LedgerID{{}}, // genesis ID is 32 zero bytes
	}
	return &TestLedgerBuilder{
		genesis:  genesis,
		children: make(map[childKey]*TestLedger),
	}
}

// Genesis returns the builder's genesis ledger (seq 0, zero ID).
func (b *TestLedgerBuilder) Genesis() *TestLedger { return b.genesis }

// Build returns the ledger identified by the given string. Each rune
// selects the child of the previous ledger; identical strings always
// return the same *TestLedger (memoized). Empty string returns genesis.
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

// extend produces a new ledger one step past `parent`. ID is the
// parent ID with byte `depth` overwritten to r. Since genesis ID is
// all zeros, the resulting IDs spell out the path in the high bytes,
// making lexicographic comparison on IDs agree with lexicographic
// comparison on paths.
func (b *TestLedgerBuilder) extend(parent *TestLedger, r rune, depth int) *TestLedger {
	var id consensus.LedgerID
	copy(id[:], parent.id[:])
	id[depth] = byte(r)

	ancestors := make([]consensus.LedgerID, parent.seq+2)
	copy(ancestors, parent.ancestors)
	ancestors[parent.seq+1] = id
	return &TestLedger{id: id, seq: parent.seq + 1, ancestors: ancestors}
}
