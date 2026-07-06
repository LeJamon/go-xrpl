package ledgertrie

import (
	"fmt"
	"strconv"
)

// GetJson returns a JSON-serializable snapshot of the trie's support state:
// the compressed ancestry trie under "trie" and a per-sequence tip-count map
// under "seq_support".
func (t *Trie) GetJson() map[string]any {
	seqSupport := make(map[string]uint32, len(t.seqSupport))
	for seq, sup := range t.seqSupport {
		seqSupport[strconv.FormatUint(uint64(seq), 10)] = sup
	}
	return map[string]any{
		"trie":        t.root.getJSON(),
		"seq_support": seqSupport,
	}
}

// getJSON recursively serializes n and its descendants. The "span" string is
// the tip ID followed by the half-open [start,end) interval, matching rippled's
// wire format.
func (n *node) getJSON() map[string]any {
	tip := n.s.tip()
	startID := n.s.startID()
	res := map[string]any{
		"span":          fmt.Sprintf("%X[%d,%d)", tip.ID[:], n.s.start, n.s.end),
		"startID":       fmt.Sprintf("%X", startID[:]),
		"seq":           tip.Seq,
		"tipSupport":    n.tipSupport,
		"branchSupport": n.branchSupport,
	}
	if len(n.children) > 0 {
		children := make([]any, len(n.children))
		for i, c := range n.children {
			children[i] = c.getJSON()
		}
		res["children"] = children
	}
	return res
}
