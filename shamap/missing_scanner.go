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
	generation  uint32
	rootHash    [32]byte
	baseRoot    [32]byte
	budget      *AcquisitionBudget
	fixedBytes  uint64
	nextLane    uint8
	passActive  bool
	rootDurable bool
	lanes       [BranchFactor]backedScanLane
}

const backedScanFrameBytes = uint64(unsafe.Sizeof(backedScanFrame{}))

type missingNodesMoreReason uint8

const (
	missingNodesResultFull missingNodesMoreReason = iota
	missingNodesTraversalYield
)

const backedScanDefaultResultLimit = 256

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
	charge := 2 * uint64(unsafe.Sizeof(backedScanCursor{}))
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
		capacity := cap(lane.stack)
		clear(lane.stack)
		lane.stack = nil
		if capacity != 0 {
			sm.releaseCursorBytes(cursorAsWalkCursor(cursor), uint64(capacity)*backedScanFrameBytes)
		}
		lane.root = backedWalkItem{}
	}
	if cursor.fixedBytes != 0 {
		sm.releaseCursorBytes(cursorAsWalkCursor(cursor), cursor.fixedBytes)
	}
	sm.acquisition.scan = nil
	sm.acquisition.base = nil
	sm.acquisition.stats.activeFrames.Store(0)
	sm.acquisition.stats.activeCapacity.Store(0)
	sm.acquisition.stats.blockedItems.Store(0)
	sm.acquisition.stats.blockedCapacity.Store(0)
	sm.acquisition.stats.proofEntries.Store(0)
	sm.acquisition.stats.proofCapacity.Store(0)
}

// cursorAsWalkCursor lets the shared cursor accounting release a reservation
// without retaining a second accounting implementation. It is safe because
// only budget and no backed-walk lane data are read by releaseCursorBytes.
func cursorAsWalkCursor(cursor *backedScanCursor) *cursorBudget {
	if cursor == nil {
		return nil
	}
	return &cursorBudget{budget: cursor.budget}
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

func (sm *SHAMap) updateBackedScanStats(cursor *backedScanCursor) {
	if cursor == nil {
		return
	}
	var active, capacity uint64
	for i := range cursor.lanes {
		active += uint64(len(cursor.lanes[i].stack))
		capacity += uint64(cap(cursor.lanes[i].stack))
	}
	sm.acquisition.stats.blockedItems.Store(0)
	sm.acquisition.stats.blockedCapacity.Store(0)
	sm.acquisition.stats.activeFrames.Store(active)
	sm.acquisition.stats.activeCapacity.Store(capacity)
	sm.acquisition.stats.proofEntries.Store(0)
	sm.acquisition.stats.proofCapacity.Store(0)
}

func (sm *SHAMap) startBackedScanPass(cursor *backedScanCursor) error {
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
		if err := sm.pushBackedScanFrame(cursor, lane, backedScanFrame{item: lane.root}); err != nil {
			return err
		}
	}
	cursor.passActive = true
	return nil
}

func (sm *SHAMap) pushBackedScanFrame(cursor *backedScanCursor, lane *backedScanLane, frame backedScanFrame) error {
	if len(lane.stack) >= cap(lane.stack) {
		oldCap := cap(lane.stack)
		newCap := max(1, oldCap*2)
		newCap = min(newCap, maxDepth+1)
		if newCap <= oldCap {
			return errMaxDepthExceeded
		}
		charge := uint64(newCap) * backedScanFrameBytes
		if !sm.reserveCursorBytes(cursorAsWalkCursor(cursor), charge) {
			return ErrAcquisitionMemory
		}
		next := make([]backedScanFrame, len(lane.stack), newCap)
		copy(next, lane.stack)
		lane.stack = next
		sm.releaseCursorBytes(cursorAsWalkCursor(cursor), uint64(oldCap)*backedScanFrameBytes)
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
		capacity := cap(lane.stack)
		lane.stack = nil
		if capacity != 0 {
			sm.releaseCursorBytes(cursorAsWalkCursor(cursor), uint64(capacity)*backedScanFrameBytes)
		}
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
				sm.acquisition.stats.equalSubtreesSkipped.Add(1)
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
					sm.acquisition.stats.missingNodes.Add(1)
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
					if !errors.Is(err, errVerifiedBaseUnavailable) {
						return false, err
					}
					sm.acquisition.stats.verifiedBaseFallbacks.Add(1)
				} else {
					frame.baseView = baseView
				}
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
		if err := sm.pushBackedScanFrame(cursor, lane, child); err != nil {
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
	// An empty SHAMap root has no serialized node and therefore no durable
	// record to await. Treat it as complete once the empty structure is
	// observed; requiring a store entry for the zero hash would leave empty
	// transaction maps permanently in the persistence state.
	if !root.HasChildren() {
		sm.releaseBackedScanCursorLocked()
		return MissingNodesResult{Status: MissingNodesComplete}, nil
	}
	base := sm.acquisition.base
	baseRoot := [32]byte{}
	if base != nil {
		baseRoot = base.rootHash
		if baseRoot == rootHash && !root.IsDirty() {
			sm.acquisition.stats.equalSubtreesSkipped.Add(1)
			root.setFullBelowGen(generation)
			cacheFullBelow(cache, generation, rootHash, 0)
			sm.releaseBackedScanCursorLocked()
			return MissingNodesResult{Status: MissingNodesComplete}, nil
		}
	}
	if cache.Has(generation, rootHash) && !root.IsDirty() {
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
	defer sm.updateBackedScanStats(cursor)
	if !cursor.passActive {
		if err := sm.startBackedScanPass(cursor); err != nil {
			if errors.Is(err, ErrAcquisitionMemory) {
				return MissingNodesResult{Status: MissingNodesMemory}, nil
			}
			return MissingNodesResult{}, err
		}
	}
	if maxMissing < 0 {
		maxMissing = 0
	}
	missing := make([]MissingNode, 0, min(maxMissing, backedScanDefaultResultLimit))
	for offset := range BranchFactor {
		branch := (int(cursor.nextLane) + offset) % BranchFactor
		lane := &cursor.lanes[branch]
		if lane.complete {
			continue
		}
		full, err := sm.scanBackedLane(ctx, access, cache, generation, root, cursor, lane, filter, &missing, maxMissing)
		if err != nil {
			if errors.Is(err, ErrTraversalBudget) {
				cursor.nextLane = uint8((branch + 1) % BranchFactor)
				return MissingNodesResult{Nodes: missing, Status: MissingNodesMore, reason: missingNodesTraversalYield}, nil
			}
			if errors.Is(err, ErrAcquisitionMemory) {
				return MissingNodesResult{Nodes: missing, Status: MissingNodesMemory}, nil
			}
			return MissingNodesResult{Nodes: missing}, err
		}
		if full {
			cursor.nextLane = uint8((branch + 1) % BranchFactor)
			cursor.passActive = true
			return MissingNodesResult{Nodes: missing, Status: MissingNodesMore, reason: missingNodesResultFull}, nil
		}
	}

	allComplete := true
	allDurable := true
	for i := range cursor.lanes {
		if !cursor.lanes[i].complete {
			allComplete = false
		}
		if !cursor.lanes[i].durable {
			allDurable = false
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
	if !allDurable {
		return MissingNodesResult{Nodes: missing, Status: MissingNodesPersistence}, nil
	}
	if root.IsDirty() {
		return MissingNodesResult{Nodes: missing, Status: MissingNodesPersistence}, nil
	}
	if err := ctx.Err(); err != nil {
		return MissingNodesResult{Nodes: missing}, err
	}
	if !takeTraversalVisit(ctx) {
		return MissingNodesResult{Nodes: missing, Status: MissingNodesMore, reason: missingNodesTraversalYield}, nil
	}
	sm.acquisition.stats.durableReads.Add(1)
	data, stored, err := access.fetchPreferDurable(ctx, rootHash)
	if err != nil {
		return MissingNodesResult{Nodes: missing}, err
	}
	if !stored || len(data) == 0 {
		return MissingNodesResult{Nodes: missing, Status: MissingNodesPersistence}, nil
	}
	if _, err := decodeTraversalNode(data, rootHash); err != nil {
		return MissingNodesResult{Nodes: missing}, err
	}
	cursor.rootDurable = true
	root.setFullBelowGen(generation)
	cacheFullBelow(cache, generation, rootHash, 0)
	sm.releaseBackedScanCursorLocked()
	return MissingNodesResult{Nodes: missing, Status: MissingNodesComplete}, nil
}

func (sm *SHAMap) scanUnbackedContext(
	ctx context.Context,
	root *innerNode,
	maxMissing int,
	filter SyncFilter,
) (MissingNodesResult, error) {
	gen, cache, done := fullBelowContext(sm)
	defer done()
	missing := make([]MissingNode, 0, min(maxMissing, backedScanDefaultResultLimit))
	full, stopped, err := walkFullBelow(
		ctx, sm, root, newRootNodeID(), root.Hash(), 0, gen, filter, false, cache,
		func(node MissingNode) bool {
			missing = append(missing, node)
			return maxMissing > 0 && len(missing) >= maxMissing
		}, nil,
	)
	if err != nil {
		return MissingNodesResult{Nodes: missing}, err
	}
	if stopped {
		return MissingNodesResult{Nodes: missing, Status: MissingNodesMore, reason: missingNodesResultFull}, nil
	}
	if !full {
		// A filtered path is represented by a false full-below result even when
		// the caller received no request IDs.
		return MissingNodesResult{Nodes: missing, Status: MissingNodesWaiting}, nil
	}
	return MissingNodesResult{Status: MissingNodesComplete}, nil
}

// ScanMissingNodesContext performs one bounded, resumable discovery slice.
// Missing children never stop sibling discovery; a later call resumes the
// cursor or starts another pass when unresolved paths remain. A non-positive
// maxNodes selects the bounded default result window.
func (sm *SHAMap) ScanMissingNodesContext(ctx context.Context, maxNodes int, filter SyncFilter) (MissingNodesResult, error) {
	if err := ctx.Err(); err != nil {
		return MissingNodesResult{}, err
	}
	if filter == nil {
		filter = &defaultSyncFilter{}
	}
	if maxNodes <= 0 || maxNodes > backedScanDefaultResultLimit {
		maxNodes = backedScanDefaultResultLimit
	}
	sm.acquisition.walkMu.Lock()
	defer sm.acquisition.walkMu.Unlock()
	sm.tree.mu.RLock()
	state := sm.tree.state
	root := sm.tree.root
	sm.tree.mu.RUnlock()
	if state == stateInvalid || root == nil {
		return MissingNodesResult{Status: MissingNodesComplete}, nil
	}
	sm.backing.mu.RLock()
	access, cache := sm.backing.access, sm.backing.fullBelow
	backed := access.available() && cache != nil
	if !backed {
		sm.backing.mu.RUnlock()
		sm.tree.mu.RLock()
		defer sm.tree.mu.RUnlock()
		return sm.scanUnbackedContext(ctx, root, maxNodes, filter)
	}
	defer sm.backing.mu.RUnlock()
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
	case MissingNodesPersistence:
		return "persistence"
	default:
		return fmt.Sprintf("missing-nodes-status(%d)", s)
	}
}
