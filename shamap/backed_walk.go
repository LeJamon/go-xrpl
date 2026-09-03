package shamap

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

const backedWalkProofChunkSize = 1024

var errVerifiedBaseUnavailable = errors.New("verified SHAMap base is unavailable")

// BackedWalkStats reports local discovery work performed by a backed SHAMap.
type BackedWalkStats struct {
	EqualSubtreesSkipped  uint64
	NodesDescended        uint64
	DurableReads          uint64
	MissingNodes          uint64
	VerifiedBaseFallbacks uint64
}

type backedWalkStats struct {
	equalSubtreesSkipped  atomic.Uint64
	nodesDescended        atomic.Uint64
	durableReads          atomic.Uint64
	missingNodes          atomic.Uint64
	verifiedBaseFallbacks atomic.Uint64
}

type verifiedWalkBase struct {
	rootHash [32]byte
	root     traversalNode
}

type traversalBudgetKey struct{}

type traversalBudget struct {
	remaining atomic.Int64
}

// WithTraversalBudget bounds the number of uncached nodes examined by backed
// SHAMap walks sharing the returned context. Exhaustion preserves the walk
// cursor and returns ErrTraversalBudget so the caller can resume later.
func WithTraversalBudget(ctx context.Context, maxVisits int64) context.Context {
	if maxVisits <= 0 {
		return ctx
	}
	budget := &traversalBudget{}
	budget.remaining.Store(maxVisits)
	return context.WithValue(ctx, traversalBudgetKey{}, budget)
}

func takeTraversalVisit(ctx context.Context) bool {
	budget, _ := ctx.Value(traversalBudgetKey{}).(*traversalBudget)
	return budget == nil || budget.remaining.Add(-1) >= 0
}

type backedWalkProof struct {
	hash  [32]byte
	depth uint8
}

type backedWalkProofs struct {
	chunks [][]backedWalkProof
	size   int
}

func (proofs *backedWalkProofs) add(hash [32]byte, depth int) {
	if len(proofs.chunks) == 0 || len(proofs.chunks[len(proofs.chunks)-1]) == backedWalkProofChunkSize {
		proofs.chunks = append(proofs.chunks, make([]backedWalkProof, 0, backedWalkProofChunkSize))
	}
	last := len(proofs.chunks) - 1
	proofs.chunks[last] = append(proofs.chunks[last], backedWalkProof{hash: hash, depth: uint8(depth)})
	proofs.size++
}

func (proofs backedWalkProofs) count() int {
	return proofs.size
}

func (proofs *backedWalkProofs) truncate(size int) {
	if size >= proofs.size {
		return
	}
	if size <= 0 {
		for i := range proofs.chunks {
			proofs.chunks[i] = nil
		}
		proofs.chunks = nil
		proofs.size = 0
		return
	}
	keepChunks := (size + backedWalkProofChunkSize - 1) / backedWalkProofChunkSize
	for i := keepChunks; i < len(proofs.chunks); i++ {
		proofs.chunks[i] = nil
	}
	proofs.chunks = proofs.chunks[:keepChunks]
	lastSize := size - (keepChunks-1)*backedWalkProofChunkSize
	proofs.chunks[keepChunks-1] = proofs.chunks[keepChunks-1][:lastSize]
	proofs.size = size
}

func (proofs *backedWalkProofs) clear() {
	proofs.truncate(0)
}

type backedWalkItem struct {
	hash       [32]byte
	baseHash   [32]byte
	parentHash [32]byte
	nodeID     NodeID
	depth      int
	branch     int
	parent     *innerNode
	node       mapNode
}

type backedWalkFrame struct {
	item       backedWalkItem
	view       traversalNode
	baseView   traversalNode
	nextBranch int
	full       bool
	stored     bool
	loaded     bool
	topLevel   bool
	proofStart int
	usedBase   bool
}

type backedWalkLane struct {
	root        backedWalkItem
	stack       []backedWalkFrame
	blocked     []backedWalkItem
	proofs      backedWalkProofs
	complete    bool
	durable     bool
	passDurable bool
}

type backedWalkCursor struct {
	generation uint32
	rootHash   [32]byte
	baseRoot   [32]byte
	lanes      [BranchFactor]backedWalkLane
}

type traversalNode struct {
	inner    bool
	branches uint16
	hashes   [BranchFactor][32]byte
	children [BranchFactor]mapNode
}

// SetVerifiedBaseContext installs a durable, position-aligned comparison base
// for subsequent backed walks. The caller must keep managed destructive store
// mutations excluded until the acquisition using the base has completed.
func (sm *SHAMap) SetVerifiedBaseContext(ctx context.Context, rootHash [32]byte) error {
	if rootHash == ([32]byte{}) {
		return errors.New("verified SHAMap base has an empty root hash")
	}
	sm.acquisition.walkMu.Lock()
	defer sm.acquisition.walkMu.Unlock()
	sm.backing.mu.RLock()
	access := sm.backing.access
	sm.backing.mu.RUnlock()
	if !access.available() || access.durable == nil {
		return errors.New("verified SHAMap base requires a durable backing family")
	}
	sm.acquisition.stats.durableReads.Add(1)
	data, err := access.fetchDurable(ctx, rootHash)
	if err != nil {
		return fmt.Errorf("load verified SHAMap base root: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("%w: root %x is missing", errVerifiedBaseUnavailable, rootHash[:8])
	}
	view, err := decodeTraversalNode(data, rootHash)
	if err != nil {
		return fmt.Errorf("%w: root %x: %v", errVerifiedBaseUnavailable, rootHash[:8], err)
	}
	if !view.inner {
		return fmt.Errorf("%w: root %x is not an inner node", errVerifiedBaseUnavailable, rootHash[:8])
	}
	sm.acquisition.base = &verifiedWalkBase{rootHash: rootHash, root: view}
	sm.acquisition.cursor = nil
	return nil
}

// BackedWalkStats returns a consistent snapshot of the acquisition counters.
func (sm *SHAMap) BackedWalkStats() BackedWalkStats {
	return BackedWalkStats{
		EqualSubtreesSkipped:  sm.acquisition.stats.equalSubtreesSkipped.Load(),
		NodesDescended:        sm.acquisition.stats.nodesDescended.Load(),
		DurableReads:          sm.acquisition.stats.durableReads.Load(),
		MissingNodes:          sm.acquisition.stats.missingNodes.Load(),
		VerifiedBaseFallbacks: sm.acquisition.stats.verifiedBaseFallbacks.Load(),
	}
}

func (sm *SHAMap) walkBackedContext(
	ctx context.Context,
	root *innerNode,
	access *familyAccess,
	cache *FullBelowCache,
	gen uint32,
	maxMissing int,
	filter SyncFilter,
) ([]MissingNode, error) {
	rootHash := root.Hash()
	if cache.Has(gen, rootHash) {
		return nil, nil
	}
	base := sm.acquisition.base
	baseRoot := [32]byte{}
	if base != nil {
		baseRoot = base.rootHash
		if baseRoot == rootHash {
			sm.acquisition.stats.equalSubtreesSkipped.Add(1)
			root.setFullBelowGen(gen)
			cacheFullBelow(cache, gen, rootHash, 0)
			return nil, nil
		}
	}
	if sm.acquisition.cursor == nil || sm.acquisition.cursor.generation != gen ||
		sm.acquisition.cursor.rootHash != rootHash || sm.acquisition.cursor.baseRoot != baseRoot {
		sm.acquisition.cursor = newBackedWalkCursor(root, rootHash, gen, base)
	}
	cursor := sm.acquisition.cursor

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
		sm.acquisition.stats.missingNodes.Add(1)
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
		if lane.complete {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sm.walkBackedLane(ctx, access, cache, gen, root, lane, filter, &stop, report); err != nil {
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
		if !takeTraversalVisit(ctx) {
			return nil, ErrTraversalBudget
		}
		data, stored, err := access.fetchPreferDurable(ctx, rootHash)
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

func newBackedWalkCursor(root *innerNode, rootHash [32]byte, gen uint32, base *verifiedWalkBase) *backedWalkCursor {
	cursor := &backedWalkCursor{generation: gen, rootHash: rootHash}
	if base != nil {
		cursor.baseRoot = base.rootHash
	}
	rootID := newRootNodeID()
	for branch := range BranchFactor {
		child, hash, set := root.LoadChild(branch)
		if !set {
			cursor.lanes[branch].complete = true
			cursor.lanes[branch].durable = true
			continue
		}
		childID, err := rootID.childNodeID(uint8(branch))
		if err != nil {
			cursor.lanes[branch].complete = true
			continue
		}
		baseHash := [32]byte{}
		if base != nil && base.root.branches&(1<<branch) != 0 {
			baseHash = base.root.hashes[branch]
		}
		item := backedWalkItem{
			hash: hash, baseHash: baseHash, parentHash: rootHash, nodeID: childID,
			depth: 1, branch: branch, parent: root, node: child,
		}
		cursor.lanes[branch].root = item
		cursor.lanes[branch].stack = []backedWalkFrame{{item: item, topLevel: true, proofStart: 0}}
		cursor.lanes[branch].passDurable = true
	}
	return cursor
}

func (sm *SHAMap) walkBackedLane(
	ctx context.Context,
	access *familyAccess,
	cache *FullBelowCache,
	gen uint32,
	root *innerNode,
	lane *backedWalkLane,
	filter SyncFilter,
	stop *atomic.Bool,
	report func(backedWalkItem),
) error {
	if len(lane.blocked) != 0 {
		for _, item := range dedupeBackedWalkItems(lane.blocked) {
			lane.stack = append(lane.stack, backedWalkFrame{
				item: item, topLevel: true, proofStart: lane.proofs.count(),
			})
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
			if frame.item.baseHash != ([32]byte{}) && frame.item.baseHash == frame.item.hash {
				sm.acquisition.stats.equalSubtreesSkipped.Add(1)
				sm.finishBackedWalkFrame(lane, cache, gen, true, true, true)
				continue
			}
			if cache.Has(gen, frame.item.hash) {
				sm.finishBackedWalkFrame(lane, cache, gen, true, true, false)
				continue
			}
			if !takeTraversalVisit(ctx) {
				return ErrTraversalBudget
			}
			sm.acquisition.stats.nodesDescended.Add(1)
			view, found, stored, err := sm.loadTraversalNode(ctx, access, root, frame.item)
			if err != nil {
				return err
			}
			if !found {
				lane.blocked = append(lane.blocked, frame.item)
				if filter.ShouldFetch(frame.item.hash) {
					report(frame.item)
				}
				sm.finishBackedWalkFrame(lane, cache, gen, false, false, false)
				continue
			}
			if !view.inner {
				sm.finishBackedWalkFrame(lane, cache, gen, true, stored, false)
				continue
			}
			frame.view = view
			if frame.item.baseHash != ([32]byte{}) {
				baseView, err := sm.loadVerifiedBaseTraversalNode(ctx, access, frame.item.baseHash)
				if err != nil {
					return err
				}
				frame.baseView = baseView
			}
			frame.full = true
			frame.stored = stored
			frame.loaded = true
		}

		for frame.nextBranch < BranchFactor && frame.view.branches&(1<<frame.nextBranch) == 0 {
			frame.nextBranch++
		}
		if frame.nextBranch == BranchFactor {
			full, stored, usedBase := frame.full, frame.stored, frame.usedBase
			sm.finishBackedWalkFrame(lane, cache, gen, full, stored, usedBase)
			continue
		}

		branch := frame.nextBranch
		childID, err := frame.item.nodeID.childNodeID(uint8(branch))
		if err != nil {
			return err
		}
		frame.nextBranch++
		var parent *innerNode
		if attached, ok := frame.item.node.(*innerNode); ok {
			parent = attached
		}
		baseHash := [32]byte{}
		if frame.baseView.inner && frame.baseView.branches&(1<<branch) != 0 {
			baseHash = frame.baseView.hashes[branch]
		}
		lane.stack = append(lane.stack, backedWalkFrame{
			item: backedWalkItem{
				hash:       frame.view.hashes[branch],
				baseHash:   baseHash,
				parentHash: frame.item.hash,
				nodeID:     childID,
				depth:      frame.item.depth + 1,
				branch:     branch,
				parent:     parent,
				node:       frame.view.children[branch],
			},
			proofStart: lane.proofs.count(),
		})
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

func (lane *backedWalkLane) rememberProof(hash [32]byte, depth int) {
	lane.proofs.add(hash, depth)
}

func (sm *SHAMap) finishBackedWalkFrame(
	lane *backedWalkLane,
	cache *FullBelowCache,
	generation uint32,
	full bool,
	stored bool,
	usedBase bool,
) {
	frame := &lane.stack[len(lane.stack)-1]
	if full {
		lane.proofs.truncate(frame.proofStart)
		lane.rememberProof(frame.item.hash, frame.item.depth)
		if stored && !usedBase {
			cacheFullBelow(cache, generation, frame.item.hash, frame.item.depth)
		}
		if stored && frame.item.parent != nil && frame.item.node != nil {
			sm.releaseChild(frame.item.parent, frame.item.branch, frame.item.node)
		}
	}
	finishBackedWalkFrame(lane, full, stored, usedBase)
}

func finishBackedWalkFrame(lane *backedWalkLane, full, stored, usedBase bool) {
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
	parent.usedBase = parent.usedBase || usedBase
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

func (sm *SHAMap) loadTraversalNode(ctx context.Context, access *familyAccess, root *innerNode, item backedWalkItem) (traversalNode, bool, bool, error) {
	sm.acquisition.stats.durableReads.Add(1)
	data, stored, err := access.fetchPreferDurable(ctx, item.hash)
	if err != nil {
		return traversalNode{}, false, false, err
	}
	if len(data) != 0 {
		sm.backing.loads.Add(1)
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

func (sm *SHAMap) loadVerifiedBaseTraversalNode(
	ctx context.Context,
	access *familyAccess,
	hash [32]byte,
) (traversalNode, error) {
	sm.acquisition.stats.durableReads.Add(1)
	data, err := access.fetchDurable(ctx, hash)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return traversalNode{}, ctxErr
		}
		return traversalNode{}, fmt.Errorf("%w: read %x: %v", errVerifiedBaseUnavailable, hash[:8], err)
	}
	if len(data) == 0 {
		return traversalNode{}, fmt.Errorf("%w: node %x is missing", errVerifiedBaseUnavailable, hash[:8])
	}
	view, err := decodeTraversalNode(data, hash)
	if err != nil {
		return traversalNode{}, fmt.Errorf("%w: node %x: %v", errVerifiedBaseUnavailable, hash[:8], err)
	}
	return view, nil
}

func attachedNode(root *innerNode, nodeID NodeID) mapNode {
	if root == nil {
		return nil
	}
	current := mapNode(root)
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

func traversalNodeFromNode(node mapNode) traversalNode {
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
	body, err := decodePrefixBody(data)
	if err != nil {
		return traversalNode{}, fmt.Errorf("%w: %v", ErrInvalidNodeData, err)
	}
	view := traversalNode{}
	switch body.kind {
	case storedInner:
		view.inner = true
		view.branches, err = decodeInnerBranches(body, &view.hashes)
		if err != nil {
			return traversalNode{}, fmt.Errorf("%w: %v", ErrInvalidNodeData, err)
		}
	case storedAccountState:
		if len(body.payload) < 12 {
			return traversalNode{}, fmt.Errorf("%w: account-state leaf too short: %d", ErrInvalidNodeData, len(data))
		}
	case storedTransaction:
		if len(body.payload) < 12 {
			return traversalNode{}, fmt.Errorf("%w: transaction leaf too short: %d", ErrInvalidNodeData, len(data))
		}
	case storedTransactionWithMeta:
		if len(body.payload) < 12 {
			return traversalNode{}, fmt.Errorf("%w: transaction-with-metadata leaf too short: %d", ErrInvalidNodeData, len(data))
		}
	}
	digest := sha512.Sum512(data)
	var actual [32]byte
	copy(actual[:], digest[:32])
	if actual != expected {
		return traversalNode{}, fmt.Errorf("%w: expected %x, got %x", ErrInvalidNodeData, expected[:8], actual[:8])
	}
	return view, nil
}
