package ledgertrie

import (
	"math/rand"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// idGreater reports whether a > b in lexicographic byte order. Local
// helper for tests that would otherwise need to slice unaddressable
// array returns.
func idGreater(a, b consensus.LedgerID) bool {
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// newTestTrie constructs an empty trie paired with a fresh builder.
// The genesis ledger for the trie is builder's genesis so ID matching
// works naturally.
func newTestTrie() (*Trie, *TestLedgerBuilder) {
	b := NewTestLedgerBuilder()
	return New(b.Genesis()), b
}

// --- TestLedgerTrie_ParityTable --------------------------------------
//
// Ports the state-assertion tests from rippled's
// src/test/consensus/LedgerTrie_test.cpp — testInsert, testRemove,
// testEmpty, testSupport — as table-driven subtests. Each subtest runs
// a sequence of trie operations and asserts expected tip/branch
// support at each named ledger. After every op we verify the trie's
// own invariants.

type trieAssertion struct {
	path   string
	tip    uint32
	branch uint32
}

func assertSupport(t *testing.T, trie *Trie, b *TestLedgerBuilder, a trieAssertion) {
	t.Helper()
	l := b.Build(a.path)
	if got := trie.TipSupport(l); got != a.tip {
		t.Errorf("TipSupport(%q) = %d, want %d", a.path, got, a.tip)
	}
	if got := trie.BranchSupport(l); got != a.branch {
		t.Errorf("BranchSupport(%q) = %d, want %d", a.path, got, a.branch)
	}
}

// TestLedgerTrie_ParityTable consolidates the testInsert / testRemove /
// testSupport scenarios from rippled's LedgerTrie_test.cpp as a single
// table-driven test. Each subtest is run in isolation via t.Run for
// per-scenario diagnostics. testEmpty, testGetPreferred, testRootRelated
// and testStress have their own top-level tests below.
func TestLedgerTrie_ParityTable(t *testing.T) {
	t.Run("Insert/Simple", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("abc"), 1)
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken after first insert")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 1})

		trie.Insert(b.Build("abc"), 1)
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken after duplicate insert")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 2, branch: 2})
	})

	t.Run("Insert/SuffixExtension", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("abc"), 1)
		trie.Insert(b.Build("abcd"), 1)
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 2})
		assertSupport(t, trie, b, trieAssertion{path: "abcd", tip: 1, branch: 1})

		trie.Insert(b.Build("abce"), 1)
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 3})
		assertSupport(t, trie, b, trieAssertion{path: "abcd", tip: 1, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "abce", tip: 1, branch: 1})
	})

	t.Run("Insert/UncommittedPrefix", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("abcd"), 1)
		trie.Insert(b.Build("abcdf"), 1)
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abcd", tip: 1, branch: 2})
		assertSupport(t, trie, b, trieAssertion{path: "abcdf", tip: 1, branch: 1})

		trie.Insert(b.Build("abc"), 1)
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 3})
		assertSupport(t, trie, b, trieAssertion{path: "abcd", tip: 1, branch: 2})
		assertSupport(t, trie, b, trieAssertion{path: "abcdf", tip: 1, branch: 1})
	})

	t.Run("Insert/SuffixAndUncommitted", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("abcd"), 1)
		trie.Insert(b.Build("abce"), 1)
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 0, branch: 2})
		assertSupport(t, trie, b, trieAssertion{path: "abcd", tip: 1, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "abce", tip: 1, branch: 1})
	})

	t.Run("Insert/SuffixAndUncommittedWithChild", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("abcd"), 1)
		trie.Insert(b.Build("abcde"), 1)
		trie.Insert(b.Build("abcf"), 1)
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 0, branch: 3})
		assertSupport(t, trie, b, trieAssertion{path: "abcd", tip: 1, branch: 2})
		assertSupport(t, trie, b, trieAssertion{path: "abcf", tip: 1, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "abcde", tip: 1, branch: 1})
	})

	t.Run("Insert/MultipleCounts", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("ab"), 4)
		assertSupport(t, trie, b, trieAssertion{path: "ab", tip: 4, branch: 4})
		assertSupport(t, trie, b, trieAssertion{path: "a", tip: 0, branch: 4})

		trie.Insert(b.Build("abc"), 2)
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 2, branch: 2})
		assertSupport(t, trie, b, trieAssertion{path: "ab", tip: 4, branch: 6})
		assertSupport(t, trie, b, trieAssertion{path: "a", tip: 0, branch: 6})
	})

	t.Run("Remove/NotInTrie", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("abc"), 1)
		if trie.Remove(b.Build("ab"), 1) {
			t.Errorf("Remove(ab) unexpectedly returned true")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		if trie.Remove(b.Build("a"), 1) {
			t.Errorf("Remove(a) unexpectedly returned true")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
	})

	t.Run("Remove/ZeroTip", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("abcd"), 1)
		trie.Insert(b.Build("abce"), 1)
		if trie.Remove(b.Build("abc"), 1) {
			t.Errorf("Remove on 0-tip node should return false")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 0, branch: 2})
	})

	t.Run("Remove/HighTip", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("abc"), 2)
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 2, branch: 2})
		if !trie.Remove(b.Build("abc"), 1) {
			t.Fatal("Remove(abc, 1) should return true")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 1})

		trie.Insert(b.Build("abc"), 1)
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 2, branch: 2})
		if !trie.Remove(b.Build("abc"), 2) {
			t.Fatal("Remove(abc, 2) should return true")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 0, branch: 0})

		trie.Insert(b.Build("abc"), 3)
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 3, branch: 3})
		// Over-subtract: clamp to tipSupport.
		if !trie.Remove(b.Build("abc"), 300) {
			t.Fatal("Remove(abc, 300) should return true")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 0, branch: 0})
	})

	t.Run("Remove/LeafCompaction", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("ab"), 1)
		trie.Insert(b.Build("abc"), 1)
		assertSupport(t, trie, b, trieAssertion{path: "ab", tip: 1, branch: 2})
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 1})

		if !trie.Remove(b.Build("abc"), 1) {
			t.Fatal("Remove(abc) should return true")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "ab", tip: 1, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 0, branch: 0})
	})

	t.Run("Remove/SingleChildMerge", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("ab"), 1)
		trie.Insert(b.Build("abc"), 1)
		trie.Insert(b.Build("abcd"), 1)

		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 2})

		if !trie.Remove(b.Build("abc"), 1) {
			t.Fatal("Remove(abc) should return true")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken after single-child merge")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 0, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "abcd", tip: 1, branch: 1})
	})

	t.Run("Remove/MultiChildNoCompact", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("ab"), 1)
		trie.Insert(b.Build("abc"), 1)
		trie.Insert(b.Build("abcd"), 1)
		trie.Insert(b.Build("abce"), 1)

		if !trie.Remove(b.Build("abc"), 1) {
			t.Fatal("Remove(abc) should return true")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 0, branch: 2})
	})

	t.Run("Remove/ParentCompaction", func(t *testing.T) {
		trie, b := newTestTrie()
		trie.Insert(b.Build("ab"), 1)
		trie.Insert(b.Build("abc"), 1)
		trie.Insert(b.Build("abd"), 1)
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken")
		}
		if !trie.Remove(b.Build("ab"), 1) {
			t.Fatal("Remove(ab) should return true")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken after ab removal")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "abd", tip: 1, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "ab", tip: 0, branch: 2})

		if !trie.Remove(b.Build("abd"), 1) {
			t.Fatal("Remove(abd) should return true")
		}
		if !trie.CheckInvariants() {
			t.Fatal("invariants broken after abd removal")
		}
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "ab", tip: 0, branch: 1})
	})

	t.Run("Support", func(t *testing.T) {
		trie, b := newTestTrie()
		assertSupport(t, trie, b, trieAssertion{path: "a", tip: 0, branch: 0})
		assertSupport(t, trie, b, trieAssertion{path: "axy", tip: 0, branch: 0})

		trie.Insert(b.Build("abc"), 1)
		assertSupport(t, trie, b, trieAssertion{path: "a", tip: 0, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "ab", tip: 0, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "abcd", tip: 0, branch: 0})

		trie.Insert(b.Build("abe"), 1)
		assertSupport(t, trie, b, trieAssertion{path: "a", tip: 0, branch: 2})
		assertSupport(t, trie, b, trieAssertion{path: "ab", tip: 0, branch: 2})
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 1, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "abe", tip: 1, branch: 1})

		if !trie.Remove(b.Build("abc"), 1) {
			t.Fatal("Remove(abc) should return true")
		}
		assertSupport(t, trie, b, trieAssertion{path: "a", tip: 0, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "ab", tip: 0, branch: 1})
		assertSupport(t, trie, b, trieAssertion{path: "abc", tip: 0, branch: 0})
		assertSupport(t, trie, b, trieAssertion{path: "abe", tip: 1, branch: 1})
	})
}

func TestLedgerTrie_Empty(t *testing.T) {
	trie, b := newTestTrie()
	if !trie.Empty() {
		t.Fatal("new trie should be empty")
	}

	trie.Insert(b.Build(""), 1) // genesis
	if trie.Empty() {
		t.Fatal("trie with genesis support should not be empty")
	}
	if !trie.Remove(b.Build(""), 1) {
		t.Fatal("Remove(genesis) should return true")
	}
	if !trie.Empty() {
		t.Fatal("trie should be empty after genesis removal")
	}

	trie.Insert(b.Build("abc"), 1)
	if trie.Empty() {
		t.Fatal("trie with abc should not be empty")
	}
	if !trie.Remove(b.Build("abc"), 1) {
		t.Fatal("Remove(abc) should return true")
	}
	if !trie.Empty() {
		t.Fatal("trie should be empty after abc removal")
	}
}

// --- getPreferred parity tests ---------------------------------------

// idOf is a small convenience helper for the getPreferred tests below.
func idOf(b *TestLedgerBuilder, path string) string { // returns hex-like
	id := b.Build(path).ID()
	return string(id[:])
}

// getPreferredID returns the preferred ID or "" if no preferred ledger.
func getPreferredID(trie *Trie, largestIssued uint32) string {
	tip, ok := trie.GetPreferred(largestIssued)
	if !ok {
		return ""
	}
	return string(tip.ID[:])
}

func TestLedgerTrie_GetPreferred_Empty(t *testing.T) {
	trie, _ := newTestTrie()
	if _, ok := trie.GetPreferred(0); ok {
		t.Fatal("empty trie should return (_, false)")
	}
	if _, ok := trie.GetPreferred(2); ok {
		t.Fatal("empty trie should return (_, false) at any seq")
	}
}

func TestLedgerTrie_GetPreferred_Genesis(t *testing.T) {
	trie, b := newTestTrie()
	genesis := b.Build("")
	trie.Insert(genesis, 1)
	if got := getPreferredID(trie, 0); got != idOf(b, "") {
		t.Fatalf("genesis insert: got != genesis ID")
	}
	if !trie.Remove(genesis, 1) {
		t.Fatal("Remove(genesis) should return true")
	}
	if _, ok := trie.GetPreferred(0); ok {
		t.Fatal("empty after remove should return false")
	}
}

func TestLedgerTrie_GetPreferred_SingleNoChildren(t *testing.T) {
	trie, b := newTestTrie()
	trie.Insert(b.Build("abc"), 1)
	if got := getPreferredID(trie, 3); got != idOf(b, "abc") {
		t.Fatalf("want abc, got different")
	}
}

func TestLedgerTrie_GetPreferred_SingleSmallerChild(t *testing.T) {
	trie, b := newTestTrie()
	trie.Insert(b.Build("abc"), 1)
	trie.Insert(b.Build("abcd"), 1)
	if got := getPreferredID(trie, 3); got != idOf(b, "abc") {
		t.Fatalf("want abc @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "abc") {
		t.Fatalf("want abc @4")
	}
}

func TestLedgerTrie_GetPreferred_SingleLargerChild(t *testing.T) {
	trie, b := newTestTrie()
	trie.Insert(b.Build("abc"), 1)
	trie.Insert(b.Build("abcd"), 2)
	if got := getPreferredID(trie, 3); got != idOf(b, "abcd") {
		t.Fatalf("want abcd @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "abcd") {
		t.Fatalf("want abcd @4")
	}
}

func TestLedgerTrie_GetPreferred_SingleSmallerChildrenSupport(t *testing.T) {
	trie, b := newTestTrie()
	trie.Insert(b.Build("abc"), 1)
	trie.Insert(b.Build("abcd"), 1)
	trie.Insert(b.Build("abce"), 1)
	if got := getPreferredID(trie, 3); got != idOf(b, "abc") {
		t.Fatalf("want abc @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "abc") {
		t.Fatalf("want abc @4")
	}

	trie.Insert(b.Build("abc"), 1)
	if got := getPreferredID(trie, 3); got != idOf(b, "abc") {
		t.Fatalf("want abc @3 after tie-breaker vote")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "abc") {
		t.Fatalf("want abc @4 after tie-breaker vote")
	}
}

func TestLedgerTrie_GetPreferred_SingleLargerChildren(t *testing.T) {
	trie, b := newTestTrie()
	trie.Insert(b.Build("abc"), 1)
	trie.Insert(b.Build("abcd"), 2)
	trie.Insert(b.Build("abce"), 1)
	if got := getPreferredID(trie, 3); got != idOf(b, "abc") {
		t.Fatalf("want abc @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "abc") {
		t.Fatalf("want abc @4")
	}

	trie.Insert(b.Build("abcd"), 1)
	if got := getPreferredID(trie, 3); got != idOf(b, "abcd") {
		t.Fatalf("want abcd @3 after extra vote")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "abcd") {
		t.Fatalf("want abcd @4 after extra vote")
	}
}

func TestLedgerTrie_GetPreferred_TieBreakerByID(t *testing.T) {
	// IDs in our TestLedgerBuilder are sha256-derived and thus
	// unpredictable. We compute which sibling has the larger ID at
	// seq 4 and steer assertions accordingly — mirroring rippled's
	// test which does the same with an explicit `>` check.
	trie, b := newTestTrie()
	abcd := b.Build("abcd")
	abce := b.Build("abce")
	var larger, smaller string
	if idGreater(abce.ID(), abcd.ID()) {
		larger, smaller = "abce", "abcd"
	} else {
		larger, smaller = "abcd", "abce"
	}

	trie.Insert(b.Build(smaller), 2)
	trie.Insert(b.Build(larger), 2)
	if got := getPreferredID(trie, 4); got != idOf(b, larger) {
		t.Fatalf("2-2 tie should go to the larger-ID sibling")
	}

	// Add one more to the smaller-ID side → 3 vs 2: still needs the
	// tie-breaker against uncommitted; preferred backs up to the
	// common ancestor.
	trie.Insert(b.Build(smaller), 1)
	if got := getPreferredID(trie, 4); got != idOf(b, smaller) {
		t.Fatalf("3-2 should go to the larger-branchSupport sibling")
	}
}

func TestLedgerTrie_GetPreferred_TieBreakerNotNeeded(t *testing.T) {
	trie, b := newTestTrie()
	abcd := b.Build("abcd")
	abce := b.Build("abce")
	var larger, smaller string
	if idGreater(abce.ID(), abcd.ID()) {
		larger, smaller = "abce", "abcd"
	} else {
		larger, smaller = "abcd", "abce"
	}

	trie.Insert(b.Build("abc"), 1)
	trie.Insert(b.Build(smaller), 1)
	trie.Insert(b.Build(larger), 2)
	// larger has margin of 1 but owns tie-breaker
	if got := getPreferredID(trie, 3); got != idOf(b, larger) {
		t.Fatalf("want larger @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, larger) {
		t.Fatalf("want larger @4")
	}

	trie.Remove(b.Build(larger), 1)
	trie.Insert(b.Build(smaller), 1)
	if got := getPreferredID(trie, 3); got != idOf(b, "abc") {
		t.Fatalf("after switch: want abc @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "abc") {
		t.Fatalf("after switch: want abc @4")
	}
}

func TestLedgerTrie_GetPreferred_LargerGrandchild(t *testing.T) {
	trie, b := newTestTrie()
	trie.Insert(b.Build("abc"), 1)
	trie.Insert(b.Build("abcd"), 2)
	trie.Insert(b.Build("abcde"), 4)
	if got := getPreferredID(trie, 3); got != idOf(b, "abcde") {
		t.Fatalf("want abcde @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "abcde") {
		t.Fatalf("want abcde @4")
	}
	if got := getPreferredID(trie, 5); got != idOf(b, "abcde") {
		t.Fatalf("want abcde @5")
	}
}

func TestLedgerTrie_GetPreferred_CompetingBranchesUncommitted(t *testing.T) {
	// Motivating example from issue #268: if competing branches are
	// tied and the spectator's validation is still out at a later
	// sequence, the uncommitted support prevents descending into a
	// tied branch. This is exactly the scenario the flat hash-count
	// approximation mis-calls.
	trie, b := newTestTrie()
	trie.Insert(b.Build("abc"), 1)
	trie.Insert(b.Build("abcde"), 2)
	trie.Insert(b.Build("abcfg"), 2)
	// de and fg are tied without abc's vote
	if got := getPreferredID(trie, 3); got != idOf(b, "abc") {
		t.Fatalf("want abc @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "abc") {
		t.Fatalf("want abc @4")
	}
	if got := getPreferredID(trie, 5); got != idOf(b, "abc") {
		t.Fatalf("want abc @5")
	}

	trie.Remove(b.Build("abc"), 1)
	trie.Insert(b.Build("abcd"), 1)

	// de branch has 3 to 2; seq 3/4 queries see it as preferred
	if got := getPreferredID(trie, 3); got != idOf(b, "abcde") {
		t.Fatalf("want abcde @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "abcde") {
		t.Fatalf("want abcde @4")
	}
	// At seq 5 the querier's own validation is still unaccounted,
	// so abc remains preferred.
	if got := getPreferredID(trie, 5); got != idOf(b, "abc") {
		t.Fatalf("want abc @5")
	}
}

// TestGetPreferred_PrefersDeepestSharedAncestor is the scenario
// called out directly by the issue: a minority near-tip should not
// outrank a majority-further-back branch.
func TestGetPreferred_PrefersDeepestSharedAncestor(t *testing.T) {
	//          /-> C        (tip 1)
	//     root-> B
	//          \-> D -> E   (tip 2 at E)
	//
	// Expected: the trie picks E because branchSupport(D) = 2 and
	// branchSupport(C) = 1.
	trie, b := newTestTrie()
	trie.Insert(b.Build("bc"), 1)
	trie.Insert(b.Build("bde"), 2)

	// At seq 3 (E's seq) the trie should pick E (largestIssued=3).
	if got := getPreferredID(trie, 3); got != idOf(b, "bde") {
		t.Fatalf("deepest-shared-ancestor: want bde @3")
	}
	// At seq 2, the root-of-fork 'b' is preferred because descending
	// to D still exceeds C's branchSupport.
	if got := getPreferredID(trie, 2); got != idOf(b, "bde") {
		t.Fatalf("deepest-shared-ancestor: want bde @2 (branchSupport 2>1)")
	}
}

// TestGetPreferred_MinSeqRespected — rippled calls the parameter
// largestIssued; the test here mirrors the "Too much uncommitted
// support" case at LedgerTrie_test.cpp:480-504 where a local validator
// waiting on a Seq-5 commitment is not allowed to descend past the
// common ancestor even when a branch appears to lead.
func TestGetPreferred_MinSeqRespected(t *testing.T) {
	trie, b := newTestTrie()
	trie.Insert(b.Build("abc"), 1)
	trie.Insert(b.Build("abcde"), 2)
	trie.Insert(b.Build("abcfg"), 2)

	// Without our own vote, any largestIssued keeps abc preferred.
	for _, seq := range []uint32{3, 4, 5} {
		if got := getPreferredID(trie, seq); got != idOf(b, "abc") {
			t.Fatalf("minSeq parity: want abc @%d", seq)
		}
	}

	// Adding a vote for abcd breaks the tie on the de branch.
	trie.Remove(b.Build("abc"), 1)
	trie.Insert(b.Build("abcd"), 1)

	// At seq 5 the local validator has effectively committed at seq
	// 5; uncommitted support from seq<5 is NOT replayed as evidence
	// for the de branch, so abc remains preferred.
	if got := getPreferredID(trie, 5); got != idOf(b, "abc") {
		t.Fatalf("minSeq parity: want abc @5 despite 3-2 lead on de")
	}
}

// TestLedgerTrie_GetPreferred_ChangingLargestIssued is the multi-step
// scenario from LedgerTrie_test.cpp:506-591 ("Changing largestSeq
// perspective changes preferred branch").
func TestLedgerTrie_GetPreferred_ChangingLargestIssued(t *testing.T) {
	trie, b := newTestTrie()
	trie.Insert(b.Build("ab"), 1)
	trie.Insert(b.Build("ac"), 1)
	trie.Insert(b.Build("acf"), 1)
	trie.Insert(b.Build("abde"), 2)

	// B has more branch support; seq 1/2 queries pick ab.
	if got := getPreferredID(trie, 1); got != idOf(b, "ab") {
		t.Fatalf("want ab @1")
	}
	if got := getPreferredID(trie, 2); got != idOf(b, "ab") {
		t.Fatalf("want ab @2")
	}
	// Seq 3/4 queriers haven't heard enough to prefer any branch.
	if got := getPreferredID(trie, 3); got != idOf(b, "a") {
		t.Fatalf("want a @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "a") {
		t.Fatalf("want a @4")
	}

	// E -> G: nothing changes because we simply extend the E tip.
	trie.Remove(b.Build("abde"), 1)
	trie.Insert(b.Build("abdeg"), 1)
	if got := getPreferredID(trie, 1); got != idOf(b, "ab") {
		t.Fatalf("E→G want ab @1")
	}
	if got := getPreferredID(trie, 2); got != idOf(b, "ab") {
		t.Fatalf("E→G want ab @2")
	}
	if got := getPreferredID(trie, 3); got != idOf(b, "a") {
		t.Fatalf("E→G want a @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "a") {
		t.Fatalf("E→G want a @4")
	}
	if got := getPreferredID(trie, 5); got != idOf(b, "a") {
		t.Fatalf("E→G want a @5")
	}

	// C vacates, H picks up → seq 3 query advances to ab.
	trie.Remove(b.Build("ac"), 1)
	trie.Insert(b.Build("abh"), 1)
	if got := getPreferredID(trie, 1); got != idOf(b, "ab") {
		t.Fatalf("C→H want ab @1")
	}
	if got := getPreferredID(trie, 2); got != idOf(b, "ab") {
		t.Fatalf("C→H want ab @2")
	}
	if got := getPreferredID(trie, 3); got != idOf(b, "ab") {
		t.Fatalf("C→H want ab @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "a") {
		t.Fatalf("C→H want a @4")
	}
	if got := getPreferredID(trie, 5); got != idOf(b, "a") {
		t.Fatalf("C→H want a @5")
	}

	// F migrates to E, now E has 2 tip support; preferred ledger at
	// early seqs descends to abde.
	trie.Remove(b.Build("acf"), 1)
	trie.Insert(b.Build("abde"), 1)
	if got := getPreferredID(trie, 1); got != idOf(b, "abde") {
		t.Fatalf("F→E want abde @1")
	}
	if got := getPreferredID(trie, 2); got != idOf(b, "abde") {
		t.Fatalf("F→E want abde @2")
	}
	if got := getPreferredID(trie, 3); got != idOf(b, "abde") {
		t.Fatalf("F→E want abde @3")
	}
	if got := getPreferredID(trie, 4); got != idOf(b, "ab") {
		t.Fatalf("F→E want ab @4")
	}
	if got := getPreferredID(trie, 5); got != idOf(b, "ab") {
		t.Fatalf("F→E want ab @5")
	}
}

// TestLedgerTrie_RootRelated ports testRootRelated from
// LedgerTrie_test.cpp:594-621.
func TestLedgerTrie_RootRelated(t *testing.T) {
	trie, b := newTestTrie()
	if trie.Remove(b.Build(""), 1) {
		t.Fatal("Remove(genesis) on empty trie should return false")
	}
	assertSupport(t, trie, b, trieAssertion{path: "", tip: 0, branch: 0})

	trie.Insert(b.Build("a"), 1)
	if !trie.CheckInvariants() {
		t.Fatal("invariants broken after insert(a)")
	}
	assertSupport(t, trie, b, trieAssertion{path: "", tip: 0, branch: 1})

	trie.Insert(b.Build("e"), 1)
	if !trie.CheckInvariants() {
		t.Fatal("invariants broken after insert(e)")
	}
	assertSupport(t, trie, b, trieAssertion{path: "", tip: 0, branch: 2})

	if !trie.Remove(b.Build("e"), 1) {
		t.Fatal("Remove(e) should return true")
	}
	if !trie.CheckInvariants() {
		t.Fatal("invariants broken after remove(e)")
	}
	assertSupport(t, trie, b, trieAssertion{path: "", tip: 0, branch: 1})
}

// --- Invariant stress test -------------------------------------------

func TestLedgerTrie_Stress_InvariantsHold(t *testing.T) {
	trie, b := newTestTrie()
	r := rand.New(rand.NewSource(42))

	// Pre-build a small pool of ledgers via short path strings.
	pool := []string{
		"a", "ab", "abc", "abd", "abcd", "abce",
		"abcde", "abcdf", "abcfg", "ac", "acf", "abh", "abde", "abdeg",
	}

	inserted := map[string]uint32{}
	for i := 0; i < 2000; i++ {
		path := pool[r.Intn(len(pool))]
		if inserted[path] > 0 && r.Intn(2) == 0 {
			cnt := uint32(1 + r.Intn(int(inserted[path])))
			if !trie.Remove(b.Build(path), cnt) {
				t.Fatalf("Remove(%q, %d) returned false (inserted=%d)", path, cnt, inserted[path])
			}
			inserted[path] -= cnt
		} else {
			cnt := uint32(1 + r.Intn(3))
			trie.Insert(b.Build(path), cnt)
			inserted[path] += cnt
		}
		if !trie.CheckInvariants() {
			t.Fatalf("invariants broken at iter %d after %s(%q)", i, "op", path)
		}
	}
}
