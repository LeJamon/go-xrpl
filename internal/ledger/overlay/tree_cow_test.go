package overlay

import (
	"bytes"
	"errors"
	"testing"
)

func treeTestKey(value byte) [32]byte {
	var key [32]byte
	key[31] = value
	return key
}

type treeTestReporter interface {
	Helper()
	Fatalf(string, ...any)
}

func buildTreeTestTree(t treeTestReporter, owner *budget, keys ...byte) *treeNode {
	t.Helper()
	var root *treeNode
	for _, key := range keys {
		next, err := insertTreeNode(owner, root, treeTestKey(key), []byte{key}, false)
		if err != nil {
			releaseTreeNode(root)
			t.Fatalf("insert %d: %v", key, err)
		}
		releaseTreeNode(root)
		root = next
	}
	return root
}

func assertTreeAVL(t treeTestReporter, root *treeNode, min, max int) int {
	t.Helper()
	if root == nil {
		return 0
	}
	if int(root.key[31]) <= min || int(root.key[31]) >= max {
		t.Fatalf("tree ordering at %d outside (%d,%d)", root.key[31], min, max)
	}
	leftHeight := assertTreeAVL(t, root.left, min, int(root.key[31]))
	rightHeight := assertTreeAVL(t, root.right, int(root.key[31]), max)
	if difference := leftHeight - rightHeight; difference < -1 || difference > 1 {
		t.Fatalf("tree imbalance at %d: left=%d right=%d", root.key[31], leftHeight, rightHeight)
	}
	wantHeight := maxHeight(int8(leftHeight), int8(rightHeight)) + 1
	if root.height != wantHeight {
		t.Fatalf("tree height at %d = %d, want %d", root.key[31], root.height, wantHeight)
	}
	return int(wantHeight)
}

func TestExclusiveInsertRotationsStayWithinLeafBudget(t *testing.T) {
	cases := []struct {
		name    string
		initial []byte
		insert  byte
	}{
		{name: "right-right", initial: []byte{1, 2}, insert: 3},
		{name: "left-left", initial: []byte{3, 2}, insert: 1},
		{name: "left-right", initial: []byte{3, 1}, insert: 2},
		{name: "right-left", initial: []byte{1, 3}, insert: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
			root := buildTreeTestTree(t, owner, tc.initial...)
			defer func() {
				releaseTreeNode(root)
				if got := owner.memoryUsed(); got != 0 {
					t.Fatalf("memory credits leaked: %d", got)
				}
			}()

			baseline := owner.memoryUsed()
			leafCharge := nodeOverhead + blobOverhead + 1
			owner.mu.Lock()
			owner.maxMemory = baseline + leafCharge
			owner.mu.Unlock()
			updated, err := insertTreeNode(owner, root, treeTestKey(tc.insert), []byte{tc.insert}, false)
			if err != nil {
				t.Fatalf("rotation insert: %v", err)
			}
			releaseTreeNode(root)
			root = updated
			if root.key != treeTestKey(2) || root.left == nil || root.right == nil || root.left.key != treeTestKey(1) || root.right.key != treeTestKey(3) {
				t.Fatalf("rotated root = %#v, want 2 with children 1 and 3", root)
			}
			assertTreeAVL(t, root, -1, 256)
			if got := owner.memoryUsed(); got != baseline+leafCharge {
				t.Fatalf("rotation memory = %d, want %d", got, baseline+leafCharge)
			}
		})
	}
}

func TestExclusiveUpdateUsesOnlyReplacementAdmission(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
	root := buildTreeTestTree(t, owner, 2, 1, 3)
	defer func() {
		releaseTreeNode(root)
		if got := owner.memoryUsed(); got != 0 {
			t.Fatalf("memory credits leaked: %d", got)
		}
	}()

	baseline := owner.memoryUsed()
	newValue := []byte("replacement")
	owner.mu.Lock()
	owner.maxMemory = baseline + blobOverhead + uint64(len(newValue))
	owner.mu.Unlock()
	updated, err := insertTreeNode(owner, root, treeTestKey(2), newValue, false)
	if err != nil {
		t.Fatalf("exclusive update: %v", err)
	}
	if updated != root {
		t.Fatal("exclusive update cloned the root")
	}
	releaseTreeNode(root)
	root = updated
	if got := root.value.data; !bytes.Equal(got, newValue) {
		t.Fatalf("updated value = %q, want %q", got, newValue)
	}
	if got := owner.memoryUsed(); got != baseline-blobOverhead-1+blobOverhead+uint64(len(newValue)) {
		t.Fatalf("update memory = %d", got)
	}
}

func TestExclusiveUpdateFailurePreservesRoot(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
	root := buildTreeTestTree(t, owner, 2, 1, 3)
	defer func() {
		releaseTreeNode(root)
		if got := owner.memoryUsed(); got != 0 {
			t.Fatalf("memory credits leaked: %d", got)
		}
	}()

	baseline := owner.memoryUsed()
	owner.mu.Lock()
	owner.maxMemory = baseline
	owner.mu.Unlock()
	updated, err := insertTreeNode(owner, root, treeTestKey(2), []byte("replacement"), false)
	if !errors.Is(err, ErrMemoryBudget) || updated != nil {
		t.Fatalf("failed exclusive update = root %v, err %v, want ErrMemoryBudget with no result", updated, err)
	}
	if root.value == nil {
		t.Fatal("failed update cleared root value")
	}
	if !bytes.Equal(root.value.data, []byte{2}) {
		t.Fatalf("failed update changed root value to %q", root.value.data)
	}
	if got := owner.memoryUsed(); got != baseline {
		t.Fatalf("failed update memory = %d, want %d", got, baseline)
	}
}

func TestSharedAncestorDoesNotUseExclusiveMutation(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
	root := buildTreeTestTree(t, owner, 2, 1)
	shared := retainTreeNode(root)
	defer func() {
		releaseTreeNode(root)
		releaseTreeNode(shared)
		if got := owner.memoryUsed(); got != 0 {
			t.Fatalf("memory credits leaked: %d", got)
		}
	}()

	baseline := owner.memoryUsed()
	leafCharge := nodeOverhead + blobOverhead + 1
	owner.mu.Lock()
	owner.maxMemory = baseline + leafCharge
	owner.mu.Unlock()
	updated, err := insertTreeNode(owner, root, treeTestKey(3), []byte{3}, false)
	if !errors.Is(err, ErrMemoryBudget) || updated != nil {
		t.Fatalf("shared insertion = root %v, err %v, want ErrMemoryBudget with no result", updated, err)
	}
	if findTreeNode(root, treeTestKey(3)) != nil || findTreeNode(shared, treeTestKey(3)) != nil {
		t.Fatal("failed shared insertion changed an existing root")
	}
	if got := owner.memoryUsed(); got != baseline {
		t.Fatalf("failed insertion memory = %d, want %d", got, baseline)
	}
}

func TestSharedPrefixFailureAfterChildClone(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
	root := buildTreeTestTree(t, owner, 2, 1, 4)
	shared := retainTreeNode(root)
	defer func() {
		releaseTreeNode(root)
		releaseTreeNode(shared)
		if got := owner.memoryUsed(); got != 0 {
			t.Fatalf("memory credits leaked: %d", got)
		}
	}()

	baseline := owner.memoryUsed()
	leafCharge := nodeOverhead + blobOverhead + 1
	owner.mu.Lock()
	owner.maxMemory = baseline + leafCharge + nodeOverhead
	owner.mu.Unlock()
	updated, err := insertTreeNode(owner, root, treeTestKey(0), []byte{0}, false)
	if !errors.Is(err, ErrMemoryBudget) || updated != nil {
		t.Fatalf("shared-prefix insertion = root %v, err %v, want ErrMemoryBudget with no result", updated, err)
	}
	if findTreeNode(root, treeTestKey(0)) != nil || findTreeNode(shared, treeTestKey(0)) != nil {
		t.Fatal("failed shared-prefix insertion changed an existing root")
	}
	if got := owner.memoryUsed(); got != baseline {
		t.Fatalf("failed shared-prefix memory = %d, want %d", got, baseline)
	}
}

func TestExclusiveMutationPreservesCapturedRootAfterPriorShare(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
	root := buildTreeTestTree(t, owner, 1, 2)
	shared := retainTreeNode(root)
	first, err := insertTreeNode(owner, root, treeTestKey(3), []byte{3}, false)
	if err != nil {
		releaseTreeNode(shared)
		releaseTreeNode(root)
		t.Fatal(err)
	}
	releaseTreeNode(root)
	root = first
	defer func() {
		releaseTreeNode(root)
		releaseTreeNode(shared)
		if got := owner.memoryUsed(); got != 0 {
			t.Fatalf("memory credits leaked: %d", got)
		}
	}()

	updated, err := insertTreeNode(owner, root, treeTestKey(3), []byte("new"), false)
	if err != nil {
		t.Fatal(err)
	}
	releaseTreeNode(root)
	root = updated
	if got := findTreeNode(shared, treeTestKey(3)); got != nil {
		t.Fatalf("captured root unexpectedly gained key 3: %v", got)
	}
	updatedNode := findTreeNode(root, treeTestKey(3))
	if updatedNode == nil || !bytes.Equal(updatedNode.value.data, []byte("new")) {
		t.Fatalf("updated root key 3 = %v", updatedNode)
	}
}

func TestDeepNoopPreservesBudgetAndRoot(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
	root := buildTreeTestTree(t, owner, 2, 1, 3)
	defer func() {
		releaseTreeNode(root)
		if got := owner.memoryUsed(); got != 0 {
			t.Fatalf("memory credits leaked: %d", got)
		}
	}()

	baseline := owner.memoryUsed()
	owner.mu.Lock()
	owner.maxMemory = baseline
	owner.mu.Unlock()
	updated, err := insertTreeNode(owner, root, treeTestKey(3), []byte{3}, false)
	if err != nil {
		t.Fatalf("deep no-op: %v", err)
	}
	if updated != root {
		t.Fatal("deep no-op cloned the root")
	}
	if got := owner.memoryUsed(); got != baseline {
		t.Fatalf("deep no-op memory = %d, want %d", got, baseline)
	}
	releaseTreeNode(root)
	root = updated
}

func TestSharedInsertRotationsPreserveCapturedRoot(t *testing.T) {
	cases := []struct {
		name    string
		initial []byte
		insert  byte
	}{
		{name: "right-right", initial: []byte{1, 2}, insert: 3},
		{name: "left-left", initial: []byte{3, 2}, insert: 1},
		{name: "left-right", initial: []byte{3, 1}, insert: 2},
		{name: "right-left", initial: []byte{1, 3}, insert: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
			root := buildTreeTestTree(t, owner, tc.initial...)
			shared := retainTreeNode(root)
			defer func() {
				releaseTreeNode(root)
				releaseTreeNode(shared)
				if got := owner.memoryUsed(); got != 0 {
					t.Fatalf("memory credits leaked: %d", got)
				}
			}()

			updated, err := insertTreeNode(owner, root, treeTestKey(tc.insert), []byte{tc.insert}, false)
			if err != nil {
				t.Fatalf("shared rotation insert: %v", err)
			}
			releaseTreeNode(root)
			root = updated
			if findTreeNode(shared, treeTestKey(tc.insert)) != nil {
				t.Fatal("captured root changed during shared rotation")
			}
			assertTreeAVL(t, root, -1, 256)
		})
	}
}

func TestPrivatePrefixDoesNotMutateSharedDescendant(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
	root := buildTreeTestTree(t, owner, 4, 2, 6, 1, 3, 5, 7)
	sharedChild := retainTreeNode(root.left)
	defer func() {
		releaseTreeNode(root)
		releaseTreeNode(sharedChild)
		if got := owner.memoryUsed(); got != 0 {
			t.Fatalf("memory credits leaked: %d", got)
		}
	}()

	updated, err := insertTreeNode(owner, root, treeTestKey(0), []byte{0}, false)
	if err != nil {
		t.Fatalf("private-prefix insertion: %v", err)
	}
	if updated != root {
		t.Fatal("private prefix was unnecessarily cloned")
	}
	releaseTreeNode(root)
	root = updated
	if findTreeNode(sharedChild, treeTestKey(0)) != nil {
		t.Fatal("shared descendant changed during private-prefix insertion")
	}
	if findTreeNode(root, treeTestKey(0)) == nil {
		t.Fatal("private-prefix insertion missing from updated root")
	}
	assertTreeAVL(t, root, -1, 256)
}

func TestPrivatePrefixFailurePreservesSharedDescendant(t *testing.T) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 20, MaxDiskBytes: 1 << 20})
	root := buildTreeTestTree(t, owner, 4, 2, 6, 1, 3, 5, 7)
	sharedChild := retainTreeNode(root.left)
	defer func() {
		releaseTreeNode(root)
		releaseTreeNode(sharedChild)
		if got := owner.memoryUsed(); got != 0 {
			t.Fatalf("memory credits leaked: %d", got)
		}
	}()

	baseline := owner.memoryUsed()
	leafCharge := nodeOverhead + blobOverhead + 1
	owner.mu.Lock()
	owner.maxMemory = baseline + leafCharge
	owner.mu.Unlock()
	updated, err := insertTreeNode(owner, root, treeTestKey(0), []byte{0}, false)
	if !errors.Is(err, ErrMemoryBudget) || updated != nil {
		t.Fatalf("private-prefix failure = root %v, err %v, want ErrMemoryBudget with no result", updated, err)
	}
	if findTreeNode(root, treeTestKey(0)) != nil || findTreeNode(sharedChild, treeTestKey(0)) != nil {
		t.Fatal("failed private-prefix insertion changed an existing root")
	}
	if got := owner.memoryUsed(); got != baseline {
		t.Fatalf("failed private-prefix memory = %d, want %d", got, baseline)
	}
}

func BenchmarkInsertTreeNodeExclusive(b *testing.B) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 30, MaxDiskBytes: 1 << 30})
	root := buildTreeTestTree(b, owner, 8, 4, 12, 2, 6, 10, 14, 1, 3, 5, 7, 9, 11, 13, 15)
	firstValue := []byte("value-a")
	secondValue := []byte("value-b")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		value := firstValue
		if i&1 != 0 {
			value = secondValue
		}
		updated, err := insertTreeNode(owner, root, treeTestKey(8), value, false)
		if err != nil {
			b.Fatal(err)
		}
		releaseTreeNode(root)
		root = updated
	}
	b.StopTimer()
	releaseTreeNode(root)
}

func BenchmarkInsertTreeNodeExclusiveDeep(b *testing.B) {
	owner := newBudget(Config{MaxMemoryBytes: 1 << 30, MaxDiskBytes: 1 << 30})
	root := buildTreeTestTree(b, owner,
		16, 8, 24, 4, 12, 20, 28, 2, 6, 10, 14, 18, 22, 26, 30,
		1, 3, 5, 7, 9, 11, 13, 15, 17, 19, 21, 23, 25, 27, 29, 31,
	)
	firstValue := []byte("value-a")
	secondValue := []byte("value-b")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		value := firstValue
		if i&1 != 0 {
			value = secondValue
		}
		updated, err := insertTreeNode(owner, root, treeTestKey(1), value, false)
		if err != nil {
			b.Fatal(err)
		}
		releaseTreeNode(root)
		root = updated
	}
	b.StopTimer()
	releaseTreeNode(root)
}
