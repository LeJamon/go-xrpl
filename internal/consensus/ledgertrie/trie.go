// Package ledgertrie implements branchSupport-based preferred-ledger selection
// over a compressed ancestry trie, porting rippled's LedgerTrie (LedgerTrie.h).
package ledgertrie

import (
	"bytes"
	"math"
	"reflect"
	"slices"

	"github.com/LeJamon/go-xrpl/internal/consensus"
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

// mismatch returns the first sequence at which a and b diverge, or 1 when the
// overlap is empty or mismatches at its floor (rippled's post-genesis-divergence
// fallback, RCLValidations.cpp:99).
func mismatch(a, b Ledger) uint32 {
	upper := min(b.Seq(), a.Seq())
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
	Seq uint32
	ID  consensus.LedgerID
}

// Trie is the ancestry trie. The zero value is not usable — call New.
type Trie struct {
	root *node

	// seqKeys is the sorted-key view over seqSupport.
	seqSupport map[uint32]uint32
	seqKeys    []uint32
}

// New constructs an empty trie. genesis must satisfy Seq() == 0.
func New(genesis Ledger) *Trie {
	if isNilLedger(genesis) {
		panic("ledgertrie: nil genesis ledger")
	}
	if genesis.Seq() != 0 {
		panic("ledgertrie: genesis sequence must be zero")
	}
	return &Trie{
		root:       newEmptyNode(genesis),
		seqSupport: make(map[uint32]uint32),
	}
}

func isNilLedger(l Ledger) bool {
	if l == nil {
		return true
	}
	v := reflect.ValueOf(l)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (t *Trie) empty() bool { return t.root == nil || t.root.branchSupport == 0 }

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

// seqSupportAdd/seqSupportSub keep seqKeys sorted with O(n) shifts. The set of
// distinct sequences with active branch support is small, so this stays simple.
func (t *Trie) seqSupportAdd(seq uint32, delta uint32) {
	current, ok := t.seqSupport[seq]
	if current > math.MaxUint32-delta {
		panic("ledgertrie: sequence support overflow")
	}
	if !ok {
		idx, _ := slices.BinarySearch(t.seqKeys, seq)
		t.seqKeys = slices.Insert(t.seqKeys, idx, seq)
	}
	t.seqSupport[seq] = current + delta
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

// Insert adds count support for l along its ancestry. A zero count is a
// no-op: a 0-count insert that takes the newSuffix branch would create a
// 0-tip leaf and break the compressed-trie invariant.
func (t *Trie) Insert(l Ledger, count uint32) {
	if isNilLedger(l) {
		panic("ledgertrie: nil ledger")
	}
	seq := l.Seq()
	if seq == math.MaxUint32 {
		panic("ledgertrie: ledger sequence cannot be represented")
	}
	if count == 0 {
		return
	}
	loc, diffSeq := t.find(l)

	prefix, hasPrefix := loc.s.before(diffSeq)
	oldSuffix, hasOldSuffix := loc.s.from(diffSeq)
	newSuffix, hasNewSuffix := newSpanFromLedger(l).from(diffSeq)

	if !hasOldSuffix && !hasNewSuffix && loc.tipSupport > math.MaxUint32-count {
		panic("ledgertrie: tip support overflow")
	}
	for cur := loc; cur != nil; cur = cur.parent {
		if cur.branchSupport > math.MaxUint32-count {
			panic("ledgertrie: branch support overflow")
		}
	}
	if t.seqSupport[seq] > math.MaxUint32-count {
		panic("ledgertrie: sequence support overflow")
	}

	incNode := loc

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

	t.seqSupportAdd(seq, count)
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
	if t.empty() {
		return SpanTip{}, false
	}

	curr := t.root
	uncommitted := uint64(0)
	uncommittedIdx := 0

	for curr != nil {
		// Absorb uncommitted support for seqs < max(curr.start+1, largestIssued).
		nextSeq := curr.s.start + 1
		floor := max(largestIssued, nextSeq)
		for uncommittedIdx < len(t.seqKeys) && t.seqKeys[uncommittedIdx] < floor {
			uncommitted += uint64(t.seqSupport[t.seqKeys[uncommittedIdx]])
			uncommittedIdx++
		}

		for nextSeq < curr.s.end && uint64(curr.branchSupport) > uncommitted {
			if uncommittedIdx < len(t.seqKeys) && t.seqKeys[uncommittedIdx] < curr.s.end {
				nextSeq = t.seqKeys[uncommittedIdx] + 1
				uncommitted += uint64(t.seqSupport[t.seqKeys[uncommittedIdx]])
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
		var margin uint64
		switch len(curr.children) {
		case 0:
			best = nil
		case 1:
			best = curr.children[0]
			margin = uint64(best.branchSupport)
		default:
			bestIndex := 0
			secondIndex := 1
			if nodeOutranks(curr.children[secondIndex], curr.children[bestIndex]) {
				bestIndex, secondIndex = secondIndex, bestIndex
			}
			for i := 2; i < len(curr.children); i++ {
				if nodeOutranks(curr.children[i], curr.children[bestIndex]) {
					secondIndex = bestIndex
					bestIndex = i
				} else if nodeOutranks(curr.children[i], curr.children[secondIndex]) {
					secondIndex = i
				}
			}

			best = curr.children[bestIndex]
			second := curr.children[secondIndex]
			if bestIndex != 0 {
				curr.children[0], curr.children[bestIndex] = curr.children[bestIndex], curr.children[0]
			}
			for i := 1; i < len(curr.children); i++ {
				if curr.children[i] == second {
					curr.children[1], curr.children[i] = curr.children[i], curr.children[1]
					break
				}
			}

			margin = uint64(best.branchSupport) - uint64(second.branchSupport)
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
	if t == nil || t.root == nil || t.root.parent != nil {
		return false
	}
	if len(t.seqKeys) != len(t.seqSupport) {
		return false
	}
	for i, seq := range t.seqKeys {
		if i > 0 && t.seqKeys[i-1] >= seq {
			return false
		}
		if support, ok := t.seqSupport[seq]; !ok || support == 0 {
			return false
		}
	}

	expected := make(map[uint32]uint64)
	stack := []*node{t.root}
	for len(stack) > 0 {
		curr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if curr == nil || curr.s.start >= curr.s.end || isNilLedger(curr.s.ledger) {
			return false
		}
		if curr != t.root && curr.tipSupport == 0 && len(curr.children) < 2 {
			return false
		}
		support := uint64(curr.tipSupport)
		if curr.tipSupport != 0 {
			seq := curr.s.end - 1
			expected[seq] += uint64(curr.tipSupport)
			if expected[seq] > math.MaxUint32 {
				return false
			}
		}
		for _, c := range curr.children {
			if c.parent != curr {
				return false
			}
			support += uint64(c.branchSupport)
			stack = append(stack, c)
		}
		if support > math.MaxUint32 || support != uint64(curr.branchSupport) {
			return false
		}
	}
	if len(expected) != len(t.seqSupport) {
		return false
	}
	for k, v := range expected {
		if uint64(t.seqSupport[k]) != v {
			return false
		}
	}
	return true
}
