package ledgertrie

import (
	"fmt"
	"io"
	"strings"
)

func (s span) String() string {
	tip := s.tip()
	return fmt.Sprintf("%X[%d,%d)", tip.ID[:], s.start, s.end)
}

func (n *node) String() string {
	return fmt.Sprintf("%s(T:%d,B:%d)", n.s.String(), n.tipSupport, n.branchSupport)
}

// Dump writes an indented ASCII rendering of the trie to w, one node per
// line as "<tipID>[start,end)(T:tipSupport,B:branchSupport)", each child
// indented beneath its parent. Intended for interactive debugging.
func (t *Trie) Dump(w io.Writer) error {
	return dumpNode(w, t.root, 0)
}

// String returns the trie's ASCII dump, satisfying fmt.Stringer.
func (t *Trie) String() string {
	var b strings.Builder
	_ = t.Dump(&b)
	return b.String()
}

// dumpNode prints curr and its subtree. offset is the column at which the
// "|-" marker is right-aligned; the root prints at offset 0 with no marker,
// and each child indents to offset + len(line) + 3 past its parent.
func dumpNode(w io.Writer, curr *node, offset int) error {
	if curr == nil {
		return nil
	}
	if offset > 2 {
		if _, err := io.WriteString(w, strings.Repeat(" ", offset-2)); err != nil {
			return err
		}
	}
	if offset > 0 {
		if _, err := io.WriteString(w, "|-"); err != nil {
			return err
		}
	}
	line := curr.String()
	if _, err := io.WriteString(w, line+"\n"); err != nil {
		return err
	}
	childOffset := offset + 1 + len(line) + 2
	for _, child := range curr.children {
		if err := dumpNode(w, child, childOffset); err != nil {
			return err
		}
	}
	return nil
}
