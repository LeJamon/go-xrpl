package shamap

import (
	"context"
	"errors"
	"fmt"
	"unsafe"
)

// The scanner owns one fixed-depth DFS stack per root lane. A stack frame is
// retained only while a traversal slice is in progress; unavailable children
// are reported and the enclosing frame advances to its next sibling.
type backedScanFrame struct {
	item     backedWalkItem
	view     traversalNode
	baseView traversalNode
	next     int
	full     bool
	stored   bool
	loaded   bool
	usedBase bool
}

type backedScanLane struct {
	root       backedWalkItem
	stack      []backedScanFrame
	complete   bool
	incomplete bool
	durable    bool
	hint       backedScanHint
}

type backedScanHint struct {
	hash    [32]byte
	depth   int
	pending bool
}

type backedScanCursor struct {
	generation uint32
	rootHash   [32]byte
	baseRoot   [32]byte
	budget     *AcquisitionBudget
	fixedBytes uint64
	nextLane   uint8
	passActive bool
	lanes      [BranchFactor]backedScanLane
}

const backedScanFrameBytes = uint64(unsafe.Sizeof(backedScanFrame{}))

func (sm *SHAMap) newBackedScanCursor(
	root *innerNode,
	rootHash [32]byte,
	generation uint32,
	base *verifiedWalkBase,
	budget *AcquisitionBudget,
) (*backedScanCursor, error) {
	// The cursor and all lane stacks are admitted as one bounded reservation.
	// This keeps a small budget from partially constructing an unaccounted
	// frontier while making stack growth independent of the unresolved tree.
	frameCount := uint64(BranchFactor * (maxDepth + 1))
	charge := 2*uint64(unsafe.Sizeof(backedScanCursor{})) + frameCount*backedScanFrameBytes
	if budget != nil && !budget.tryReserveCursor(charge) {
		return nil, ErrAcquisitionMemory
	}
	cursor := &backedScanCursor{
		generation: generation,
		rootHash:   rootHash,
		budget:     budget,
	}
	if budget != nil {
		cursor.fixedBytes = charge
		sm.recordCursorReservation(charge)
	}
	rootID := newRootNodeID()
	for branch := range BranchFactor {
		child, hash, set := root.LoadChild(branch)
		lane := &cursor.lanes[branch]
		lane.stack = make([]backedScanFrame, 0, maxDepth+1)
		if !set {
			lane.complete = true
			lane.durable = true
			continue
		}
		childID, err := rootID.childNodeID(uint8(branch))
		if err != nil {
			lane.complete = true
			lane.durable = true
			continue
		}
		baseHash := [32]byte{}
		if base != nil && base.root.branches&(1<<branch) != 0 {
			baseHash = base.root.hashes[branch]
		}
		lane.root = backedWalkItem{
			hash: hash, baseHash: baseHash, parentHash: rootHash,
			nodeID: childID, depth: 1, branch: branch,
			parent: root, node: child,
		}
		lane.durable = true
	}
	if base != nil {
		cursor.baseRoot = base.rootHash
	}
	return cursor, nil
}

func (sm *SHAMap) releaseBackedScanCursorLocked() {
	cursor := sm.acquisition.scan
	if cursor == nil {
		return
	}
	for i := range cursor.lanes {
		lane := &cursor.lanes[i]
		clear(lane.stack)
		lane.stack = nil
		lane.root = backedWalkItem{}
	}
	if cursor.fixedBytes != 0 {
		sm.releaseCursorBytes(cursorAsWalkCursor(cursor), cursor.fixedBytes)
	}
	sm.acquisition.scan = nil
}

// cursorAsWalkCursor lets the shared cursor accounting release a reservation
// without retaining a second accounting implementation. It is safe because
// only budget and no backed-walk lane data are read by releaseCursorBytes.
func cursorAsWalkCursor(cursor *backedScanCursor) *backedWalkCursor {
	if cursor == nil {
		return nil
	}
	return &backedWalkCursor{budget: cursor.budget}
}

func (sm *SHAMap) clearBackedScanNodeReferencesLocked() {
	cursor := sm.acquisition.scan
	if cursor == nil {
		return
	}
	for i := range cursor.lanes {
		lane := &cursor.lanes[i]
		lane.root.parent = nil
		lane.root.node = nil
		for j := range lane.stack {
			frame := &lane.stack[j]
			frame.item.parent = nil
			frame.item.node = nil
			frame.view.children = [BranchFactor]mapNode{}
			frame.baseView.children = [BranchFactor]mapNode{}
		}
	}
}

func (sm *SHAMap) startBackedScanPass(cursor *backedScanCursor) {
	for i := range cursor.lanes {
		lane := &cursor.lanes[i]
		if lane.complete {
			continue
		}
		clear(lane.stack)
		lane.stack = lane.stack[:0]
		lane.incomplete = false
		lane.durable = true
		lane.hint = backedScanHint{}
		if lane.root.hash == ([32]byte{}) {
			lane.complete = true
			continue
		}
		lane.stack = append(lane.stack, backedScanFrame{item: lane.root})
	}
	cursor.passActive = true
}

func (sm *SHAMap) pushBackedScanFrame(lane *backedScanLane, frame backedScanFrame) error {
	if len(lane.stack) >= cap(lane.stack) {
		return errMaxDepthExceeded
	}
	lane.stack = append(lane.stack, frame)
	return nil
}

func (sm *SHAMap) finishBackedScanFrame(
	cursor *backedScanCursor,
	lane *backedScanLane,
	cache *FullBelowCache,
	generation uint32,
) {
	stack := lane.stack
	last := len(stack) - 1
	frame := stack[last]
	lane.stack[last] = backedScanFrame{}
	lane.stack = stack[:last]
	if frame.full {
		if frame.stored && !frame.usedBase {
			cacheFullBelow(cache, generation, frame.item.hash, frame.item.depth)
		}
		if frame.stored && frame.item.parent != nil && frame.item.node != nil {
			sm.releaseChild(frame.item.parent, frame.item.branch, frame.item.node)
		}
	}
	if len(lane.stack) == 0 {
		lane.durable = lane.durable && frame.full && frame.stored
		return
	}
	parent := &lane.stack[len(lane.stack)-1]
	parent.full = parent.full && frame.full
	parent.stored = parent.stored && frame.stored
	parent.usedBase = parent.usedBase || frame.usedBase
}

func (sm *SHAMap) scanBackedLane(
	ctx context.Context,
	access *familyAccess,
	cache *FullBelowCache,
	generation uint32,
	root *innerNode,
	cursor *backedScanCursor,
	lane *backedScanLane,
	filter SyncFilter,
	missing *[]MissingNode,
	maxMissing int,
) (bool, error) {
	for len(lane.stack) != 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		frame := &lane.stack[len(lane.stack)-1]
		if !frame.loaded {
			if frame.item.baseHash != ([32]byte{}) && frame.item.baseHash == frame.item.hash {
				frame.full, frame.stored, frame.usedBase, frame.loaded = true, true, true, true
				continue
			}
			if cache.Has(generation, frame.item.hash) {
				frame.full, frame.stored, frame.loaded = true, true, true
				continue
			}
			if !takeTraversalVisit(ctx) {
				return false, ErrTraversalBudget
			}
			sm.acquisition.stats.nodesDescended.Add(1)
			view, found, stored, err := sm.loadTraversalNode(ctx, access, root, frame.item)
			if err != nil {
				return false, err
			}
			if !found {
				lane.incomplete = true
				frame.full, frame.stored, frame.loaded = false, false, true
				outputFull := false
				if filter.ShouldFetch(frame.item.hash) {
					*missing = append(*missing, MissingNode{
						Hash: frame.item.hash, Depth: frame.item.depth,
						ParentHash: frame.item.parentHash, Branch: frame.item.branch,
						NodeID: frame.item.nodeID,
					})
					if maxMissing > 0 && len(*missing) >= maxMissing {
						outputFull = true
					}
				}
				sm.finishBackedScanFrame(cursor, lane, cache, generation)
				if outputFull {
					return true, nil
				}
				continue
			}
			frame.view = view
			frame.full, frame.stored, frame.loaded = true, stored, true
			if frame.item.baseHash != ([32]byte{}) {
				baseView, err := sm.loadVerifiedBaseTraversalNode(ctx, access, frame.item.baseHash)
				if err != nil {
					return false, err
				}
				frame.baseView = baseView
			}
		}

		for frame.next < BranchFactor && frame.view.branches&(1<<frame.next) == 0 {
			frame.next++
		}
		if frame.next == BranchFactor {
			sm.finishBackedScanFrame(cursor, lane, cache, generation)
			continue
		}
		branch := frame.next
		childID, err := frame.item.nodeID.childNodeID(uint8(branch))
		if err != nil {
			return false, err
		}
		var parent *innerNode
		if attached, ok := frame.item.node.(*innerNode); ok {
			parent = attached
		}
		baseHash := [32]byte{}
		if frame.baseView.inner && frame.baseView.branches&(1<<branch) != 0 {
			baseHash = frame.baseView.hashes[branch]
		}
		child := backedScanFrame{item: backedWalkItem{
			hash: frame.view.hashes[branch], baseHash: baseHash,
			parentHash: frame.item.hash, nodeID: childID,
			depth: frame.item.depth + 1, branch: branch,
			parent: parent, node: frame.view.children[branch],
		}}
		frame.next++
		if err := sm.pushBackedScanFrame(lane, child); err != nil {
			frame.next--
			return false, err
		}
	}
	lane.complete = !lane.incomplete
	if lane.complete && !lane.durable && lane.root.hash != ([32]byte{}) {
		lane.hint = backedScanHint{hash: lane.root.hash, depth: lane.root.depth, pending: true}
	}
	return false, nil
}

func (sm *SHAMap) scanBackedContext(
	ctx context.Context,
	root *innerNode,
	access *familyAccess,
	cache *FullBelowCache,
	generation uint32,
	maxMissing int,
	filter SyncFilter,
) (MissingNodesResult, error) {
	rootHash := root.Hash()
	base := sm.acquisition.base
	baseRoot := [32]byte{}
	if base != nil {
		baseRoot = base.rootHash
		if baseRoot == rootHash {
			sm.acquisition.stats.equalSubtreesSkipped.Add(1)
			root.setFullBelowGen(generation)
			cacheFullBelow(cache, generation, rootHash, 0)
			sm.releaseBackedScanCursorLocked()
			return MissingNodesResult{Status: MissingNodesComplete}, nil
		}
	}
	if cache.Has(generation, rootHash) {
		sm.releaseBackedScanCursorLocked()
		return MissingNodesResult{Status: MissingNodesComplete}, nil
	}
	cursor := sm.acquisition.scan
	if cursor == nil || cursor.generation != generation || cursor.rootHash != rootHash || cursor.baseRoot != baseRoot {
		sm.releaseBackedScanCursorLocked()
		var err error
		cursor, err = sm.newBackedScanCursor(root, rootHash, generation, base, sm.acquisition.budget)
		if err != nil {
			if errors.Is(err, ErrAcquisitionMemory) {
				return MissingNodesResult{Status: MissingNodesMemory}, nil
			}
			return MissingNodesResult{}, err
		}
		sm.acquisition.scan = cursor
	}
	if !cursor.passActive {
		sm.startBackedScanPass(cursor)
	}
	if maxMissing < 0 {
		maxMissing = 0
	}
	missing := make([]MissingNode, 0, min(maxMissing, 256))
	for offset := range BranchFactor {
		branch := (int(cursor.nextLane) + offset) % BranchFactor
		lane := &cursor.lanes[branch]
		if lane.complete {
			continue
		}
		full, err := sm.scanBackedLane(ctx, access, cache, generation, root, cursor, lane, filter, &missing, maxMissing)
		if err != nil {
			if errors.Is(err, ErrTraversalBudget) {
				return MissingNodesResult{Nodes: missing, Status: MissingNodesMore}, nil
			}
			return MissingNodesResult{Nodes: missing}, err
		}
		if full {
			cursor.passActive = true
			return MissingNodesResult{Nodes: missing, Status: MissingNodesMore}, nil
		}
	}

	allComplete := true
	for i := range cursor.lanes {
		if !cursor.lanes[i].complete {
			allComplete = false
			break
		}
	}
	cursor.passActive = false
	cursor.nextLane++
	if cursor.nextLane == BranchFactor {
		cursor.nextLane = 0
	}
	if !allComplete {
		return MissingNodesResult{Nodes: missing, Status: MissingNodesWaiting}, nil
	}
	if err := ctx.Err(); err != nil {
		return MissingNodesResult{Nodes: missing}, err
	}
	if !takeTraversalVisit(ctx) {
		return MissingNodesResult{Nodes: missing, Status: MissingNodesMore}, nil
	}
	sm.acquisition.stats.durableReads.Add(1)
	data, stored, err := access.fetchPreferDurable(ctx, rootHash)
	if err != nil {
		return MissingNodesResult{Nodes: missing}, err
	}
	if !stored || len(data) == 0 {
		return MissingNodesResult{Nodes: missing, Status: MissingNodesWaiting}, nil
	}
	if _, err := decodeTraversalNode(data, rootHash); err != nil {
		return MissingNodesResult{Nodes: missing}, err
	}
	root.setFullBelowGen(generation)
	cacheFullBelow(cache, generation, rootHash, 0)
	sm.releaseBackedScanCursorLocked()
	return MissingNodesResult{Nodes: missing, Status: MissingNodesComplete}, nil
}

// ScanMissingNodesContext performs one bounded, resumable discovery slice.
// Missing children never stop sibling discovery; a later call resumes the
// cursor or starts another pass when unresolved paths remain.
func (sm *SHAMap) ScanMissingNodesContext(ctx context.Context, maxNodes int, filter SyncFilter) (MissingNodesResult, error) {
	if err := ctx.Err(); err != nil {
		return MissingNodesResult{}, err
	}
	if filter == nil {
		filter = &defaultSyncFilter{}
	}
	sm.tree.mu.RLock()
	state := sm.tree.state
	root := sm.tree.root
	sm.tree.mu.RUnlock()
	if state != stateSyncing || root == nil {
		return MissingNodesResult{Status: MissingNodesComplete}, nil
	}
	sm.acquisition.walkMu.Lock()
	defer sm.acquisition.walkMu.Unlock()
	sm.backing.mu.RLock()
	access, cache := sm.backing.access, sm.backing.fullBelow
	sm.backing.mu.RUnlock()
	backed := access.available() && cache != nil
	if !backed {
		sm.tree.mu.RLock()
		defer sm.tree.mu.RUnlock()
		missing, err := sm.missingNodesLocked(maxNodes, filter, false)
		if err != nil {
			return MissingNodesResult{}, err
		}
		if maxNodes > 0 && len(missing) >= maxNodes {
			return MissingNodesResult{Nodes: missing, Status: MissingNodesMore}, nil
		}
		if len(missing) != 0 {
			return MissingNodesResult{Nodes: missing, Status: MissingNodesWaiting}, nil
		}
		return MissingNodesResult{Status: MissingNodesComplete}, nil
	}
	gen, done, err := sm.acquireBackedWalkGeneration(ctx, access, cache, true)
	if err != nil {
		return MissingNodesResult{}, err
	}
	defer done()
	return sm.scanBackedContext(ctx, root, access, cache, gen, maxNodes, filter)
}

// String is useful in acquisition logs without exposing implementation values.
func (s MissingNodesStatus) String() string {
	switch s {
	case MissingNodesMore:
		return "more"
	case MissingNodesWaiting:
		return "waiting"
	case MissingNodesComplete:
		return "complete"
	case MissingNodesMemory:
		return "memory"
	default:
		return fmt.Sprintf("missing-nodes-status(%d)", s)
	}
}
