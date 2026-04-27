package ledgertrie

import "github.com/LeJamon/goXRPLd/internal/consensus"

// span is the half-open interval [start, end) of a ledger's ancestry.
// Spans are always non-empty (start < end). Port of rippled's
// ledger_trie_detail::Span<Ledger> (LedgerTrie.h:77-198).
type span struct {
	start  uint32
	end    uint32
	ledger Ledger // interpreted as "the ledger we're sitting in for this span"
}

// newSpanFromLedger produces [0, ledger.Seq()+1) — the span that
// covers the ledger's entire ancestry up to and including itself.
func newSpanFromLedger(l Ledger) span {
	return span{start: 0, end: l.Seq() + 1, ledger: l}
}

// clamp forces val into [start, end].
func (s span) clamp(val uint32) uint32 {
	if val < s.start {
		return s.start
	}
	if val > s.end {
		return s.end
	}
	return val
}

// sub returns the span over [from, to), or (zero, false) if empty.
func (s span) sub(from, to uint32) (span, bool) {
	nf := s.clamp(from)
	nt := s.clamp(to)
	if nf < nt {
		return span{start: nf, end: nt, ledger: s.ledger}, true
	}
	return span{}, false
}

// from returns the sub-span [spot, end).
func (s span) from(spot uint32) (span, bool) { return s.sub(spot, s.end) }

// before returns the sub-span [start, spot).
func (s span) before(spot uint32) (span, bool) { return s.sub(s.start, spot) }

// startID is the ID of the ledger at sequence start. For a non-genesis
// span this is an ancestor of s.ledger.
func (s span) startID() consensus.LedgerID { return s.ledger.Ancestor(s.start) }

// diff returns the first sequence where s.ledger and o disagree,
// clamped into [start, end]. Port of Span::diff (LedgerTrie.h:144-148).
func (s span) diff(o Ledger) uint32 { return s.clamp(Mismatch(s.ledger, o)) }

// tip returns a read-only view of the last ledger in the span
// (seq = end-1). Port of Span::tip (LedgerTrie.h:151-156).
func (s span) tip() SpanTip {
	tipSeq := s.end - 1
	return SpanTip{Seq: tipSeq, ID: s.ledger.Ancestor(tipSeq), ledger: s.ledger}
}

// merge combines two overlapping/adjacent spans, using the ledger
// from the one with the higher end (so the combined tip resolves
// correctly). Port of free `merge` (LedgerTrie.h:189-197).
func mergeSpans(a, b span) span {
	lo := a.start
	if b.start < lo {
		lo = b.start
	}
	if a.end < b.end {
		return span{start: lo, end: b.end, ledger: b.ledger}
	}
	return span{start: lo, end: a.end, ledger: a.ledger}
}

// node is a trie node. Port of ledger_trie_detail::Node<Ledger>
// (LedgerTrie.h:201-269).
type node struct {
	s             span
	tipSupport    uint32
	branchSupport uint32
	children      []*node
	parent        *node
}

// newEmptyNode is the genesis-root node used by an empty trie.
func newEmptyNode(genesis Ledger) *node {
	return &node{s: span{start: 0, end: 1, ledger: genesis}}
}

// newNodeFromSpan wraps an existing span into a node. tipSupport and
// branchSupport remain zero; the caller sets them.
func newNodeFromSpan(s span) *node { return &node{s: s} }

// eraseChild unlinks child from n.children. Swap-with-last-and-pop
// mirrors rippled's Node::erase (LedgerTrie.h:227-239).
func (n *node) eraseChild(child *node) {
	for i, c := range n.children {
		if c == child {
			last := len(n.children) - 1
			n.children[i] = n.children[last]
			n.children[last] = nil
			n.children = n.children[:last]
			return
		}
	}
}
