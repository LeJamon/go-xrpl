// Package ledgertrie is a Go port of rippled's LedgerTrie<Ledger>
// (src/xrpld/consensus/LedgerTrie.h). The trie maintains validation
// support of recent ledgers based on their ancestry so that
// consensus can pick a preferred branch by branchSupport rather than
// by flat hash-count.
package ledgertrie

import (
	"sort"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// Ledger is the interface the trie needs. Ledgers have a unique-history
// invariant: if a[s] == b[s] then a[p] == b[p] for all p < s within
// each ledger's known-ancestor range.
//
// Production ledgers (rippled's RCLValidatedLedger / our providerLedger)
// only carry the most recent ~256 ancestors via the keylet::skip SLE.
// MinSeq() reports the earliest seq for which Ancestor() returns a
// real ID; below it Ancestor() returns the zero LedgerID to signal
// "out of range". Test ledgers that cache the full chain return 0.
type Ledger interface {
	// ID returns the ledger's own identifier (== Ancestor(Seq())).
	ID() consensus.LedgerID
	// Seq returns this ledger's sequence number.
	Seq() uint32
	// MinSeq returns the lowest seq for which Ancestor returns a
	// non-zero ID. 0 for ledgers that retain their full ancestry.
	MinSeq() uint32
	// Ancestor returns the ID of the ancestor at sequence s. For s
	// outside [MinSeq(), Seq()] the implementation returns the zero
	// LedgerID. Ancestor(0) is the genesis ID for unbounded ledgers.
	Ancestor(s uint32) consensus.LedgerID
}

// Mismatch returns the first sequence number at which a and b's
// ancestries differ, restricted to the overlap of their known ranges
// [max(a.MinSeq, b.MinSeq), min(a.Seq, b.Seq)]. When the entire
// overlap mismatches — or there is no overlap — returns 1, matching
// rippled's RCLValidations.cpp:99-114 fallback ("assume divergence
// post-genesis"). When ancestries agree throughout the overlap,
// returns min(a.Seq, b.Seq)+1.
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
		// No overlap of known ranges; assume divergence is post-genesis.
		return 1
	}

	// Binary search in [lower, upper] for the first seq where a[s]
	// disagrees with b[s]. Unique-history makes the "match" predicate
	// monotone within the overlap (matches above s implies matches at
	// every p in [lower, s]).
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
		// Mismatch at the lowest known seq — divergence is below our
		// visibility. Per the rippled fallback, return 1.
		return 1
	}
	return low
}

// SpanTip is the public read-only view of the tip of a span. Port of
// rippled's SpanTip<Ledger> (LedgerTrie.h:39-73).
type SpanTip struct {
	Seq    uint32
	ID     consensus.LedgerID
	ledger Ledger
}

// Ancestor returns the ID of the ancestor at sequence s (s <= Seq).
func (t SpanTip) Ancestor(s uint32) consensus.LedgerID { return t.ledger.Ancestor(s) }

// Trie is the ancestry trie. The zero value is not usable — call New.
type Trie struct {
	root *node
	// genesis is captured at construction so an empty root always has
	// a concrete Ledger to resolve Ancestor(0) against.
	genesis Ledger

	// seqSupport[seq] is the number of ledgers at sequence `seq` that
	// currently hold tip support. Rippled uses std::map for ordered
	// iteration; we mirror that with a side-sorted slice of keys.
	seqSupport map[uint32]uint32
	seqKeys    []uint32 // sorted ascending
}

// New constructs an empty trie. genesis is used as the placeholder
// ledger for the root node and must satisfy Seq() == 0.
func New(genesis Ledger) *Trie {
	return &Trie{
		root:       newEmptyNode(genesis),
		genesis:    genesis,
		seqSupport: make(map[uint32]uint32),
	}
}

// Empty reports whether the trie has any support recorded.
func (t *Trie) Empty() bool { return t.root == nil || t.root.branchSupport == 0 }

// --- find ---

// find locates the node with the longest common ancestry prefix of
// `l` and returns (node, diffSeq). Port of LedgerTrie::find
// (LedgerTrie.h:371-401).
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

// findByLedgerID does an O(n) walk looking for an exact ID match.
// Port of LedgerTrie::findByLedgerID (LedgerTrie.h:409-423).
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

// --- seqSupport helpers ---

func (t *Trie) seqSupportAdd(seq uint32, delta uint32) {
	if _, ok := t.seqSupport[seq]; !ok {
		// insert seq into sorted key list
		idx := sort.Search(len(t.seqKeys), func(i int) bool { return t.seqKeys[i] >= seq })
		t.seqKeys = append(t.seqKeys, 0)
		copy(t.seqKeys[idx+1:], t.seqKeys[idx:])
		t.seqKeys[idx] = seq
	}
	t.seqSupport[seq] += delta
}

func (t *Trie) seqSupportSub(seq uint32, delta uint32) {
	cur, ok := t.seqSupport[seq]
	if !ok || cur < delta {
		return // caller validated; defensive no-op
	}
	cur -= delta
	if cur == 0 {
		delete(t.seqSupport, seq)
		idx := sort.Search(len(t.seqKeys), func(i int) bool { return t.seqKeys[i] >= seq })
		if idx < len(t.seqKeys) && t.seqKeys[idx] == seq {
			t.seqKeys = append(t.seqKeys[:idx], t.seqKeys[idx+1:]...)
		}
		return
	}
	t.seqSupport[seq] = cur
}

// --- Insert ---

// Insert adds `count` units of support for ledger l. The ledger's
// ancestry is walked from genesis to l itself; branchSupport is
// incremented along the spine and tipSupport is incremented on the
// node that owns l. Port of LedgerTrie::insert (LedgerTrie.h:452-531).
func (t *Trie) Insert(l Ledger, count uint32) {
	if count == 0 {
		return
	}
	loc, diffSeq := t.find(l)

	incNode := loc

	prefix, hasPrefix := loc.s.before(diffSeq)
	oldSuffix, hasOldSuffix := loc.s.from(diffSeq)
	newSuffix, hasNewSuffix := newSpanFromLedger(l).from(diffSeq)

	if hasOldSuffix {
		// Split: loc keeps the prefix; a new node takes over loc's
		// tip/branch support and its children (which are the suffix
		// continuation). Then loc's tipSupport resets to zero.
		_ = hasPrefix // prefix must be present whenever oldSuffix is (see LedgerTrie.h:500)
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

// --- Remove ---

// Remove decreases support for l by up to `count`, compacting the
// trie when a node's tipSupport falls to zero. Returns true when a
// matching node was found. Port of LedgerTrie::remove
// (LedgerTrie.h:540-589).
func (t *Trie) Remove(l Ledger, count uint32) bool {
	loc := t.findByLedgerID(l)
	if loc == nil || loc.tipSupport == 0 {
		return false
	}
	if count > loc.tipSupport {
		count = loc.tipSupport
	}
	if count == 0 {
		return false
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
			// Can't compact further — 0-tip node with >1 children is
			// a valid compressed-trie node.
			return true
		}
		loc = parent
	}
	return true
}

// --- Support queries ---

// TipSupport returns the tip support for the exact ledger l (0 if
// not in the trie).
func (t *Trie) TipSupport(l Ledger) uint32 {
	if loc := t.findByLedgerID(l); loc != nil {
		return loc.tipSupport
	}
	return 0
}

// BranchSupport returns the branch support rooted at l: tip of l
// plus all descendants of l. If l is a proper prefix of a trie span,
// returns the enclosing node's branchSupport. Port of
// LedgerTrie::branchSupport (LedgerTrie.h:610-623).
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

// --- GetPreferred ---

// GetPreferred returns the preferred ledger's SpanTip and true, or a
// zero SpanTip and false when the trie is empty. Port of
// LedgerTrie::getPreferred (LedgerTrie.h:684-778).
func (t *Trie) GetPreferred(largestIssued uint32) (SpanTip, bool) {
	if t.Empty() {
		return SpanTip{}, false
	}

	curr := t.root
	uncommitted := uint32(0)
	uncommittedIdx := 0 // index into t.seqKeys

	for curr != nil {
		// --- within-span advancement ---

		// Absorb uncommitted support for sequences earlier than
		// max(curr.start+1, largestIssued).
		nextSeq := curr.s.start + 1
		floor := nextSeq
		if largestIssued > floor {
			floor = largestIssued
		}
		for uncommittedIdx < len(t.seqKeys) && t.seqKeys[uncommittedIdx] < floor {
			uncommitted += t.seqSupport[t.seqKeys[uncommittedIdx]]
			uncommittedIdx++
		}

		// Advance nextSeq along the span while branchSupport > uncommitted.
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
			// Preferred sits strictly inside the current span.
			sub, ok := curr.s.before(nextSeq)
			if !ok {
				return curr.s.tip(), true
			}
			return sub.tip(), true
		}

		// --- between-spans: pick the best child ---
		var best *node
		var margin uint32
		switch len(curr.children) {
		case 0:
			best = nil
		case 1:
			best = curr.children[0]
			margin = best.branchSupport
		default:
			// partial_sort top-2 by (branchSupport desc, startID desc).
			// With small N a full sort is fine.
			sorted := make([]*node, len(curr.children))
			copy(sorted, curr.children)
			sort.Slice(sorted, func(i, j int) bool {
				if sorted[i].branchSupport != sorted[j].branchSupport {
					return sorted[i].branchSupport > sorted[j].branchSupport
				}
				return ledgerIDGreater(sorted[i].s.startID(), sorted[j].s.startID())
			})
			best = sorted[0]
			second := sorted[1]
			margin = best.branchSupport - second.branchSupport
			// Tie-break bonus: if best's startID sorts first it needs
			// one extra unit of ambiguity to lose. Matches the
			// `if (best->span.startID() > curr->children[1]->span.startID())
			//     margin++;`
			// at LedgerTrie.h:766-767 — comparing against the ORIGINAL
			// second child (index 1 after partial_sort), not the best.
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

// ledgerIDGreater returns a > b in lexicographic byte order.
func ledgerIDGreater(a, b consensus.LedgerID) bool {
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// --- Invariants ---

// CheckInvariants returns true when the compressed-trie invariants
// hold:
//   - every non-root 0-tip node has ≥2 children
//   - branchSupport == tipSupport + sum(child.branchSupport)
//   - parent pointers are consistent
//   - seqSupport matches the sum of tip supports at each sequence
//
// Port of LedgerTrie::checkInvariants (LedgerTrie.h:811-849).
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
