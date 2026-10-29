package shamap

import (
	"context"
	"errors"
	"fmt"
	"unsafe"
)

// Lanes own fixed-depth DFS stacks, admitted lazily. Missing children are
// reported without blocking sibling discovery. Stacks survive yielded slices;
// compact proofs retain completed child branches across passes independently
// of node payloads.
type backedScanFrame struct {
	item     backedWalkItem
	view     traversalNode
	proof    *backedScanProof
	baseView traversalNode
	next     int
	complete uint16
	seen     uint16
	fresh    bool
	full     bool
	stored   bool
	loaded   bool
	usedBase bool
}

type backedScanLane struct {
	root       backedWalkItem
	proof      *backedScanProof
	stack      []backedScanFrame
	complete   bool
	incomplete bool
	started    bool
	seen       bool
	durable    bool
	hint       backedScanHint
}

type backedScanHint struct {
	hash    [32]byte
	pending bool
}

type backedScanCursor struct {
	generation  uint32
	rootHash    [32]byte
	baseRoot    [32]byte
	budget      *AcquisitionBudget
	fixedBytes  uint64
	proofCount  uint64
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
			sm.releaseCursorBytes(scanCursorBudget(cursor), uint64(capacity)*backedScanFrameBytes)
		}
		sm.releaseBackedScanProof(cursor, lane.proof)
		lane.proof = nil
		lane.root = backedWalkItem{}
	}
	if cursor.fixedBytes != 0 {
		sm.releaseCursorBytes(scanCursorBudget(cursor), cursor.fixedBytes)
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

func scanCursorBudget(cursor *backedScanCursor) *cursorBudget {
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
	sm.acquisition.stats.proofEntries.Store(cursor.proofCount)
	sm.acquisition.stats.proofCapacity.Store(cursor.proofCount)
}

func startBackedScanPass(cursor *backedScanCursor) {
	for i := range cursor.lanes {
		lane := &cursor.lanes[i]
		if lane.complete {
			continue
		}
		clear(lane.stack)
		lane.stack = lane.stack[:0]
		lane.incomplete = false
		lane.started = false
		lane.durable = true
		lane.hint = backedScanHint{}
		if lane.root.hash == ([32]byte{}) {
			lane.complete = true
			continue
		}
	}
	cursor.passActive = true
}

func (sm *SHAMap) pushBackedScanFrame(cursor *backedScanCursor, lane *backedScanLane, frame backedScanFrame) error {
	if cap(lane.stack) == 0 {
		const stackCapacity = maxDepth + 1
		charge := uint64(stackCapacity) * backedScanFrameBytes
		if !sm.reserveCursorBytes(scanCursorBudget(cursor), charge) {
			return ErrAcquisitionMemory
		}
		lane.stack = make([]backedScanFrame, 0, stackCapacity)
	}
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
		if frame.stored && !frame.usedBase && frame.view.inner {
			cache.Insert(generation, frame.item.hash)
		}
		sm.completeBackedScanProof(cursor, lane, &frame)
		if frame.stored && frame.item.parent != nil && frame.item.node != nil {
			sm.releaseChild(frame.item.parent, frame.item.branch, frame.item.node)
		}
	}
	if len(lane.stack) == 0 {
		lane.durable = lane.durable && frame.full && frame.stored
		capacity := cap(lane.stack)
		lane.stack = nil
		if capacity != 0 {
			sm.releaseCursorBytes(scanCursorBudget(cursor), uint64(capacity)*backedScanFrameBytes)
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
	probe bool,
) (bool, error) {
	for len(lane.stack) != 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		frame := &lane.stack[len(lane.stack)-1]
		if !frame.loaded {
			// A subtree hash says nothing about its depth, so neither shortcut
			// may vouch for a node at leaf depth; it is loaded and judged.
			atLeafDepth := frame.item.depth >= maxDepth
			if !atLeafDepth && frame.item.baseHash != ([32]byte{}) && frame.item.baseHash == frame.item.hash {
				sm.acquisition.stats.equalSubtreesSkipped.Add(1)
				frame.full, frame.stored, frame.usedBase, frame.loaded = true, true, true, true
				sm.observeBackedScanFrame(lane, frame)
				continue
			}
			if !atLeafDepth && cache.Has(generation, frame.item.hash) {
				frame.full, frame.stored, frame.loaded = true, true, true
				sm.observeBackedScanFrame(lane, frame)
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
				if probe {
					return true, nil
				}
				sm.retainBackedScanProofs(cursor, lane)
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
			if view.inner && atLeafDepth {
				return false, errInnerAtLeafDepth
			}
			frame.view = view
			if frame.proof != nil {
				frame.complete = frame.proof.complete
				frame.seen = frame.proof.seen
			}
			frame.full, frame.stored, frame.loaded = true, stored, true
			sm.observeBackedScanFrame(lane, frame)
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

		for frame.next < BranchFactor && (frame.view.branches&(1<<frame.next) == 0 ||
			frame.complete&(1<<frame.next) != 0) {
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
		child.fresh = frame.fresh
		if frame.proof != nil {
			child.proof = frame.proof.children[branch]
			child.fresh = child.fresh || frame.seen&(1<<branch) == 0
		}
		frame.next++
		if err := sm.pushBackedScanFrame(cursor, lane, child); err != nil {
			frame.next--
			return false, err
		}
	}
	lane.complete = !lane.incomplete
	if lane.complete && !lane.durable && lane.root.hash != ([32]byte{}) {
		lane.hint = backedScanHint{hash: lane.root.hash, pending: true}
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
	backed bool,
	probe bool,
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
	var base *verifiedWalkBase
	if backed {
		base = sm.acquisition.base
	}
	baseRoot := [32]byte{}
	if base != nil {
		baseRoot = base.rootHash
		if baseRoot == rootHash && !root.IsDirty() {
			sm.acquisition.stats.equalSubtreesSkipped.Add(1)
			root.setFullBelowGen(generation)
			cache.Insert(generation, rootHash)
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
		startBackedScanPass(cursor)
	}
	missing := make([]MissingNode, 0, maxMissing)
	blocked := false
	releasedStack := false
	for offset := range BranchFactor {
		branch := (int(cursor.nextLane) + offset) % BranchFactor
		lane := &cursor.lanes[branch]
		if lane.complete {
			continue
		}
		if !lane.started {
			if err := sm.pushBackedScanFrame(cursor, lane, backedScanFrame{item: lane.root, proof: lane.proof, fresh: !lane.seen}); err != nil {
				if errors.Is(err, ErrAcquisitionMemory) {
					blocked = true
					continue
				}
				return MissingNodesResult{Nodes: missing}, err
			}
			lane.started = true
		}
		hadStack := len(lane.stack) != 0
		full, err := sm.scanBackedLane(ctx, access, cache, generation, root, cursor, lane, filter, &missing, maxMissing, probe)
		releasedStack = releasedStack || hadStack && len(lane.stack) == 0
		if err != nil {
			if errors.Is(err, ErrTraversalBudget) {
				cursor.nextLane = uint8((branch + 1) % BranchFactor)
				return MissingNodesResult{Nodes: missing, Status: MissingNodesMore, reason: missingNodesTraversalYield}, nil
			}
			return MissingNodesResult{Nodes: missing}, err
		}
		if full {
			if probe {
				cursor.nextLane = uint8(branch)
			} else {
				cursor.nextLane = uint8((branch + 1) % BranchFactor)
			}
			return MissingNodesResult{Nodes: missing, Status: MissingNodesMore, reason: missingNodesResultFull}, nil
		}
	}
	if blocked {
		if releasedStack {
			return MissingNodesResult{Nodes: missing, Status: MissingNodesMore, reason: missingNodesTraversalYield}, nil
		}
		return MissingNodesResult{Nodes: missing, Status: MissingNodesMemory}, nil
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
	if err := ctx.Err(); err != nil {
		return MissingNodesResult{Nodes: missing}, err
	}
	if !backed {
		root.setFullBelowGen(generation)
		cache.Insert(generation, rootHash)
		sm.releaseBackedScanCursorLocked()
		return MissingNodesResult{Nodes: missing, Status: MissingNodesComplete}, nil
	}
	if !allDurable {
		return MissingNodesResult{Nodes: missing, Status: MissingNodesPersistence}, nil
	}
	if root.IsDirty() {
		return MissingNodesResult{Nodes: missing, Status: MissingNodesPersistence}, nil
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
	cache.Insert(generation, rootHash)
	sm.releaseBackedScanCursorLocked()
	return MissingNodesResult{Nodes: missing, Status: MissingNodesComplete}, nil
}

func (sm *SHAMap) scanUnbackedContext(
	ctx context.Context,
	root *innerNode,
	maxMissing int,
	filter SyncFilter,
	probe bool,
) (MissingNodesResult, error) {
	gen, cache, done := fullBelowContext(sm)
	defer done()
	return sm.scanBackedContext(ctx, root, nil, cache, gen, maxMissing, filter, false, probe)
}

// ScanMissingNodesContext performs one bounded, resumable discovery slice.
// Missing children never stop sibling discovery; a later call resumes the
// cursor or starts another pass when unresolved paths remain. A non-positive
// maxNodes selects the bounded default result window.
func (sm *SHAMap) ScanMissingNodesContext(ctx context.Context, maxNodes int, filter SyncFilter) (MissingNodesResult, error) {
	return sm.scanMissingNodesContext(ctx, maxNodes, filter, false)
}

// scanMissingNodesContext runs one scanner slice. A map found to hold an
// inner node at leaf depth is marked invalid and reported as ErrMapInvalid,
// as is any later scan of it.
func (sm *SHAMap) scanMissingNodesContext(ctx context.Context, maxNodes int, filter SyncFilter, probe bool) (MissingNodesResult, error) {
	result, err := sm.scanMissingNodesSlice(ctx, maxNodes, filter, probe)
	if errors.Is(err, errInnerAtLeafDepth) {
		sm.acquisition.walkMu.Lock()
		sm.tree.mu.Lock()
		sm.tree.state = stateInvalid
		sm.releaseBackedScanCursorLocked()
		sm.tree.mu.Unlock()
		sm.acquisition.walkMu.Unlock()
		return MissingNodesResult{}, ErrMapInvalid
	}
	return result, err
}

func (sm *SHAMap) scanMissingNodesSlice(ctx context.Context, maxNodes int, filter SyncFilter, probe bool) (MissingNodesResult, error) {
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
	if state == stateInvalid {
		return MissingNodesResult{}, ErrMapInvalid
	}
	if root == nil {
		return MissingNodesResult{Status: MissingNodesComplete}, nil
	}
	sm.backing.mu.RLock()
	access, cache := sm.backing.access, sm.backing.fullBelow
	backed := access.available() && cache != nil
	if !backed {
		sm.backing.mu.RUnlock()
		sm.tree.mu.RLock()
		defer sm.tree.mu.RUnlock()
		return sm.scanUnbackedContext(ctx, root, maxNodes, filter, probe)
	}
	defer sm.backing.mu.RUnlock()
	gen, done, err := sm.acquireBackedWalkGeneration(ctx, access, cache, true)
	if err != nil {
		return MissingNodesResult{}, err
	}
	defer done()
	return sm.scanBackedContext(ctx, root, access, cache, gen, maxNodes, filter, true, probe)
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
