package ledgertrie

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func hexID(id consensus.LedgerID) string { return fmt.Sprintf("%X", id[:]) }

func TestTrie_GetJson_Empty(t *testing.T) {
	tr, _ := newTestTrie()

	js := tr.GetJson()
	if _, err := json.Marshal(js); err != nil {
		t.Fatalf("GetJson output not JSON-marshalable: %v", err)
	}

	seqSupport, ok := js["seq_support"].(map[string]uint32)
	if !ok {
		t.Fatalf("seq_support: got %T, want map[string]uint32", js["seq_support"])
	}
	if len(seqSupport) != 0 {
		t.Errorf("empty trie seq_support = %v, want {}", seqSupport)
	}

	root, ok := js["trie"].(map[string]any)
	if !ok {
		t.Fatalf("trie: got %T, want map[string]any", js["trie"])
	}
	if root["seq"].(uint32) != 0 || root["tipSupport"].(uint32) != 0 || root["branchSupport"].(uint32) != 0 {
		t.Errorf("empty root = %v, want seq0/tip0/branch0", root)
	}
	if want := hexID(consensus.LedgerID{}) + "[0,1)"; root["span"] != want {
		t.Errorf("genesis span = %q, want %q", root["span"], want)
	}
	if _, has := root["children"]; has {
		t.Errorf("empty root must omit children key, got %v", root["children"])
	}
}

func TestTrie_GetJson_Structure(t *testing.T) {
	tr, b := newTestTrie()
	a := b.Build("a")
	abc := b.Build("abc")
	abcd := b.Build("abcd")
	tr.Insert(abc, 1)
	tr.Insert(abcd, 1)

	js := tr.GetJson()

	// Marshalable: this is how the adaptor emits the ValidationTrie log.
	if _, err := json.Marshal(js); err != nil {
		t.Fatalf("GetJson output not JSON-marshalable: %v", err)
	}

	seqSupport := js["seq_support"].(map[string]uint32)
	if len(seqSupport) != 2 || seqSupport["3"] != 1 || seqSupport["4"] != 1 {
		t.Errorf("seq_support = %v, want {3:1, 4:1}", seqSupport)
	}

	root := js["trie"].(map[string]any)
	if root["seq"].(uint32) != 0 || root["tipSupport"].(uint32) != 0 || root["branchSupport"].(uint32) != 2 {
		t.Errorf("root = %v, want seq0/tip0/branch2", root)
	}

	rootChildren := root["children"].([]any)
	if len(rootChildren) != 1 {
		t.Fatalf("root children = %d, want 1", len(rootChildren))
	}

	// abc node covers the compressed span [1,4): startID is the ledger at
	// seq 1 ('a'), the tip id is abc, and it still carries abcd's branch.
	abcNode := rootChildren[0].(map[string]any)
	if abcNode["seq"].(uint32) != 3 || abcNode["tipSupport"].(uint32) != 1 || abcNode["branchSupport"].(uint32) != 2 {
		t.Errorf("abc node = %v, want seq3/tip1/branch2", abcNode)
	}
	if want := hexID(abc.ID()) + "[1,4)"; abcNode["span"] != want {
		t.Errorf("abc span = %q, want %q", abcNode["span"], want)
	}
	if want := hexID(a.ID()); abcNode["startID"] != want {
		t.Errorf("abc startID = %q, want %q (ledger at seq 1)", abcNode["startID"], want)
	}

	abcChildren := abcNode["children"].([]any)
	if len(abcChildren) != 1 {
		t.Fatalf("abc children = %d, want 1", len(abcChildren))
	}
	abcdNode := abcChildren[0].(map[string]any)
	if abcdNode["seq"].(uint32) != 4 || abcdNode["tipSupport"].(uint32) != 1 || abcdNode["branchSupport"].(uint32) != 1 {
		t.Errorf("abcd node = %v, want seq4/tip1/branch1", abcdNode)
	}
	if want := hexID(abcd.ID()) + "[4,5)"; abcdNode["span"] != want {
		t.Errorf("abcd span = %q, want %q", abcdNode["span"], want)
	}
	if _, has := abcdNode["children"]; has {
		t.Errorf("abcd leaf must omit children key, got %v", abcdNode["children"])
	}
}
