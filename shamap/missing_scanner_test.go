package shamap

import "testing"

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
			if _, ok := child.(*innerNode); ok && childID.Depth() == 3 {
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
		t.Fatal("filtered missing path was reported complete")
	}
	if filtered.Status == MissingNodesComplete {
		t.Fatalf("filtered scan status = %v, want unresolved work", filtered.Status)
	}
}
