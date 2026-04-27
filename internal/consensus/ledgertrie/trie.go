// Package ledgertrie ports rippled's LedgerTrie<Ledger>
// (src/xrpld/consensus/LedgerTrie.h): branchSupport-based preferred-
// ledger selection over a compressed ancestry trie.
package ledgertrie

import (
	"bytes"
	"slices"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// Ledger is the interface the trie needs. Unique-history invariant:
// a[s] == b[s] implies a[p] == b[p] for all p < s in the overlap.
// Ancestor returns the zero LedgerID for s outside [MinSeq, Seq].
type Ledger interface {
	ID() consensus.LedgerID
	Seq() uint32
	MinSeq() uint32
	Ancestor(s uint32) consensus.LedgerID
}

// Mismatch returns the first sequence at which a and b diverge.
// Returns 1 when the overlap doesn't exist or mismatches at its floor
// (rippled's "assume post-genesis divergence" fallback,
// RCLValidations.cpp:99-114).
func Mismatch(a, b Ledger) uint32 {
	upper := a.Seq()
	if b.Seq() < upper {
		upper = b.Seq()
	}
	lower := a.MinSeq()
	if bm := b.MinSeq(); bm > lower {
		lower = bm
	}
	if lower > upper {
		return 1
	}

	// Unique-history makes the predicate monotone; binary search.
	low := lower
	hi := upper + 1
	for low < hi {
		mid := low + (hi-low)/2
		if a.Ancestor(mid) == b.Ancestor(mid) {
			low = mid + 1
		} else {
			hi = mid
		}
	}
	if low == lower {
		return 1
	}
	return low
}

// SpanTip is the read-only view of a span's tip.
type SpanTip struct {
	Seq    uint32
	ID     consensus.LedgerID
	ledger Ledger
}

// Ancestor returns the ID at sequence s; s must be <= Seq.
func (t SpanTip) Ancestor(s uint32) consensus.LedgerID { return t.ledger.Ancestor(s) }

// Trie is the ancestry trie. The zero value is not usable — call New.
type Trie struct {
	root    *node
	genesis Ledger

	// seqKeys is the sorted-key view over seqSupport (std::map analogue).
	seqSupport map[uint32]uint32
	seqKeys    []uint32
}

// New constructs an empty trie. genesis must satisfy Seq() == 0.
func New(genesis Ledger) *Trie {
	return &Trie{
		root:       newEmptyNode(genesis),
		genesis:    genesis,
		seqSupport: make(map[uint32]uint32),
	}
}

// Empty reports whether the trie holds any support.
func (t *Trie) Empty() bool { return t.root == nil || t.root.branchSupport == 0 }

// find returns the node sharing the longest common prefix with l and
// the sequence at which they diverge.
func (t *Trie) find(l Ledger) (*node, uint32) {
	curr := t.root
	pos := curr.s.diff(l)

	done := false
	for !done && pos == curr.s.end {
		done = true
		for _, child := range curr.children {
			childPos := child.s.diff(l)
			if childPos > pos {
				done = false
				pos = childPos
				curr = child
				break
			}
		}
	}
	return curr, pos
}

// findByLedgerID is an O(n) walk for an exact ID match.
func (t *Trie) findByLedgerID(l Ledger) *node {
	return findByIDWalk(t.root, l.ID())
}

func findByIDWalk(curr *node, id consensus.LedgerID) *node {
	if curr == nil {
		return nil
	}
	if curr.s.tip().ID == id {
		return curr
	}
	for _, child := range curr.children {
		if hit := findByIDWalk(child, id); hit != nil {
			return hit
		}
	}
	return nil
}

func (t *Trie) seqSupportAdd(seq uint32, delta uint32) {
	if _, ok := t.seqSupport[seq]; !ok {
		idx, _ := slices.BinarySearch(t.seqKeys, seq)
		t.seqKeys = slices.Insert(t.seqKeys, idx, seq)
	}
	t.seqSupport[seq] += delta
}

// seqSupportSub panics on under-subtract (XRPL_ASSERT, LedgerTrie.h:553).
func (t *Trie) seqSupportSub(seq uint32, delta uint32) {
	cur, ok := t.seqSupport[seq]
	if !ok || cur < delta {
		panic("ledgertrie: seqSupport invariant violation")
	}
	cur -= delta
	if cur == 0 {
		delete(t.seqSupport, seq)
		if idx, found := slices.BinarySearch(t.seqKeys, seq); found {
			t.seqKeys = slices.Delete(t.seqKeys, idx, idx+1)
		}
		return
	}
	t.seqSupport[seq] = cur
}

// Insert adds count support for l along its ancestry. count must be > 0
// — 0 corrupts seqSupport and resets existing tipSupport on a split.
func (t *Trie) Insert(l Ledger, count uint32) {
	loc, diffSeq := t.find(l)

	incNode := loc

	prefix, hasPrefix := loc.s.before(diffSeq)
	oldSuffix, hasOldSuffix := loc.s.from(diffSeq)
	newSuffix, hasNewSuffix := newSpanFromLedger(l).from(diffSeq)

	if hasOldSuffix {
		if !hasPrefix {
			panic("ledgertrie: Insert: prefix missing despite oldSuffix")
		}
		sfx := newNodeFromSpan(oldSuffix)
		sfx.tipSupport = loc.tipSupport
		sfx.branchSupport = loc.branchSupport
		sfx.children = loc.children
		loc.children = nil
		for _, c := range sfx.children {
			c.parent = sfx
		}

		loc.s = prefix
		sfx.parent = loc
		loc.children = append(loc.children, sfx)
		loc.tipSupport = 0
	}

	if hasNewSuffix {
		nn := newNodeFromSpan(newSuffix)
		nn.parent = loc
		incNode = nn
		loc.children = append(loc.children, nn)
	}

	incNode.tipSupport += count
	for cur := incNode; cur != nil; cur = cur.parent {
		cur.branchSupport += count
	}

	t.seqSupportAdd(l.Seq(), count)
}

// Remove decreases l's tip support by up to count, compacting the trie
// when tipSupport reaches zero. Returns true if l was in the trie.
func (t *Trie) Remove(l Ledger, count uint32) bool {
	loc := t.findByLedgerID(l)
	if loc == nil || loc.tipSupport == 0 {
		return false
	}
	if count > loc.tipSupport {
		count = loc.tipSupport
	}

	loc.tipSupport -= count
	t.seqSupportSub(l.Seq(), count)

	for cur := loc; cur != nil; cur = cur.parent {
		cur.branchSupport -= count
	}

	for loc.tipSupport == 0 && loc != t.root {
		parent := loc.parent
		switch len(loc.children) {
		case 0:
			parent.eraseChild(loc)
		case 1:
			child := loc.children[0]
			child.s = mergeSpans(loc.s, child.s)
			child.parent = parent
			parent.children = append(parent.children, child)
			parent.eraseChild(loc)
		default:
			// 0-tip node with >1 children is valid; can't compact.
			return true
		}
		loc = parent
	}
	return true
}

// TipSupport returns the exact tip support for l, or 0 if not present.
func (t *Trie) TipSupport(l Ledger) uint32 {
	if loc := t.findByLedgerID(l); loc != nil {
		return loc.tipSupport
	}
	return 0
}

// BranchSupport returns tipSupport(l) plus the branchSupport of all
// descendants. When l is a proper prefix of a trie span, returns the
// enclosing node's branchSupport.
func (t *Trie) BranchSupport(l Ledger) uint32 {
	loc := t.findByLedgerID(l)
	if loc == nil {
		candidate, diffSeq := t.find(l)
		if diffSeq > l.Seq() && l.Seq() < candidate.s.end {
			loc = candidate
		}
	}
	if loc == nil {
		return 0
	}
	return loc.branchSupport
}

// GetPreferred returns the preferred ledger's tip, or false when the
// trie is empty. largestIssued seeds uncommitted support from earlier
// sequences so ancient validations cannot retroactively swing preference.
func (t *Trie) GetPreferred(largestIssued uint32) (SpanTip, bool) {
	if t.Empty() {
		return SpanTip{}, false
	}

	curr := t.root
	uncommitted := uint32(0)
	uncommittedIdx := 0

	for curr != nil {
		// Absorb uncommitted support for seqs < max(curr.start+1, largestIssued).
		nextSeq := curr.s.start + 1
		floor := nextSeq
		if largestIssued > floor {
			floor = largestIssued
		}
		for uncommittedIdx < len(t.seqKeys) && t.seqKeys[uncommittedIdx] < floor {
			uncommitted += t.seqSupport[t.seqKeys[uncommittedIdx]]
			uncommittedIdx++
		}

		for nextSeq < curr.s.end && curr.branchSupport > uncommitted {
			if uncommittedIdx < len(t.seqKeys) && t.seqKeys[uncommittedIdx] < curr.s.end {
				nextSeq = t.seqKeys[uncommittedIdx] + 1
				uncommitted += t.seqSupport[t.seqKeys[uncommittedIdx]]
				uncommittedIdx++
			} else {
				nextSeq = curr.s.end
			}
		}

		if nextSeq < curr.s.end {
			sub, ok := curr.s.before(nextSeq)
			if !ok {
				// nextSeq > curr.s.start by construction; this is unreachable.
				panic("ledgertrie: GetPreferred: before(nextSeq) yielded empty span")
			}
			return sub.tip(), true
		}

		var best *node
		var margin uint32
		switch len(curr.children) {
		case 0:
			best = nil
		case 1:
			best = curr.children[0]
			margin = best.branchSupport
		default:
			// Inline top-2 by (branchSupport, startID) desc.
			var second *node
			for _, c := range curr.children {
				if best == nil || nodeOutranks(c, best) {
					second, best = best, c
				} else if second == nil || nodeOutranks(c, second) {
					second = c
				}
			}
			margin = best.branchSupport - second.branchSupport
			if ledgerIDGreater(best.s.startID(), second.s.startID()) {
				margin++
			}
		}

		if best != nil && (margin > uncommitted || uncommitted == 0) {
			curr = best
			continue
		}
		break
	}
	return curr.s.tip(), true
}

// ledgerIDGreater matches rippled's base_uint::operator> (big-endian memcmp).
func ledgerIDGreater(a, b consensus.LedgerID) bool {
	return bytes.Compare(a[:], b[:]) > 0
}

// nodeOutranks orders nodes by (branchSupport, startID) descending.
func nodeOutranks(a, b *node) bool {
	if a.branchSupport != b.branchSupport {
		return a.branchSupport > b.branchSupport
	}
	return ledgerIDGreater(a.s.startID(), b.s.startID())
}

// CheckInvariants verifies: non-root 0-tip nodes have ≥2 children,
// branchSupport == tipSupport + Σ child.branchSupport, parent pointers
// are consistent, and seqSupport matches the sum of tip supports.
func (t *Trie) CheckInvariants() bool {
	expected := make(map[uint32]uint32)
	stack := []*node{t.root}
	for len(stack) > 0 {
		curr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if curr == nil {
			continue
		}
		if curr != t.root && curr.tipSupport == 0 && len(curr.children) < 2 {
			return false
		}
		support := curr.tipSupport
		if curr.tipSupport != 0 {
			expected[curr.s.end-1] += curr.tipSupport
		}
		for _, c := range curr.children {
			if c.parent != curr {
				return false
			}
			support += c.branchSupport
			stack = append(stack, c)
		}
		if support != curr.branchSupport {
			return false
		}
	}
	if len(expected) != len(t.seqSupport) {
		return false
	}
	for k, v := range expected {
		if t.seqSupport[k] != v {
			return false
		}
	}
	return true
}
