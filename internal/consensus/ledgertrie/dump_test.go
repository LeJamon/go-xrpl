package ledgertrie

import (
	"bytes"
	"strings"
	"testing"
)

// zeros is the 64-char hex rendering of the all-zero genesis ledger ID.
var zeros = strings.Repeat("0", 64)

func sp(n int) string { return strings.Repeat(" ", n) }

func TestTrie_DumpEmpty(t *testing.T) {
	trie, _ := newTestTrie()

	want := zeros + "[0,1)(T:0,B:0)\n"
	if got := trie.String(); got != want {
		t.Errorf("empty trie dump:\n got %q\nwant %q", got, want)
	}
}

func TestTrie_DumpSingleBranch(t *testing.T) {
	trie, b := newTestTrie()
	trie.Insert(b.Build("abc"), 1)

	root := zeros + "[0,1)(T:0,B:1)"
	child := "616263" + strings.Repeat("0", 58) + "[1,4)(T:1,B:1)"
	want := root + "\n" + sp(len(root)+1) + "|-" + child + "\n"

	var buf bytes.Buffer
	if err := trie.Dump(&buf); err != nil {
		t.Fatalf("Dump: %v", err)
	}
	if got := buf.String(); got != want {
		t.Errorf("single-branch dump mismatch:\n got %q\nwant %q", got, want)
	}
	if s := trie.String(); s != want {
		t.Errorf("String() must equal Dump():\n got %q\nwant %q", s, want)
	}
}

func TestTrie_DumpFork(t *testing.T) {
	trie, b := newTestTrie()
	trie.Insert(b.Build("abcd"), 1)
	trie.Insert(b.Build("abce"), 1)

	root := zeros + "[0,1)(T:0,B:2)"
	// Shared "abc" prefix node, tip is the seq-3 ancestor of abcd.
	child := "616263" + strings.Repeat("0", 58) + "[1,4)(T:0,B:2)"
	abcd := "61626364" + strings.Repeat("0", 56) + "[4,5)(T:1,B:1)"
	abce := "61626365" + strings.Repeat("0", 56) + "[4,5)(T:1,B:1)"

	childIndent := len(root) + 1
	// Grandchild offset = childOffset + len(child) + 3; indent = offset - 2.
	grandIndent := (len(root) + 3) + len(child) + 1
	want := root + "\n" +
		sp(childIndent) + "|-" + child + "\n" +
		sp(grandIndent) + "|-" + abcd + "\n" +
		sp(grandIndent) + "|-" + abce + "\n"

	if got := trie.String(); got != want {
		t.Errorf("fork dump mismatch:\n got %q\nwant %q", got, want)
	}
}
