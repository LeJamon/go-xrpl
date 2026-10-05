package shamap

import (
	"bytes"
	"context"
	"sync"
	"testing"
)

func TestScanMissingNodesResumesAcrossMissingInnerSiblings(t *testing.T) {
	source := deterministicIssue2043State(t, 4096)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}
	family := &issue2043HeldMemoryFamily{base: newMemoryFamily(), withheld: make(map[[32]byte]struct{})}
	if err := family.StoreBatch(t.Context(), entries); err != nil {
		t.Fatal(err)
	}
	var holdDepth func(mapNode, NodeID)
	withheld := 0
	holdDepth = func(node mapNode, id NodeID) {
		inner, ok := node.(*innerNode)
		if !ok {
			return
		}
		for branch := range BranchFactor {
			child, _, set := inner.LoadChild(branch)
			if !set || child == nil {
				continue
			}
			childID, err := id.childNodeID(uint8(branch))
			if err != nil {
				t.Fatal(err)
			}
			if childID.Depth() >= 3 {
				family.hold(child.Hash())
				withheld++
			}
			holdDepth(child, childID)
		}
	}
	holdDepth(source.tree.root, newRootNodeID())
	if withheld < BranchFactor {
		t.Fatalf("fixture withheld %d depth-three inners, want at least %d", withheld, BranchFactor)
	}

	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	dest.SetAcquisitionBudget(NewAcquisitionBudget(8 << 20))

	seen := make(map[[32]byte]struct{})
	for pass := 0; pass < 256; pass++ {
		result, scanErr := dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 2048), 256, nil)
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		if result.Status == MissingNodesMemory {
			t.Fatal("scanner exhausted bounded metadata budget")
		}
		for _, missing := range result.Nodes {
			seen[missing.Hash] = struct{}{}
			wire, err := family.wire(missing.Hash)
			if err != nil {
				t.Fatal(err)
			}
			attached, entry, err := dest.AddKnownNodeByIDWithEntryContext(t.Context(), missing.NodeID, wire)
			if err != nil {
				t.Fatal(err)
			}
			if attached != NodeUseful {
				t.Fatalf("attach %x = %v, want useful", missing.Hash[:4], attached)
			}
			if err := family.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
				t.Fatal(err)
			}
			family.release(missing.Hash)
		}
		if result.Status == MissingNodesPersistence {
			if err := dest.AcknowledgePersistedContext(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if result.Status == MissingNodesComplete {
			got, err := dest.Hash()
			if err != nil {
				t.Fatal(err)
			}
			if got != rootHash {
				t.Fatalf("completed root hash %x, want %x", got[:4], rootHash[:4])
			}
			if len(seen) != withheld {
				t.Fatalf("requested %d nodes, want each withheld inner once", len(seen))
			}
			return
		}
	}
	t.Fatalf("scanner did not complete after bounded passes (requested=%d)", len(seen))
}

func TestScanMissingNodesFilteredPathWaits(t *testing.T) {
	fixture := newIssue2043MemoryFixture(t, 128)
	defer fixture.releaseAll()
	dest := fixture.newDestination(t, NewAcquisitionBudget(8<<20))
	first, err := dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 4096), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Nodes) != 1 || first.Status != MissingNodesMore {
		t.Fatalf("first scan = %+v, want one-node more result", first)
	}
	filtered, err := dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 4096), 1, excludingHashFilter{hash: first.Nodes[0].Hash})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Status == MissingNodesComplete {
		t.Fatalf("filtered scan status = %v, want unresolved work", filtered.Status)
	}
}

func TestScanMissingNodesUnbackedFilteredPathWaits(t *testing.T) {
	source := New(TypeState)
	for _, key := range [][32]byte{{0x10}, {0x20}} {
		if err := source.Put(key, make([]byte, 12)); err != nil {
			t.Fatal(err)
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	dest := New(TypeState)
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	result, err := dest.ScanMissingNodesContext(t.Context(), 16, excludingAllFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != MissingNodesWaiting || len(result.Nodes) != 0 {
		t.Fatalf("filtered unbacked scan = %+v, want waiting with no IDs", result)
	}
}

func TestScanMissingNodesWaitsForDurableDescendants(t *testing.T) {
	source := deterministicIssue2043State(t, 256)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatal(err)
	}
	family := newStagedFamily()
	var staged []FlushEntry
	for _, entry := range entries {
		if entry.Hash == rootHash {
			if err := family.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		staged = append(staged, entry)
		family.stage(entry)
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatal(err)
	}
	dest.SetAcquisitionBudget(NewAcquisitionBudget(8 << 20))
	first, err := dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 4096), 256, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != MissingNodesPersistence {
		t.Fatalf("staged descendant scan = %+v, want persistence wait", first)
	}
	gen, done := family.FullBelowCache().Begin()
	if family.FullBelowCache().Has(gen, rootHash) {
		t.Fatal("root cache proof published before durable acknowledgement")
	}
	done()
	if err := family.StoreBatch(t.Context(), staged); err != nil {
		t.Fatal(err)
	}
	if err := dest.AcknowledgePersistedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	second, err := dest.ScanMissingNodesContext(t.Context(), 256, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != MissingNodesComplete {
		t.Fatalf("durable descendant scan = %+v, want complete", second)
	}
}

type stagedFamily struct {
	mu        sync.RWMutex
	durable   map[[32]byte][]byte
	staged    map[[32]byte][]byte
	fullBelow *FullBelowCache
}

func newStagedFamily() *stagedFamily {
	return &stagedFamily{
		durable: make(map[[32]byte][]byte), staged: make(map[[32]byte][]byte),
		fullBelow: NewFullBelowCache(),
	}
}

func (f *stagedFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	if data := f.staged[hash]; len(data) != 0 {
		return bytes.Clone(data), nil
	}
	return bytes.Clone(f.durable[hash]), nil
}

func (f *stagedFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return bytes.Clone(f.durable[hash]), nil
}

func (f *stagedFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range entries {
		f.durable[entry.Hash] = bytes.Clone(entry.Data)
		delete(f.staged, entry.Hash)
	}
	return nil
}

func (f *stagedFamily) FullBelowCache() *FullBelowCache { return f.fullBelow }

func (f *stagedFamily) stage(entry FlushEntry) {
	f.mu.Lock()
	f.staged[entry.Hash] = bytes.Clone(entry.Data)
	f.mu.Unlock()
}

type excludingAllFilter struct{}

func (excludingAllFilter) ShouldFetch([32]byte) bool { return false }

func TestScanMissingNodesTinySlicesAdvanceSiblingLanes(t *testing.T) {
	fixture := newIssue2043MemoryFixture(t, 512)
	defer fixture.releaseAll()
	dest := fixture.newDestination(t, NewAcquisitionBudget(8<<20))
	seen := make(map[[32]byte]struct{})
	for pass := 0; pass < 96 && len(seen) < fixture.withheld; pass++ {
		result, err := dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 128), 1, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Nodes) > 1 {
			t.Fatalf("tiny scan returned %d nodes", len(result.Nodes))
		}
		for _, missing := range result.Nodes {
			if _, duplicate := seen[missing.Hash]; duplicate {
				t.Fatalf("tiny scan repeated unresolved node %x before attachment", missing.Hash[:4])
			}
			seen[missing.Hash] = struct{}{}
		}
		if result.Status == MissingNodesComplete {
			break
		}
	}
	if len(seen) <= BranchFactor {
		t.Fatalf("tiny slices exposed only %d missing nodes; sibling lanes starved", len(seen))
	}
}
