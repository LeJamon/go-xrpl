package shamap

import (
	"context"
	"fmt"
)

// CompletenessResult summarises a CheckComplete walk: how many inner and leaf
// nodes were visited, every referenced node the store does not have (Missing),
// and every referenced node the store has but cannot deserialize (Corrupt).
// Mirrors the missing-node accounting rippled performs in SHAMap::walkMap
// (SHAMap.cpp) / Ledger::walkLedger.
type CompletenessResult struct {
	InnerNodes int
	LeafNodes  int
	Missing    []MissingNode
	Corrupt    []MissingNode
}

// Complete reports whether the walk found the tree fully present and readable.
func (r *CompletenessResult) Complete() bool {
	return len(r.Missing) == 0 && len(r.Corrupt) == 0
}

// maxWalkDepth bounds the recursion. A SHAMap key is 256 bits / 4 bits per
// branch = 64 levels; the guard only fires on a corrupt store that returns an
// inner node where a leaf belongs, so it is reported as an error, not a panic.
const maxWalkDepth = 64

// CheckComplete walks the tree depth-first, forcing every referenced node to be
// resolved from the backing store, and reports any node that is missing or
// corrupt. It is the goXRPL analog of rippled's SHAMap::walkMap: it verifies
// that the content-addressed store holds the complete Merkle tree rooted at
// this map.
//
// For an unbacked (purely in-memory) map every node is already resolved, so the
// walk only counts nodes and always reports complete. For a map built via
// NewFromRootHash the children are hash-only until fetched, so the walk drives
// the lazy resolution and records branches whose node the store cannot supply.
//
// A genuine store I/O error aborts the walk and is returned; a node the store
// simply does not have is recorded in Missing and the walk continues, so a
// single call enumerates every gap in one pass.
func (sm *SHAMap) CheckComplete(ctx context.Context) (*CompletenessResult, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	res := &CompletenessResult{}
	if sm.root == nil {
		return res, nil
	}
	if err := sm.checkNodeComplete(ctx, sm.root, 0, res); err != nil {
		return nil, err
	}
	return res, nil
}

// checkNodeComplete recurses into node. The caller holds sm.mu.RLock; node is
// an already-resolved node (the root, an in-memory child, or one just fetched
// from the store), so it is never itself missing — only its referenced
// children can be.
func (sm *SHAMap) checkNodeComplete(ctx context.Context, node Node, depth int, res *CompletenessResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > maxWalkDepth {
		return fmt.Errorf("shamap: walk exceeded max depth %d (corrupt tree?)", maxWalkDepth)
	}

	inner, ok := node.(*InnerNode)
	if !ok {
		res.LeafNodes++
		return nil
	}
	res.InnerNodes++
	parentHash := inner.Hash()

	// Snapshot the branch table under the inner node's own lock so the walk
	// reads a consistent view without holding it across child fetches.
	inner.mu.RLock()
	isBranch := inner.isBranch
	hashes := inner.hashes
	children := inner.children
	inner.mu.RUnlock()

	for branch := 0; branch < BranchFactor; branch++ {
		if isBranch&(1<<branch) == 0 {
			continue
		}

		if child := children[branch]; child != nil {
			// Already resolved in memory — descend without touching the store.
			if err := sm.checkNodeComplete(ctx, child, depth+1, res); err != nil {
				return err
			}
			continue
		}

		// Hash-only branch: the node must come from the store.
		miss := MissingNode{
			Hash:       hashes[branch],
			Depth:      depth + 1,
			ParentHash: parentHash,
			Branch:     branch,
		}
		if !sm.backed || sm.family == nil {
			res.Missing = append(res.Missing, miss)
			continue
		}

		data, err := sm.family.Fetch(ctx, miss.Hash)
		if err != nil {
			return fmt.Errorf("shamap: fetch node %x at depth %d: %w", miss.Hash[:8], miss.Depth, err)
		}
		if data == nil {
			res.Missing = append(res.Missing, miss)
			continue
		}

		child, err := DeserializeFromPrefix(data)
		if err != nil {
			res.Corrupt = append(res.Corrupt, miss)
			continue
		}
		if err := sm.checkNodeComplete(ctx, child, depth+1, res); err != nil {
			return err
		}
	}
	return nil
}
