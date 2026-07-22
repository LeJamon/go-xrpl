package shamap

import (
	"context"
	"crypto/sha512"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/protocol"
)

type backedWalkItem struct {
	hash       [32]byte
	parentHash [32]byte
	nodeID     NodeID
	depth      int
	branch     int
	node       Node
}

type backedWalkFrame struct {
	item       backedWalkItem
	view       traversalNode
	nextBranch int
	full       bool
	stored     bool
	loaded     bool
	topLevel   bool
}

type backedWalkLane struct {
	root        backedWalkItem
	stack       []backedWalkFrame
	blocked     []backedWalkItem
	complete    bool
	durable     bool
	passDurable bool
}

type backedWalkCursor struct {
	generation uint32
	rootHash   [32]byte
	lanes      [BranchFactor]backedWalkLane
}

type traversalNode struct {
	inner    bool
	branches uint16
	hashes   [BranchFactor][32]byte
	children [BranchFactor]Node
}

func (sm *SHAMap) walkBackedContext(
	ctx context.Context,
	root *innerNode,
	family Family,
	cache *FullBelowCache,
	gen uint32,
	maxMissing int,
	filter SyncFilter,
) ([]MissingNode, error) {
	rootHash := root.Hash()
	if cache.Has(gen, rootHash) {
		return nil, nil
	}
	if sm.backedWalk == nil || sm.backedWalk.generation != gen || sm.backedWalk.rootHash != rootHash {
		sm.backedWalk = newBackedWalkCursor(root, rootHash, gen)
	}
	cursor := sm.backedWalk

	var (
		missing  []MissingNode
		resultMu sync.Mutex
		stop     atomic.Bool
		firstErr error
		errMu    sync.Mutex
		wg       sync.WaitGroup
		reported = make(map[[32]byte]struct{})
	)
	report := func(item backedWalkItem) {
		if stop.Load() {
			return
		}
		resultMu.Lock()
		defer resultMu.Unlock()
		if stop.Load() {
			return
		}
		if _, exists := reported[item.hash]; exists {
			return
		}
		reported[item.hash] = struct{}{}
		missing = append(missing, MissingNode{
			Hash:       item.hash,
			Depth:      item.depth,
			ParentHash: item.parentHash,
			Branch:     item.branch,
			NodeID:     item.nodeID,
		})
		if maxMissing > 0 && len(missing) >= maxMissing {
			stop.Store(true)
		}
	}

	for i := range BranchFactor {
		lane := &cursor.lanes[i]
		if lane.complete && lane.durable {
			continue
		}
		if lane.complete {
			lane.complete = false
			lane.passDurable = true
			lane.stack = append(lane.stack, backedWalkFrame{item: lane.root, topLevel: true})
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sm.walkBackedLane(ctx, family, cache, gen, root, lane, filter, &stop, report); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				stop.Store(true)
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	complete := true
	durable := true
	for i := range BranchFactor {
		if !cursor.lanes[i].complete {
			complete = false
		}
		durable = durable && cursor.lanes[i].durable
	}
	if complete {
		root.setFullBelowGen(gen)
	}
	if complete && durable {
		data, stored, err := fetchWithDurability(ctx, family, rootHash)
		if err != nil {
			return nil, err
		}
		if stored && len(data) != 0 {
			if _, err := decodeTraversalNode(data, rootHash); err != nil {
				return nil, err
			}
			cacheFullBelow(cache, gen, rootHash, 0)
		}
	}
	return missing, nil
}

func newBackedWalkCursor(root *innerNode, rootHash [32]byte, gen uint32) *backedWalkCursor {
	cursor := &backedWalkCursor{generation: gen, rootHash: rootHash}
	rootID := NewRootNodeID()
	for branch := range BranchFactor {
		child, hash, set := root.LoadChild(branch)
		if !set {
			cursor.lanes[branch].complete = true
			cursor.lanes[branch].durable = true
			continue
		}
		childID, err := rootID.ChildNodeID(uint8(branch))
		if err != nil {
			cursor.lanes[branch].complete = true
			continue
		}
		item := backedWalkItem{
			hash: hash, parentHash: rootHash, nodeID: childID, depth: 1, branch: branch, node: child,
		}
		cursor.lanes[branch].root = item
		cursor.lanes[branch].stack = []backedWalkFrame{{item: item, topLevel: true}}
		cursor.lanes[branch].passDurable = true
	}
	return cursor
}

func (sm *SHAMap) walkBackedLane(
	ctx context.Context,
	family Family,
	cache *FullBelowCache,
	gen uint32,
	root *innerNode,
	lane *backedWalkLane,
	filter SyncFilter,
	stop *atomic.Bool,
	report func(backedWalkItem),
) error {
	retryingBlocked := len(lane.blocked) != 0
	if len(lane.blocked) != 0 {
		for _, item := range dedupeBackedWalkItems(lane.blocked) {
			lane.stack = append(lane.stack, backedWalkFrame{item: item, topLevel: true})
		}
		lane.blocked = lane.blocked[:0]
	}
	for len(lane.stack) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if stop.Load() {
			return nil
		}

		last := len(lane.stack) - 1
		frame := &lane.stack[last]
		if !frame.loaded {
			if cache.Has(gen, frame.item.hash) {
				finishBackedWalkFrame(lane, true, true)
				continue
			}
			view, found, stored, err := sm.loadTraversalNode(ctx, family, root, frame.item)
			if err != nil {
				return err
			}
			if !found {
				lane.blocked = append(lane.blocked, frame.item)
				if filter.ShouldFetch(frame.item.hash) {
					report(frame.item)
				}
				finishBackedWalkFrame(lane, false, false)
				continue
			}
			if !view.inner {
				finishBackedWalkFrame(lane, true, stored)
				continue
			}
			frame.view = view
			frame.full = true
			frame.stored = stored
			frame.loaded = true
		}

		for frame.nextBranch < BranchFactor && frame.view.branches&(1<<frame.nextBranch) == 0 {
			frame.nextBranch++
		}
		if frame.nextBranch == BranchFactor {
			full, stored := frame.full, frame.stored
			if full && stored {
				cacheFullBelow(cache, gen, frame.item.hash, frame.item.depth)
			}
			finishBackedWalkFrame(lane, full, stored)
			continue
		}

		branch := frame.nextBranch
		childID, err := frame.item.nodeID.ChildNodeID(uint8(branch))
		if err != nil {
			return err
		}
		frame.nextBranch++
		lane.stack = append(lane.stack, backedWalkFrame{item: backedWalkItem{
			hash:       frame.view.hashes[branch],
			parentHash: frame.item.hash,
			nodeID:     childID,
			depth:      frame.item.depth + 1,
			branch:     branch,
			node:       frame.view.children[branch],
		}})
	}
	if len(lane.blocked) == 0 && !lane.passDurable && retryingBlocked {
		lane.passDurable = true
		lane.stack = append(lane.stack, backedWalkFrame{item: lane.root, topLevel: true})
		return sm.walkBackedLane(ctx, family, cache, gen, root, lane, filter, stop, report)
	}
	if len(lane.blocked) == 0 {
		lane.complete = true
		lane.durable = lane.passDurable
	} else {
		lane.complete = false
		lane.durable = false
	}
	return nil
}

func finishBackedWalkFrame(lane *backedWalkLane, full, stored bool) {
	last := len(lane.stack) - 1
	topLevel := lane.stack[last].topLevel
	lane.stack = lane.stack[:last]
	if topLevel {
		lane.passDurable = lane.passDurable && full && stored
		return
	}
	parent := &lane.stack[len(lane.stack)-1]
	parent.full = parent.full && full
	parent.stored = parent.stored && stored
}

func dedupeBackedWalkItems(items []backedWalkItem) []backedWalkItem {
	if len(items) < 2 {
		return items
	}
	seen := make(map[[32]byte]struct{}, len(items))
	out := items[:0]
	for i := range items {
		if _, exists := seen[items[i].hash]; exists {
			continue
		}
		seen[items[i].hash] = struct{}{}
		out = append(out, items[i])
	}
	return out
}

func (sm *SHAMap) loadTraversalNode(ctx context.Context, family Family, root *innerNode, item backedWalkItem) (traversalNode, bool, bool, error) {
	data, stored, err := fetchWithDurability(ctx, family, item.hash)
	if err != nil {
		return traversalNode{}, false, false, err
	}
	if len(data) != 0 {
		sm.familyLoads.Add(1)
		view, err := decodeTraversalNode(data, item.hash)
		if err != nil {
			return traversalNode{}, false, false, err
		}
		return view, true, stored, nil
	}
	if item.node != nil {
		return traversalNodeFromNode(item.node), true, false, nil
	}
	if attached := attachedNode(root, item.nodeID); attached != nil {
		return traversalNodeFromNode(attached), true, false, nil
	}
	return traversalNode{}, false, false, nil
}

func attachedNode(root *innerNode, nodeID NodeID) Node {
	current := Node(root)
	if current == nil {
		return nil
	}
	path := nodeID.ID()
	for depth := 0; depth < int(nodeID.Depth()); depth++ {
		inner, ok := current.(*innerNode)
		if !ok {
			return nil
		}
		child, _, set := inner.LoadChild(selectBranchForPath(path, depth))
		if !set || child == nil {
			return nil
		}
		current = child
	}
	return current
}

func traversalNodeFromNode(node Node) traversalNode {
	inner, ok := node.(*innerNode)
	if !ok {
		return traversalNode{}
	}
	inner.mu.RLock()
	defer inner.mu.RUnlock()
	return traversalNode{
		inner: true, branches: inner.isBranch, hashes: inner.hashes, children: inner.children,
	}
}

func decodeTraversalNode(data []byte, expected [32]byte) (traversalNode, error) {
	if len(data) < 4 {
		return traversalNode{}, fmt.Errorf("%w: data too short for prefix: %d bytes", ErrInvalidNodeData, len(data))
	}
	var prefix [4]byte
	copy(prefix[:], data[:4])
	view := traversalNode{}
	switch prefix {
	case protocol.HashPrefixInnerNode():
		if len(data) != fullInnerSerializedSize {
			return traversalNode{}, fmt.Errorf("%w: invalid inner node size: %d", ErrInvalidNodeData, len(data))
		}
		view.inner = true
		for branch := range BranchFactor {
			copy(view.hashes[branch][:], data[4+branch*32:4+(branch+1)*32])
			if !isZeroHash(view.hashes[branch]) {
				view.branches |= 1 << branch
			}
		}
	case protocol.HashPrefixLeafNode():
		if len(data) < 4+12+32 {
			return traversalNode{}, fmt.Errorf("%w: account-state leaf too short: %d", ErrInvalidNodeData, len(data))
		}
		var key [32]byte
		copy(key[:], data[len(data)-32:])
		if isZeroHash(key) {
			return traversalNode{}, fmt.Errorf("%w: account-state leaf has zero key", ErrInvalidNodeData)
		}
	case protocol.HashPrefixTransactionID():
		if len(data) < 4+12 {
			return traversalNode{}, fmt.Errorf("%w: transaction leaf too short: %d", ErrInvalidNodeData, len(data))
		}
	case protocol.HashPrefixTxNode():
		if len(data) < 4+12+32 {
			return traversalNode{}, fmt.Errorf("%w: transaction-with-metadata leaf too short: %d", ErrInvalidNodeData, len(data))
		}
	default:
		return traversalNode{}, fmt.Errorf("%w: unknown hash prefix: %x", ErrInvalidNodeData, prefix)
	}
	digest := sha512.Sum512(data)
	var actual [32]byte
	copy(actual[:], digest[:32])
	if actual != expected {
		return traversalNode{}, fmt.Errorf("%w: expected %x, got %x", ErrInvalidNodeData, expected[:8], actual[:8])
	}
	return view, nil
}
