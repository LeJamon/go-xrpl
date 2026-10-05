package shamap

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"unsafe"
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
	RetainedNodes         uint64
	RetainedBytes         uint64
	RetainedPeakBytes     uint64
	RetentionPressure     bool
	BlockedItems          uint64
	BlockedCapacity       uint64
	ActiveFrames          uint64
	ActiveCapacity        uint64
	ProofEntries          uint64
	ProofCapacity         uint64
	CursorBytes           uint64
	CursorPeakBytes       uint64
}

type backedWalkStats struct {
	equalSubtreesSkipped  atomic.Uint64
	nodesDescended        atomic.Uint64
	durableReads          atomic.Uint64
	missingNodes          atomic.Uint64
	verifiedBaseFallbacks atomic.Uint64
	blockedItems          atomic.Uint64
	blockedCapacity       atomic.Uint64
	activeFrames          atomic.Uint64
	activeCapacity        atomic.Uint64
	proofEntries          atomic.Uint64
	proofCapacity         atomic.Uint64
	cursorBytes           atomic.Uint64
	cursorPeakBytes       atomic.Uint64
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

func traversalBudgetHasAtMost(ctx context.Context, limit int64) bool {
	budget, ok := ctx.Value(traversalBudgetKey{}).(*traversalBudget)
	return ok && budget.remaining.Load() <= limit
}

func hasTraversalBudget(ctx context.Context) bool {
	_, ok := ctx.Value(traversalBudgetKey{}).(*traversalBudget)
	return ok
}

type backedWalkProof struct {
	hash  [32]byte
	depth uint8
}

type backedWalkProofs struct {
	chunks [][]backedWalkProof
	size   int
}

type backedWalkPending struct {
	hash       [32]byte
	baseHash   [32]byte
	parentHash [32]byte
	nodeID     NodeID
}

type backedWalkPendingQueue struct {
	head   *backedWalkPendingChunk
	tail   *backedWalkPendingChunk
	length int
}

type backedWalkPendingChunk struct {
	items [backedWalkPendingChunkSize]backedWalkPending
	start uint8
	end   uint8
	next  *backedWalkPendingChunk
}

const backedWalkPendingChunkSize = 16

const (
	backedWalkFrameBytes        = uint64(unsafe.Sizeof(backedWalkFrame{}))
	backedWalkPendingChunkBytes = uint64(unsafe.Sizeof(backedWalkPendingChunk{}))
	backedWalkProofBytes        = uint64(unsafe.Sizeof(backedWalkProof{}))
)

func (proofs *backedWalkProofs) add(hash [32]byte, depth int) {
	if len(proofs.chunks) == 0 || len(proofs.chunks[len(proofs.chunks)-1]) == backedWalkProofChunkSize {
		proofs.chunks = append(proofs.chunks, make([]backedWalkProof, 0, backedWalkProofChunkSize))
	}
	last := len(proofs.chunks) - 1
	proofs.chunks[last] = append(proofs.chunks[last], backedWalkProof{hash: hash, depth: uint8(depth)})
	proofs.size++
}

func (proofs *backedWalkProofs) addWithBudget(hash [32]byte, depth int, reserve func(uint64) bool) error {
	if len(proofs.chunks) == 0 || len(proofs.chunks[len(proofs.chunks)-1]) == backedWalkProofChunkSize {
		if reserve != nil && !reserve(uint64(backedWalkProofChunkSize)*backedWalkProofBytes) {
			return ErrAcquisitionMemory
		}
		proofs.chunks = append(proofs.chunks, make([]backedWalkProof, 0, backedWalkProofChunkSize))
	}
	last := len(proofs.chunks) - 1
	proofs.chunks[last] = append(proofs.chunks[last], backedWalkProof{hash: hash, depth: uint8(depth)})
	proofs.size++
	return nil
}

func (proofs backedWalkProofs) count() int {
	return proofs.size
}

func (proofs *backedWalkProofs) truncate(size int) {
	proofs.truncateWithRelease(size, nil)
}

func (proofs *backedWalkProofs) truncateWithRelease(size int, release func(uint64)) {
	if size >= proofs.size {
		return
	}
	if size <= 0 {
		for i := range proofs.chunks {
			if release != nil {
				release(uint64(cap(proofs.chunks[i])) * backedWalkProofBytes)
			}
			proofs.chunks[i] = nil
		}
		proofs.chunks = nil
		proofs.size = 0
		return
	}
	keepChunks := (size + backedWalkProofChunkSize - 1) / backedWalkProofChunkSize
	for i := keepChunks; i < len(proofs.chunks); i++ {
		if release != nil {
			release(uint64(cap(proofs.chunks[i])) * backedWalkProofBytes)
		}
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

func (proofs *backedWalkProofs) clearWithRelease(release func(uint64)) {
	proofs.truncateWithRelease(0, release)
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

func pendingBackedWalkItem(root *innerNode, pending backedWalkPending) backedWalkItem {
	depth := int(pending.nodeID.Depth())
	branch := 0
	if depth > 0 {
		branch = selectBranchForPath(pending.nodeID.ID(), depth-1)
	}
	var parent *innerNode
	if depth > 1 {
		parentNodeID := pending.nodeID
		parentNodeID.depth--
		parent, _ = attachedNode(root, parentNodeID).(*innerNode)
	} else if depth == 1 {
		parent = root
	}
	return backedWalkItem{
		hash:       pending.hash,
		baseHash:   pending.baseHash,
		parentHash: pending.parentHash,
		nodeID:     pending.nodeID,
		depth:      depth,
		branch:     branch,
		parent:     parent,
		node:       attachedNode(root, pending.nodeID),
	}
}

func newBackedWalkPending(item backedWalkItem) backedWalkPending {
	return backedWalkPending{
		hash:       item.hash,
		baseHash:   item.baseHash,
		parentHash: item.parentHash,
		nodeID:     item.nodeID,
	}
}

func (queue *backedWalkPendingQueue) len() int {
	return queue.length
}

func (queue *backedWalkPendingQueue) append(item backedWalkPending, reserve func(uint64) bool) error {
	if queue.tail == nil || int(queue.tail.end) == backedWalkPendingChunkSize {
		if reserve != nil && !reserve(backedWalkPendingChunkBytes) {
			return ErrAcquisitionMemory
		}
		chunk := &backedWalkPendingChunk{}
		if queue.tail == nil {
			queue.head = chunk
		} else {
			queue.tail.next = chunk
		}
		queue.tail = chunk
	}
	queue.tail.items[queue.tail.end] = item
	queue.tail.end++
	queue.length++
	return nil
}

func (queue *backedWalkPendingQueue) peek() (backedWalkPending, bool) {
	if queue.head == nil {
		return backedWalkPending{}, false
	}
	return queue.head.items[queue.head.start], true
}

func (queue *backedWalkPendingQueue) pop(release func(uint64)) (backedWalkPending, bool) {
	if queue.head == nil {
		return backedWalkPending{}, false
	}
	chunk := queue.head
	item := chunk.items[chunk.start]
	chunk.items[chunk.start] = backedWalkPending{}
	chunk.start++
	queue.length--
	if chunk.start == chunk.end {
		queue.head = chunk.next
		if queue.head == nil {
			queue.tail = nil
		}
		if release != nil {
			release(backedWalkPendingChunkBytes)
		}
	}
	return item, true
}

func (queue *backedWalkPendingQueue) clear(release func(uint64)) {
	for chunk := queue.head; chunk != nil; {
		next := chunk.next
		if release != nil {
			release(backedWalkPendingChunkBytes)
		}
		chunk = next
	}
	queue.head = nil
	queue.tail = nil
	queue.length = 0
}

func (queue *backedWalkPendingQueue) capacity() int {
	chunks := 0
	for chunk := queue.head; chunk != nil; chunk = chunk.next {
		chunks++
	}
	return chunks * backedWalkPendingChunkSize
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
	retry      bool
}

type backedWalkLane struct {
	root        backedWalkItem
	stack       []backedWalkFrame
	pending     backedWalkPendingQueue
	proofs      backedWalkProofs
	complete    bool
	durable     bool
	passDurable bool
	started     bool
	retryTurn   bool
	cursor      *backedWalkCursor
}

type backedWalkCursor struct {
	generation uint32
	rootHash   [32]byte
	baseRoot   [32]byte
	budget     *AcquisitionBudget
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
		return fmt.Errorf("%w: %w", errVerifiedBaseUnavailable, &MissingNodeError{Hash: rootHash})
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
		RetainedNodes:         sm.acquisition.retained.nodes.Load(),
		RetainedBytes:         sm.acquisition.retained.bytes.Load(),
		RetainedPeakBytes:     sm.acquisition.retained.peak.Load(),
		RetentionPressure:     sm.acquisition.retained.pressure.Load(),
		BlockedItems:          sm.acquisition.stats.blockedItems.Load(),
		BlockedCapacity:       sm.acquisition.stats.blockedCapacity.Load(),
		ActiveFrames:          sm.acquisition.stats.activeFrames.Load(),
		ActiveCapacity:        sm.acquisition.stats.activeCapacity.Load(),
		ProofEntries:          sm.acquisition.stats.proofEntries.Load(),
		ProofCapacity:         sm.acquisition.stats.proofCapacity.Load(),
		CursorBytes:           sm.acquisition.stats.cursorBytes.Load(),
		CursorPeakBytes:       sm.acquisition.stats.cursorPeakBytes.Load(),
	}
}

func (sm *SHAMap) reserveCursorBytes(cursor *backedWalkCursor, bytes uint64) bool {
	if cursor == nil || cursor.budget == nil || bytes == 0 {
		return true
	}
	if !cursor.budget.tryReserveCursor(bytes) {
		return false
	}
	current := sm.acquisition.stats.cursorBytes.Add(bytes)
	for {
		peak := sm.acquisition.stats.cursorPeakBytes.Load()
		if current <= peak || sm.acquisition.stats.cursorPeakBytes.CompareAndSwap(peak, current) {
			break
		}
	}
	return true
}

func (sm *SHAMap) releaseCursorBytes(cursor *backedWalkCursor, bytes uint64) {
	if cursor == nil || cursor.budget == nil || bytes == 0 {
		return
	}
	for {
		used := sm.acquisition.stats.cursorBytes.Load()
		if bytes > used {
			panic("shamap: acquisition cursor release exceeds reservation")
		}
		if sm.acquisition.stats.cursorBytes.CompareAndSwap(used, used-bytes) {
			cursor.budget.releaseCursor(bytes)
			return
		}
	}
}

func (sm *SHAMap) updateCursorCapacityStats(cursor *backedWalkCursor) {
	if cursor == nil {
		return
	}
	var blocked, active, proofs uint64
	var blockedCapacity, activeCapacity, proofCapacity uint64
	for i := range cursor.lanes {
		lane := &cursor.lanes[i]
		blocked += uint64(lane.pending.len())
		blockedCapacity += uint64(lane.pending.capacity())
		active += uint64(len(lane.stack))
		activeCapacity += uint64(cap(lane.stack))
		proofs += uint64(lane.proofs.count())
		for _, chunk := range lane.proofs.chunks {
			proofCapacity += uint64(cap(chunk))
		}
	}
	sm.acquisition.stats.blockedItems.Store(blocked)
	sm.acquisition.stats.blockedCapacity.Store(blockedCapacity)
	sm.acquisition.stats.activeFrames.Store(active)
	sm.acquisition.stats.activeCapacity.Store(activeCapacity)
	sm.acquisition.stats.proofEntries.Store(proofs)
	sm.acquisition.stats.proofCapacity.Store(proofCapacity)
}

func (sm *SHAMap) releaseBackedWalkCursorLocked() {
	cursor := sm.acquisition.cursor
	if cursor == nil {
		return
	}
	for i := range cursor.lanes {
		lane := &cursor.lanes[i]
		lane.pending.clear(func(bytes uint64) { sm.releaseCursorBytes(cursor, bytes) })
		if capacity := cap(lane.stack); capacity != 0 {
			for j := range lane.stack {
				lane.stack[j] = backedWalkFrame{}
			}
			lane.stack = nil
			sm.releaseCursorBytes(cursor, uint64(capacity)*backedWalkFrameBytes)
		}
		lane.proofs.clearWithRelease(func(bytes uint64) { sm.releaseCursorBytes(cursor, bytes) })
		lane.cursor = nil
	}
	sm.updateCursorCapacityStats(cursor)
	sm.acquisition.cursor = nil
	sm.acquisition.base = nil
}

// clearBackedWalkNodeReferencesLocked drops pointer-bearing traversal state
// while retaining hashes, paths, branch cursors, and proof metadata needed to
// resume the walk. The caller must hold acquisition.walkMu. A later traversal
// reloads any detached node from the backing family before descending it.
func (sm *SHAMap) clearBackedWalkNodeReferencesLocked() {
	cursor := sm.acquisition.cursor
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
		sm.releaseBackedWalkCursorLocked()
		cursor, err := newBackedWalkCursor(root, rootHash, gen, base, sm.acquisition.budget)
		if err != nil {
			return nil, err
		}
		sm.acquisition.cursor = cursor
	}
	cursor := sm.acquisition.cursor
	sm.updateCursorCapacityStats(cursor)

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
			if err := sm.walkBackedLane(ctx, access, cache, gen, root, cursor, lane, filter, &stop, report); err != nil {
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
		if (errors.Is(firstErr, ErrTraversalBudget) || errors.Is(firstErr, ErrAcquisitionMemory)) && len(missing) != 0 {
			return missing, nil
		}
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
	sm.updateCursorCapacityStats(cursor)
	if !complete && len(missing) == 0 {
		return nil, ErrTraversalBudget
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

func newBackedWalkCursor(root *innerNode, rootHash [32]byte, gen uint32, base *verifiedWalkBase, budget *AcquisitionBudget) (*backedWalkCursor, error) {
	cursor := &backedWalkCursor{generation: gen, rootHash: rootHash, budget: budget}
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
		cursor.lanes[branch].cursor = cursor
		cursor.lanes[branch].passDurable = true
	}
	return cursor, nil
}

func (sm *SHAMap) pushBackedWalkFrame(cursor *backedWalkCursor, lane *backedWalkLane, frame backedWalkFrame) error {
	if len(lane.stack) == cap(lane.stack) {
		if !sm.reserveCursorBytes(cursor, backedWalkFrameBytes) {
			return ErrAcquisitionMemory
		}
		stack := make([]backedWalkFrame, len(lane.stack), cap(lane.stack)+1)
		copy(stack, lane.stack)
		lane.stack = stack
	}
	lane.stack = append(lane.stack, frame)
	return nil
}

func (sm *SHAMap) popBackedWalkFrame(cursor *backedWalkCursor, lane *backedWalkLane) {
	if len(lane.stack) == 0 {
		return
	}
	last := len(lane.stack) - 1
	lane.stack[last] = backedWalkFrame{}
	lane.stack = lane.stack[:last]
	if len(lane.stack) == 0 {
		capacity := cap(lane.stack)
		lane.stack = nil
		sm.releaseCursorBytes(cursor, uint64(capacity)*backedWalkFrameBytes)
	}
}

func (sm *SHAMap) walkBackedLane(
	ctx context.Context,
	access *familyAccess,
	cache *FullBelowCache,
	gen uint32,
	root *innerNode,
	cursor *backedWalkCursor,
	lane *backedWalkLane,
	filter SyncFilter,
	stop *atomic.Bool,
	report func(backedWalkItem),
) error {
	lane.complete = false
	retries := lane.pending.len()
	preferRetry := lane.retryTurn
	lane.retryTurn = false
	if preferRetry && len(lane.stack) != 0 && traversalBudgetHasAtMost(ctx, 1) {
		preferRetry = false
	}
	if preferRetry {
		hadActive := len(lane.stack) != 0
		for retries > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			if stop.Load() {
				return nil
			}
			pending, ok := lane.pending.peek()
			if !ok {
				break
			}
			if err := sm.pushBackedWalkFrame(cursor, lane, backedWalkFrame{
				item: pendingBackedWalkItem(root, pending), topLevel: true, retry: true, proofStart: lane.proofs.count(),
			}); err != nil {
				return err
			}
			lane.pending.pop(func(bytes uint64) { sm.releaseCursorBytes(cursor, bytes) })
			retries--
			if err := sm.walkBackedLaneStack(ctx, access, cache, gen, root, cursor, lane, filter, stop, report, hadActive && hasTraversalBudget(ctx)); err != nil {
				return err
			}
		}
	}

	if !lane.started && len(lane.stack) == 0 && lane.pending.len() == 0 && lane.root.hash != ([32]byte{}) {
		lane.started = true
		if err := sm.pushBackedWalkFrame(cursor, lane, backedWalkFrame{
			item: lane.root, topLevel: true, proofStart: lane.proofs.count(),
		}); err != nil {
			return err
		}
	}
	if err := sm.walkBackedLaneStack(ctx, access, cache, gen, root, cursor, lane, filter, stop, report, false); err != nil {
		return err
	}
	if len(lane.stack) == 0 && lane.pending.len() == 0 {
		lane.complete = true
		lane.durable = lane.passDurable
	} else {
		lane.complete = false
		lane.durable = false
		lane.retryTurn = lane.pending.len() != 0
	}
	return nil
}

func (sm *SHAMap) walkBackedLaneStack(
	ctx context.Context,
	access *familyAccess,
	cache *FullBelowCache,
	gen uint32,
	root *innerNode,
	cursor *backedWalkCursor,
	lane *backedWalkLane,
	filter SyncFilter,
	stop *atomic.Bool,
	report func(backedWalkItem),
	suppressRetryReport bool,
) error {
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
				if err := sm.finishBackedWalkFrame(lane, cache, gen, true, true, true); err != nil {
					return err
				}
				continue
			}
			if cache.Has(gen, frame.item.hash) {
				if err := sm.finishBackedWalkFrame(lane, cache, gen, true, true, false); err != nil {
					return err
				}
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
				pending := newBackedWalkPending(frame.item)
				if err := lane.pending.append(pending, func(bytes uint64) bool {
					return sm.reserveCursorBytes(cursor, bytes)
				}); err != nil {
					return err
				}
				if filter.ShouldFetch(frame.item.hash) && !(frame.retry && suppressRetryReport) {
					report(frame.item)
				}
				if err := sm.finishBackedWalkFrame(lane, cache, gen, false, false, false); err != nil {
					return err
				}
				continue
			}
			if !view.inner {
				if err := sm.finishBackedWalkFrame(lane, cache, gen, true, stored, false); err != nil {
					return err
				}
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
			if err := sm.finishBackedWalkFrame(lane, cache, gen, full, stored, usedBase); err != nil {
				return err
			}
			continue
		}

		branch := frame.nextBranch
		childID, err := frame.item.nodeID.childNodeID(uint8(branch))
		if err != nil {
			return err
		}
		var parent *innerNode
		if attached, ok := frame.item.node.(*innerNode); ok {
			parent = attached
		}
		baseHash := [32]byte{}
		if frame.baseView.inner && frame.baseView.branches&(1<<branch) != 0 {
			baseHash = frame.baseView.hashes[branch]
		}
		childFrame := backedWalkFrame{
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
		}
		frame.nextBranch++
		if err := sm.pushBackedWalkFrame(cursor, lane, childFrame); err != nil {
			lane.stack[len(lane.stack)-1].nextBranch--
			return err
		}
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
) error {
	frame := &lane.stack[len(lane.stack)-1]
	if full {
		lane.proofs.truncateWithRelease(frame.proofStart, func(bytes uint64) {
			sm.releaseCursorBytes(lane.cursor, bytes)
		})
		if err := lane.proofs.addWithBudget(frame.item.hash, frame.item.depth, func(bytes uint64) bool {
			return sm.reserveCursorBytes(lane.cursor, bytes)
		}); err != nil {
			return err
		}
		if stored && !usedBase {
			cacheFullBelow(cache, generation, frame.item.hash, frame.item.depth)
		}
		if stored && frame.item.parent != nil && frame.item.node != nil {
			sm.releaseChild(frame.item.parent, frame.item.branch, frame.item.node)
		}
	}
	finishBackedWalkFrame(lane, full, stored, usedBase)
	if len(lane.stack) == 0 && lane.cursor != nil {
		capacity := cap(lane.stack)
		lane.stack = nil
		sm.releaseCursorBytes(lane.cursor, uint64(capacity)*backedWalkFrameBytes)
	}
	return nil
}

func finishBackedWalkFrame(lane *backedWalkLane, full, stored, usedBase bool) {
	last := len(lane.stack) - 1
	topLevel := lane.stack[last].topLevel
	lane.stack[last] = backedWalkFrame{}
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
		return traversalNode{}, fmt.Errorf("%w: %w", errVerifiedBaseUnavailable, &MissingNodeError{Hash: hash})
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
