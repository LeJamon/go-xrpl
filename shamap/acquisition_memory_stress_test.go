package shamap

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	issue2043FixtureLeaves       = 8_192
	issue2043RequestLimit        = 32
	issue2043TraversalVisits     = 512
	issue2043BudgetBytes         = 16 << 20
	issue2043OutstandingLimit    = 128
	issue2043StageBatch          = 16
	issue2043DiscoveryCallsStage = 4
	issue2043HeapSampleEvery     = 256
	issue2043HotBranch           = 0
	issue2043ColdBranch          = BranchFactor - 1
	issue2043StressEnabled       = "GOXRPL_ISSUE_2043_STRESS"
	issue2043StressNodeCount     = "GOXRPL_ISSUE_2043_STRESS_NODES"
	issue2043StressDirectory     = "GOXRPL_ISSUE_2043_STRESS_DIR"
	issue2043StressKeepFixture   = "GOXRPL_ISSUE_2043_STRESS_KEEP"
	issue2043StressDefaultNodes  = 1_000_000
)

type issue2043Backing interface {
	Family
	FullBelowCache() *FullBelowCache

	hold([32]byte)
	release([32]byte)
	wire([32]byte) ([]byte, error)
	readCount() uint64
	resetReadCount()
	close() error
}

type issue2043Fixture struct {
	rootHash       [32]byte
	rootData       []byte
	family         issue2043Backing
	withheld       int
	withheldHashes [][32]byte
	hotLeaves      int
	coldLeaves     int
}

// issue2043HeldMemoryFamily models a durable store containing all nodes while
// making a selected frontier unavailable to traversal. Keeping the bytes in
// the store, rather than deleting them, lets staged arrivals exercise the same
// persistence path as a peer reply followed by a store acknowledgement.
type issue2043HeldMemoryFamily struct {
	base     *memoryFamily
	mu       sync.RWMutex
	withheld map[[32]byte]struct{}
	reads    atomic.Uint64
}

func (f *issue2043HeldMemoryFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	f.reads.Add(1)
	if f.isHeld(hash) {
		return nil, nil
	}
	return f.base.Fetch(ctx, hash)
}

func (f *issue2043HeldMemoryFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.Fetch(ctx, hash)
}

func (f *issue2043HeldMemoryFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.base.StoreBatch(ctx, entries)
}

func (f *issue2043HeldMemoryFamily) FullBelowCache() *FullBelowCache {
	return f.base.FullBelowCache()
}

func (f *issue2043HeldMemoryFamily) hold(hash [32]byte) {
	f.mu.Lock()
	f.withheld[hash] = struct{}{}
	f.mu.Unlock()
}

func (f *issue2043HeldMemoryFamily) release(hash [32]byte) {
	f.mu.Lock()
	delete(f.withheld, hash)
	f.mu.Unlock()
}

func (f *issue2043HeldMemoryFamily) isHeld(hash [32]byte) bool {
	f.mu.RLock()
	_, held := f.withheld[hash]
	f.mu.RUnlock()
	return held
}

func (f *issue2043HeldMemoryFamily) wire(hash [32]byte) ([]byte, error) {
	f.base.mu.RLock()
	data := append([]byte(nil), f.base.store[hash]...)
	f.base.mu.RUnlock()
	if len(data) == 0 {
		return nil, fmt.Errorf("node %x is absent from fixture", hash[:8])
	}
	node, err := deserializeFromPrefix(data)
	if err != nil {
		return nil, err
	}
	return node.SerializeForWire()
}

func (f *issue2043HeldMemoryFamily) readCount() uint64 {
	return f.reads.Load()
}

func (f *issue2043HeldMemoryFamily) resetReadCount() { f.reads.Store(0) }

func (f *issue2043HeldMemoryFamily) close() error { return nil }

// issue2043DiskFamily stores serialized nodes in an append-only file and keeps
// only hash-to-offset metadata in memory. It is used by the opt-in million
// node test so source tree and node payloads are not retained while measuring
// the acquisition cursor.
type issue2043DiskFamily struct {
	mu       sync.RWMutex
	file     *os.File
	path     string
	index    map[[32]byte]issue2043DiskRecord
	withheld map[[32]byte]struct{}
	cache    *FullBelowCache
	reads    atomic.Uint64
}

type issue2043DiskRecord struct {
	offset int64
	size   int
}

func newIssue2043DiskFamily(t *testing.T) *issue2043DiskFamily {
	t.Helper()
	directory := os.Getenv(issue2043StressDirectory)
	if directory == "" {
		directory = t.TempDir()
	}
	file, err := os.CreateTemp(directory, "issue-2043-shamap-*.nodes")
	if err != nil {
		t.Fatalf("create disk-backed fixture: %v", err)
	}
	family := &issue2043DiskFamily{
		file:     file,
		path:     file.Name(),
		index:    make(map[[32]byte]issue2043DiskRecord),
		withheld: make(map[[32]byte]struct{}),
		cache:    NewFullBelowCache(),
	}
	t.Cleanup(func() {
		if err := family.close(); err != nil {
			t.Errorf("close disk-backed fixture: %v", err)
		}
		if os.Getenv(issue2043StressKeepFixture) != "1" {
			if err := os.Remove(family.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("remove disk-backed fixture: %v", err)
			}
		}
	})
	return family
}

func (f *issue2043DiskFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.reads.Add(1)
	f.mu.RLock()
	_, held := f.withheld[hash]
	record, found := f.index[hash]
	f.mu.RUnlock()
	if held || !found {
		return nil, nil
	}
	data := make([]byte, record.size)
	if _, err := f.file.ReadAt(data, record.offset); err != nil {
		return nil, err
	}
	return data, nil
}

func (f *issue2043DiskFamily) FetchDurable(ctx context.Context, hash [32]byte) ([]byte, error) {
	return f.Fetch(ctx, hash)
}

func (f *issue2043DiskFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range entries {
		offset, err := f.file.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		if _, err := f.file.Write(entry.Data); err != nil {
			return err
		}
		f.index[entry.Hash] = issue2043DiskRecord{offset: offset, size: len(entry.Data)}
	}
	return nil
}

func (f *issue2043DiskFamily) FullBelowCache() *FullBelowCache { return f.cache }

func (f *issue2043DiskFamily) hold(hash [32]byte) {
	f.mu.Lock()
	f.withheld[hash] = struct{}{}
	f.mu.Unlock()
}

func (f *issue2043DiskFamily) release(hash [32]byte) {
	f.mu.Lock()
	delete(f.withheld, hash)
	f.mu.Unlock()
}

func (f *issue2043DiskFamily) wire(hash [32]byte) ([]byte, error) {
	f.mu.RLock()
	record, found := f.index[hash]
	f.mu.RUnlock()
	if !found {
		return nil, fmt.Errorf("node %x is absent from fixture", hash[:8])
	}
	data := make([]byte, record.size)
	if _, err := f.file.ReadAt(data, record.offset); err != nil {
		return nil, err
	}
	node, err := deserializeFromPrefix(data)
	if err != nil {
		return nil, err
	}
	return node.SerializeForWire()
}

func (f *issue2043DiskFamily) readCount() uint64 { return f.reads.Load() }

func (f *issue2043DiskFamily) resetReadCount() { f.reads.Store(0) }

func (f *issue2043DiskFamily) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file == nil {
		return nil
	}
	err := f.file.Close()
	f.file = nil
	return err
}

func deterministicIssue2043State(t testing.TB, leaves int) *SHAMap {
	t.Helper()
	sm := New(TypeState)
	for i := 0; i < leaves; i++ {
		var seed [16]byte
		copy(seed[:], "issue-2043-leaf")
		binary.BigEndian.PutUint64(seed[8:], uint64(i)+1)
		key := sha256.Sum256(seed[:])
		if err := sm.Put(key, seed[:]); err != nil {
			t.Fatalf("put deterministic leaf %d: %v", i, err)
		}
	}
	return sm
}

func newIssue2043MemoryFixture(t testing.TB, leaves int) *issue2043Fixture {
	t.Helper()
	source := deterministicIssue2043State(t, leaves)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("hash fixture source: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize fixture root: %v", err)
	}
	entries, err := collectDirtyForTest(source)
	if err != nil {
		t.Fatalf("collect fixture nodes: %v", err)
	}
	family := &issue2043HeldMemoryFamily{
		base:     newMemoryFamily(),
		withheld: make(map[[32]byte]struct{}),
	}
	if err := family.StoreBatch(t.Context(), entries); err != nil {
		t.Fatalf("store fixture nodes: %v", err)
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk fixture nodes: %v", err)
	}
	withheld := 0
	withheldHashes := make([][32]byte, 0)
	hotLeaves, coldLeaves := 0, 0
	for _, wire := range wireNodes {
		node, err := deserializeNodeFromWire(wire.Data)
		if err != nil {
			t.Fatalf("decode fixture node: %v", err)
		}
		if _, ok := node.(mapLeaf); ok {
			nodeID, err := ParseNodeID(wire.NodeID)
			if err != nil {
				t.Fatalf("parse fixture leaf path: %v", err)
			}
			family.hold(node.Hash())
			withheldHashes = append(withheldHashes, node.Hash())
			switch issue2043RootBranch(nodeID) {
			case issue2043HotBranch:
				hotLeaves++
			case issue2043ColdBranch:
				coldLeaves++
			}
			withheld++
		}
	}
	return &issue2043Fixture{
		rootHash: rootHash, rootData: rootData, family: family,
		withheld: withheld, withheldHashes: withheldHashes,
		hotLeaves: hotLeaves, coldLeaves: coldLeaves,
	}
}

func newIssue2043DiskFixture(t *testing.T, leaves int) *issue2043Fixture {
	t.Helper()
	source := deterministicIssue2043State(t, leaves)
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("hash disk fixture source: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize disk fixture root: %v", err)
	}
	family := newIssue2043DiskFamily(t)
	withheld := 0
	withheldHashes := make([][32]byte, 0)
	hotLeaves, coldLeaves := 0, 0
	var write func(mapNode, NodeID) error
	write = func(node mapNode, nodeID NodeID) error {
		if node == nil {
			return nil
		}
		data, err := node.SerializeWithPrefix()
		if err != nil {
			return err
		}
		if err := family.StoreBatch(t.Context(), []FlushEntry{{Hash: node.Hash(), Data: data, MapType: TypeState}}); err != nil {
			return err
		}
		if _, ok := node.(mapLeaf); ok {
			family.hold(node.Hash())
			withheldHashes = append(withheldHashes, node.Hash())
			switch issue2043RootBranch(nodeID) {
			case issue2043HotBranch:
				hotLeaves++
			case issue2043ColdBranch:
				coldLeaves++
			}
			withheld++
			return nil
		}
		inner, ok := node.(*innerNode)
		if !ok {
			return nil
		}
		for branch := range BranchFactor {
			child, _, set := inner.LoadChild(branch)
			if set && child != nil {
				childID, err := nodeID.childNodeID(uint8(branch))
				if err != nil {
					return err
				}
				if err := write(child, childID); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := write(source.tree.root, newRootNodeID()); err != nil {
		t.Fatalf("write disk fixture nodes: %v", err)
	}
	return &issue2043Fixture{
		rootHash: rootHash, rootData: rootData, family: family,
		withheld: withheld, withheldHashes: withheldHashes,
		hotLeaves: hotLeaves, coldLeaves: coldLeaves,
	}
}

func (f *issue2043Fixture) newDestination(t testing.TB, budget *AcquisitionBudget) *SHAMap {
	t.Helper()
	dest, err := NewBacked(TypeState, f.family)
	if err != nil {
		t.Fatalf("new backed fixture destination: %v", err)
	}
	if err := dest.AddRootNode(f.rootHash, f.rootData); err != nil {
		t.Fatalf("add fixture root: %v", err)
	}
	issue2043AttachBudget(dest, budget)
	return dest
}

func (f *issue2043Fixture) releaseAll() {
	for _, hash := range f.withheldHashes {
		f.family.release(hash)
	}
}

func (f *issue2043Fixture) cloneMemory(t testing.TB) *issue2043Fixture {
	t.Helper()
	source, ok := f.family.(*issue2043HeldMemoryFamily)
	if !ok {
		t.Fatalf("fixture family %T is not cloneable memory storage", f.family)
	}
	base := newMemoryFamily()
	source.base.mu.RLock()
	for hash, data := range source.base.store {
		base.store[hash] = append([]byte(nil), data...)
	}
	source.base.mu.RUnlock()
	family := &issue2043HeldMemoryFamily{base: base, withheld: make(map[[32]byte]struct{}, len(f.withheldHashes))}
	for _, hash := range f.withheldHashes {
		family.hold(hash)
	}
	return &issue2043Fixture{
		rootHash: f.rootHash, rootData: append([]byte(nil), f.rootData...),
		family: family, withheld: f.withheld, withheldHashes: append([][32]byte(nil), f.withheldHashes...),
		hotLeaves: f.hotLeaves, coldLeaves: f.coldLeaves,
	}
}

func (f *issue2043Fixture) stage(t testing.TB, dest *SHAMap, missing MissingNode) bool {
	t.Helper()
	wire, err := f.family.wire(missing.Hash)
	if err != nil {
		t.Fatalf("load staged node %x: %v", missing.Hash[:8], err)
	}
	result, entry, err := dest.AddKnownNodeByIDWithEntryContext(t.Context(), missing.NodeID, wire)
	if err != nil {
		t.Fatalf("attach staged node %x: %v", missing.Hash[:8], err)
	}
	switch result {
	case NodeUseful:
		if err := f.family.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
			t.Fatalf("persist staged node %x: %v", missing.Hash[:8], err)
		}
		f.family.release(missing.Hash)
		return true
	case NodeDuplicate:
		f.family.release(missing.Hash)
		return true
	case NodeReRequest:
		return false
	default:
		t.Fatalf("stage node %x returned %v", missing.Hash[:8], result)
		return false
	}
}

func issue2043RootBranch(nodeID NodeID) int {
	return selectBranchForPath(nodeID.ID(), 0)
}

type issue2043Outstanding struct {
	items map[[32]byte]MissingNode
	order [][32]byte
}

func newIssue2043Outstanding(limit int) *issue2043Outstanding {
	return &issue2043Outstanding{
		items: make(map[[32]byte]MissingNode, limit),
		order: make([][32]byte, 0, limit),
	}
}

func (q *issue2043Outstanding) add(node MissingNode) bool {
	if _, exists := q.items[node.Hash]; exists {
		return false
	}
	q.items[node.Hash] = node
	q.order = append(q.order, node.Hash)
	return true
}

func (q *issue2043Outstanding) len() int { return len(q.order) }

func (q *issue2043Outstanding) take(limit int) []MissingNode {
	if limit <= 0 || len(q.order) == 0 {
		return nil
	}
	selected := make(map[[32]byte]struct{}, limit)
	batch := make([]MissingNode, 0, limit)
	pickBranch := func(branch int) {
		for _, hash := range q.order {
			if len(batch) == limit {
				return
			}
			if _, already := selected[hash]; already {
				continue
			}
			node := q.items[hash]
			if issue2043RootBranch(node.NodeID) != branch {
				continue
			}
			selected[hash] = struct{}{}
			batch = append(batch, node)
		}
	}
	pickBranch(issue2043HotBranch)
	pickBranch(issue2043ColdBranch)
	for _, hash := range q.order {
		if len(batch) == limit {
			break
		}
		if _, already := selected[hash]; already {
			continue
		}
		selected[hash] = struct{}{}
		batch = append(batch, q.items[hash])
	}
	for _, node := range batch {
		delete(q.items, node.Hash)
	}
	remaining := 0
	for _, hash := range q.order {
		if _, removed := selected[hash]; removed {
			continue
		}
		q.order[remaining] = hash
		remaining++
	}
	q.order = q.order[:remaining]
	return batch
}

type issue2043DriveOptions struct {
	stageBatch          int
	discoveryCallsStage int
	logProgress         bool
	measureHeap         bool
}

type issue2043RunStats struct {
	calls             uint64
	requested         uint64
	staged            uint64
	requestedBranches [BranchFactor]uint64
	stagedBranches    [BranchFactor]uint64
	peakOutstanding   int
	peakCursorBytes   uint64
	peakBlocked       uint64
	peakActive        uint64
	peakProofs        uint64
	budgetPeak        uint64
	cursorBudgetPeak  uint64
	heapBefore        uint64
	heapPeak          uint64
	heapRetainedPeak  uint64
	heapAfter         uint64
	heapSampledStaged uint64
	heapPressureSeen  bool
}

func issue2043HeapAlloc() uint64 {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

func issue2043ForceGCHeap() uint64 {
	runtime.GC()
	return issue2043HeapAlloc()
}

func issue2043Observe(t testing.TB, dest *SHAMap, budget *AcquisitionBudget, run *issue2043RunStats) {
	t.Helper()
	metrics := issue2043SnapshotMetrics(dest, budget)
	cursorLimit := metrics.limit / issue2043CursorBudgetFraction
	if metrics.enforceBudget && metrics.limit != 0 && cursorLimit != 0 {
		if metrics.cursorBytes > cursorLimit || metrics.cursorPeakBytes > cursorLimit {
			t.Fatalf("cursor accounting exceeded cap: current=%d peak=%d cap=%d metrics=%+v", metrics.cursorBytes, metrics.cursorPeakBytes, cursorLimit, metrics)
		}
		if metrics.cursorPeak > cursorLimit {
			t.Fatalf("budget cursor peak exceeded cap: peak=%d cap=%d metrics=%+v", metrics.cursorPeak, cursorLimit, metrics)
		}
	}
	if metrics.enforceBudget && (metrics.used > metrics.limit || metrics.peak > metrics.limit) {
		t.Fatalf("acquisition budget exceeded: %+v", metrics)
	}
	if metrics.cursorPeakBytes > run.peakCursorBytes {
		run.peakCursorBytes = metrics.cursorPeakBytes
	}
	if metrics.blockedCapacity > run.peakBlocked {
		run.peakBlocked = metrics.blockedCapacity
	}
	if metrics.activeCapacity > run.peakActive {
		run.peakActive = metrics.activeCapacity
	}
	if metrics.proofCapacity > run.peakProofs {
		run.peakProofs = metrics.proofCapacity
	}
	if metrics.peak > run.budgetPeak {
		run.budgetPeak = metrics.peak
	}
	if metrics.cursorPeak > run.cursorBudgetPeak {
		run.cursorBudgetPeak = metrics.cursorPeak
	}
}

const issue2043CursorBudgetFraction = 4

func issue2043CompleteWithBudget(t testing.TB, dest *SHAMap) {
	t.Helper()
	for pass := 0; pass < 1_000_000; pass++ {
		err := dest.FinishSyncContext(WithTraversalBudget(t.Context(), issue2043TraversalVisits))
		if err == nil {
			return
		}
		if errors.Is(err, ErrAcquisitionMemory) {
			if ackErr := dest.AcknowledgePersistedContext(t.Context()); ackErr != nil {
				t.Fatalf("finish sync memory checkpoint %d: %v", pass, ackErr)
			}
			continue
		}
		if errors.Is(err, ErrTraversalBudget) {
			continue
		}
		t.Fatalf("finish sync pass %d: %v", pass, err)
	}
	t.Fatal("finish sync did not converge within traversal budget")
}

func issue2043DriveInterleaved(t testing.TB, fixture *issue2043Fixture, dest *SHAMap, budget *AcquisitionBudget, options issue2043DriveOptions) issue2043RunStats {
	t.Helper()
	if options.stageBatch <= 0 {
		options.stageBatch = issue2043StageBatch
	}
	if options.discoveryCallsStage <= 0 {
		options.discoveryCallsStage = issue2043DiscoveryCallsStage
	}
	if fixture.hotLeaves == 0 || fixture.coldLeaves == 0 {
		t.Fatalf("fixture hot/cold lanes are empty: hot=%d cold=%d", fixture.hotLeaves, fixture.coldLeaves)
	}
	run := issue2043RunStats{}
	if options.measureHeap {
		run.heapBefore = issue2043ForceGCHeap()
		run.heapPeak = run.heapBefore
		run.heapRetainedPeak = run.heapBefore
	}
	outstanding := newIssue2043Outstanding(issue2043OutstandingLimit)
	heapSampleEvery := uint64(max(issue2043HeapSampleEvery, fixture.withheld/128))
	noProgress := 0
	callsSinceStage := 0
	maxPasses := fixture.withheld*16 + 1_024
	for pass := 0; pass < maxPasses; pass++ {
		stage := func(releaseNodes bool) int {
			batch := outstanding.take(options.stageBatch)
			staged := 0
			for _, node := range batch {
				if fixture.stage(t, dest, node) {
					staged++
					run.staged++
					branch := issue2043RootBranch(node.NodeID)
					run.stagedBranches[branch]++
				} else {
					if outstanding.len() >= issue2043OutstandingLimit {
						t.Fatalf("requeueing node %x exceeded outstanding reply limit", node.Hash[:8])
					}
					outstanding.add(node)
				}
			}
			if staged != 0 || releaseNodes {
				var err error
				if releaseNodes {
					err = dest.AcknowledgePersistedAndReleaseContext(t.Context())
				} else {
					err = dest.AcknowledgePersistedContext(t.Context())
				}
				if err != nil {
					t.Fatalf("checkpoint after %d staged replies: %v", run.staged, err)
				}
				if run.staged < uint64(fixture.withheld) {
					cache := fixture.family.FullBelowCache()
					if cache.Has(cache.Generation(), fixture.rootHash) {
						t.Fatalf("incomplete checkpoint published root after %d/%d staged replies", run.staged, fixture.withheld)
					}
				}
				if options.measureHeap && ((releaseNodes && !run.heapPressureSeen) || run.staged-run.heapSampledStaged >= heapSampleEvery) {
					heap := issue2043ForceGCHeap()
					if heap > run.heapRetainedPeak {
						run.heapRetainedPeak = heap
					}
					run.heapSampledStaged = run.staged
					run.heapPressureSeen = run.heapPressureSeen || releaseNodes
				}
			}
			return staged
		}

		for outstanding.len() > issue2043OutstandingLimit-issue2043RequestLimit {
			if stage(false) == 0 {
				t.Fatalf("outstanding replies could not drain below admission limit: %d", outstanding.len())
			}
			callsSinceStage = 0
		}
		beforeWalk := dest.BackedWalkStats()
		missing, walkErr := dest.GetMissingNodesContext(WithTraversalBudget(t.Context(), issue2043TraversalVisits), issue2043RequestLimit, nil)
		run.calls++
		if walkErr != nil && !errors.Is(walkErr, ErrTraversalBudget) && !errors.Is(walkErr, ErrAcquisitionMemory) {
			t.Fatalf("frontier pass %d: %v", pass, walkErr)
		}
		if len(missing) > issue2043RequestLimit {
			t.Fatalf("frontier pass %d returned %d requests, want at most %d", pass, len(missing), issue2043RequestLimit)
		}
		newRequests := 0
		for _, node := range missing {
			if _, exists := outstanding.items[node.Hash]; exists {
				continue
			}
			if outstanding.len() >= issue2043OutstandingLimit {
				t.Fatalf("frontier pass %d exceeded outstanding reply limit", pass)
			}
			outstanding.add(node)
			newRequests++
			run.requested++
			run.requestedBranches[issue2043RootBranch(node.NodeID)]++
		}
		if outstanding.len() > run.peakOutstanding {
			run.peakOutstanding = outstanding.len()
		}
		callsSinceStage++
		forceStage := errors.Is(walkErr, ErrAcquisitionMemory) || outstanding.len() >= issue2043OutstandingLimit || callsSinceStage >= options.discoveryCallsStage
		staged := 0
		if forceStage {
			staged = stage(errors.Is(walkErr, ErrAcquisitionMemory))
			callsSinceStage = 0
		}
		issue2043Observe(t, dest, budget, &run)
		if options.measureHeap {
			if heap := issue2043HeapAlloc(); heap > run.heapPeak {
				run.heapPeak = heap
			}
		}
		if options.logProgress && pass%256 == 0 {
			metrics := issue2043SnapshotMetrics(dest, budget)
			t.Logf("issue2043 pass=%d requested=%d staged=%d outstanding=%d cursor=%d cursor_peak=%d budget_peak=%d reads=%d", pass, run.requested, run.staged, outstanding.len(), metrics.cursorBytes, run.peakCursorBytes, run.budgetPeak, fixture.family.readCount())
		}
		afterWalk := dest.BackedWalkStats()
		if newRequests == 0 && staged == 0 && afterWalk.BlockedItems >= beforeWalk.BlockedItems && afterWalk.ActiveFrames >= beforeWalk.ActiveFrames {
			noProgress++
		} else {
			noProgress = 0
		}
		if noProgress > 32 {
			t.Fatalf("frontier discovery made no progress at pass %d: requested=%d staged=%d outstanding=%d walk_error=%v cursor=%+v", pass, run.requested, run.staged, outstanding.len(), walkErr, afterWalk)
		}
		if run.staged == uint64(fixture.withheld) && outstanding.len() == 0 {
			issue2043CompleteWithBudget(t, dest)
			break
		}
	}
	if run.staged != uint64(fixture.withheld) || outstanding.len() != 0 {
		t.Fatalf("interleaved acquisition did not drain: staged=%d/%d outstanding=%d", run.staged, fixture.withheld, outstanding.len())
	}
	issue2043Observe(t, dest, budget, &run)
	if options.measureHeap {
		run.heapAfter = issue2043ForceGCHeap()
		if run.heapAfter > run.heapRetainedPeak {
			run.heapRetainedPeak = run.heapAfter
		}
	}
	return run
}

func TestIssue2043BroadMissingFrontierStagesToExactRoot(t *testing.T) {
	fixture := newIssue2043MemoryFixture(t, issue2043FixtureLeaves)
	fixtureHeap := issue2043ForceGCHeap()
	budget := issue2043NewBudget(issue2043BudgetBytes)
	dest := fixture.newDestination(t, budget)
	run := issue2043DriveInterleaved(t, fixture, dest, budget, issue2043DriveOptions{
		logProgress: true,
		measureHeap: true,
	})
	if run.peakOutstanding < issue2043RequestLimit*2 {
		t.Fatalf("frontier never became broad: peak outstanding=%d, want at least %d", run.peakOutstanding, issue2043RequestLimit*2)
	}
	if run.requestedBranches[issue2043HotBranch] == 0 || run.requestedBranches[issue2043ColdBranch] == 0 {
		t.Fatalf("hot/cold request progress missing: hot=%d cold=%d", run.requestedBranches[issue2043HotBranch], run.requestedBranches[issue2043ColdBranch])
	}
	if run.stagedBranches[issue2043HotBranch] == 0 || run.stagedBranches[issue2043ColdBranch] == 0 {
		t.Fatalf("hot/cold staged progress missing: hot=%d cold=%d", run.stagedBranches[issue2043HotBranch], run.stagedBranches[issue2043ColdBranch])
	}
	if err := dest.AcknowledgePersistedContext(t.Context()); err != nil {
		t.Fatalf("final checkpoint: %v", err)
	}
	got, err := dest.Hash()
	if err != nil {
		t.Fatalf("completed hash: %v", err)
	}
	if got != fixture.rootHash {
		t.Fatalf("completed root = %x, want %x", got[:8], fixture.rootHash[:8])
	}
	metrics := issue2043SnapshotMetrics(dest, budget)
	if metrics.used != 0 || metrics.cursorUsed != 0 {
		t.Fatalf("completed acquisition retained budget: metrics=%+v", metrics)
	}
	t.Logf("issue2043 memory: fixture_leaves=%d hot=%d cold=%d requests=%d staged=%d peak_outstanding=%d cursor_peak=%d blocked_capacity=%d active_capacity=%d proof_capacity=%d budget_peak=%d fixture_heap=%d heap_before=%d heap_peak=%d heap_retained_peak=%d heap_after=%d reads=%d metrics=%+v", fixture.withheld, fixture.hotLeaves, fixture.coldLeaves, run.requested, run.staged, run.peakOutstanding, run.peakCursorBytes, run.peakBlocked, run.peakActive, run.peakProofs, run.budgetPeak, fixtureHeap, run.heapBefore, run.heapPeak, run.heapRetainedPeak, run.heapAfter, fixture.family.readCount(), metrics)
}

type issue2043VerifiedFixture struct {
	baseRoot    [32]byte
	pivotRoot   [32]byte
	pivotData   []byte
	baseEntries []FlushEntry
}

func newIssue2043VerifiedFixture(t testing.TB, leaves int) *issue2043VerifiedFixture {
	t.Helper()
	base := deterministicIssue2043State(t, leaves)
	baseRoot, err := base.Hash()
	if err != nil {
		t.Fatalf("hash verified base: %v", err)
	}
	baseEntries, err := collectDirtyForTest(base)
	if err != nil {
		t.Fatalf("collect verified base: %v", err)
	}
	pivot, err := base.SnapshotMutable()
	if err != nil {
		t.Fatalf("snapshot verified pivot: %v", err)
	}
	var changedKey [32]byte
	for i := range changedKey {
		changedKey[i] = 0xff
	}
	if err := pivot.Put(changedKey, []byte("issue-2043-verified-base")); err != nil {
		t.Fatalf("change verified pivot: %v", err)
	}
	pivotRoot, err := pivot.Hash()
	if err != nil {
		t.Fatalf("hash verified pivot: %v", err)
	}
	pivotData, err := pivot.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize verified pivot: %v", err)
	}
	return &issue2043VerifiedFixture{baseRoot: baseRoot, pivotRoot: pivotRoot, pivotData: pivotData, baseEntries: baseEntries}
}

func (f *issue2043VerifiedFixture) newDestination(t testing.TB, budget *AcquisitionBudget) *SHAMap {
	t.Helper()
	family := newMemoryFamily()
	if err := family.StoreBatch(t.Context(), f.baseEntries); err != nil {
		t.Fatalf("store verified base: %v", err)
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("new verified destination: %v", err)
	}
	if err := dest.AddRootNode(f.pivotRoot, f.pivotData); err != nil {
		t.Fatalf("add verified pivot root: %v", err)
	}
	issue2043AttachBudget(dest, budget)
	if err := dest.SetVerifiedBaseContext(t.Context(), f.baseRoot); err != nil {
		t.Fatalf("set verified base: %v", err)
	}
	return dest
}

func BenchmarkIssue2043Acquisition(b *testing.B) {
	template := newIssue2043MemoryFixture(b, 512)
	b.Run("retry-drain", func(b *testing.B) {
		b.ReportAllocs()
		var calls, reads, cursorPeak, budgetPeak uint64
		for range b.N {
			b.StopTimer()
			fixture := template.cloneMemory(b)
			budget := issue2043NewBudget(issue2043BudgetBytes)
			dest := fixture.newDestination(b, budget)
			fixture.family.resetReadCount()
			b.StartTimer()
			run := issue2043DriveInterleaved(b, fixture, dest, budget, issue2043DriveOptions{})
			b.StopTimer()
			calls += run.calls
			reads += fixture.family.readCount()
			cursorPeak += run.peakCursorBytes
			budgetPeak += run.budgetPeak
		}
		b.ReportMetric(float64(calls)/float64(b.N), "frontier-calls/op")
		b.ReportMetric(float64(reads)/float64(b.N), "durable-reads/op")
		b.ReportMetric(float64(cursorPeak)/float64(b.N), "cursor-peak-bytes/op")
		b.ReportMetric(float64(budgetPeak)/float64(b.N), "budget-peak-bytes/op")
	})

	b.Run("staged-arrivals", func(b *testing.B) {
		b.ReportAllocs()
		var calls, reads, cursorPeak, budgetPeak uint64
		for range b.N {
			b.StopTimer()
			fixture := template.cloneMemory(b)
			budget := issue2043NewBudget(issue2043BudgetBytes)
			dest := fixture.newDestination(b, budget)
			fixture.family.resetReadCount()
			b.StartTimer()
			run := issue2043DriveInterleaved(b, fixture, dest, budget, issue2043DriveOptions{stageBatch: 4, discoveryCallsStage: 1})
			b.StopTimer()
			calls += run.calls
			reads += fixture.family.readCount()
			cursorPeak += run.peakCursorBytes
			budgetPeak += run.budgetPeak
		}
		b.ReportMetric(float64(calls)/float64(b.N), "frontier-calls/op")
		b.ReportMetric(float64(reads)/float64(b.N), "durable-reads/op")
		b.ReportMetric(float64(cursorPeak)/float64(b.N), "cursor-peak-bytes/op")
		b.ReportMetric(float64(budgetPeak)/float64(b.N), "budget-peak-bytes/op")
	})

	b.Run("warm-durable", func(b *testing.B) {
		b.ReportAllocs()
		var reads, cursorPeak, budgetPeak uint64
		for range b.N {
			b.StopTimer()
			fixture := template.cloneMemory(b)
			fixture.releaseAll()
			budget := issue2043NewBudget(issue2043BudgetBytes)
			dest := fixture.newDestination(b, budget)
			if _, err := dest.GetMissingNodesContext(b.Context(), 0, nil); err != nil {
				b.Fatalf("warm durable setup: %v", err)
			}
			fixture.family.resetReadCount()
			b.StartTimer()
			if _, err := dest.GetMissingNodesContext(b.Context(), 0, nil); err != nil {
				b.Fatalf("warm durable walk: %v", err)
			}
			b.StopTimer()
			reads += fixture.family.readCount()
			metrics := issue2043SnapshotMetrics(dest, budget)
			cursorPeak += metrics.cursorPeakBytes
			budgetPeak += metrics.peak
		}
		b.ReportMetric(float64(reads)/float64(b.N), "durable-reads/op")
		b.ReportMetric(float64(cursorPeak)/float64(b.N), "cursor-peak-bytes/op")
		b.ReportMetric(float64(budgetPeak)/float64(b.N), "budget-peak-bytes/op")
	})

	verified := newIssue2043VerifiedFixture(b, 512)
	b.Run("verified-base", func(b *testing.B) {
		b.ReportAllocs()
		var reads, cursorPeak, budgetPeak uint64
		for range b.N {
			b.StopTimer()
			budget := issue2043NewBudget(issue2043BudgetBytes)
			dest := verified.newDestination(b, budget)
			b.StartTimer()
			missing, err := dest.GetMissingNodesContext(b.Context(), issue2043RequestLimit, nil)
			b.StopTimer()
			if err != nil && !errors.Is(err, ErrTraversalBudget) && !errors.Is(err, ErrAcquisitionMemory) {
				b.Fatalf("verified-base walk: %v", err)
			}
			if len(missing) == 0 {
				b.Fatal("verified-base walk produced no missing frontier")
			}
			metrics := issue2043SnapshotMetrics(dest, budget)
			reads += metrics.durableReads
			cursorPeak += metrics.cursorPeakBytes
			budgetPeak += metrics.peak
		}
		b.ReportMetric(float64(reads)/float64(b.N), "durable-reads/op")
		b.ReportMetric(float64(cursorPeak)/float64(b.N), "cursor-peak-bytes/op")
		b.ReportMetric(float64(budgetPeak)/float64(b.N), "budget-peak-bytes/op")
	})
}

func TestIssue2043MillionNodeStress(t *testing.T) {
	if os.Getenv(issue2043StressEnabled) != "1" {
		t.Skip("set GOXRPL_ISSUE_2043_STRESS=1 to run the disk-backed million-node stress")
	}
	leaves := issue2043StressDefaultNodes
	if raw := os.Getenv(issue2043StressNodeCount); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < issue2043FixtureLeaves {
			t.Fatalf("%s must be an integer >= %d, got %q", issue2043StressNodeCount, issue2043FixtureLeaves, raw)
		}
		leaves = parsed
	}
	fixture := newIssue2043DiskFixture(t, leaves)
	fixtureHeap := issue2043ForceGCHeap()
	budget := issue2043NewBudget(issue2043BudgetBytes)
	dest := fixture.newDestination(t, budget)
	run := issue2043DriveInterleaved(t, fixture, dest, budget, issue2043DriveOptions{
		logProgress: true,
		measureHeap: true,
	})
	if run.peakOutstanding > issue2043OutstandingLimit {
		t.Fatalf("stress outstanding replies exceeded bound: %d", run.peakOutstanding)
	}
	if run.requestedBranches[issue2043HotBranch] == 0 || run.requestedBranches[issue2043ColdBranch] == 0 {
		t.Fatalf("stress hot/cold request progress missing: hot=%d cold=%d", run.requestedBranches[issue2043HotBranch], run.requestedBranches[issue2043ColdBranch])
	}
	if err := dest.AcknowledgePersistedContext(t.Context()); err != nil {
		t.Fatalf("stress final checkpoint: %v", err)
	}
	got, err := dest.Hash()
	if err != nil {
		t.Fatalf("stress completed hash: %v", err)
	}
	if got != fixture.rootHash {
		t.Fatalf("stress completed root = %x, want %x", got[:8], fixture.rootHash[:8])
	}
	metrics := issue2043SnapshotMetrics(dest, budget)
	if metrics.used != 0 || metrics.cursorUsed != 0 {
		t.Fatalf("stress completed acquisition retained budget: metrics=%+v", metrics)
	}
	t.Logf("issue2043 stress: leaves=%d fixture_heap=%d heap_before=%d heap_peak=%d heap_retained_peak=%d heap_after=%d requests=%d staged=%d peak_outstanding=%d cursor_peak=%d budget_peak=%d reads=%d metrics=%+v", leaves, fixtureHeap, run.heapBefore, run.heapPeak, run.heapRetainedPeak, run.heapAfter, run.requested, run.staged, run.peakOutstanding, run.peakCursorBytes, run.budgetPeak, fixture.family.readCount(), metrics)
}
