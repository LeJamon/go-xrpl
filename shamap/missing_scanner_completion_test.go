package shamap

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"testing"
	"time"
)

const (
	missingScannerCompletionLeaves = 16_384
	// The fixture shares the first four nibbles and branches into broad
	// subtrees at depth five, just below the former cache warm-depth boundary.
	missingScannerCompletionHoleDepth = 5
)

type missingScannerCompletionFamily struct {
	mu        sync.Mutex
	data      map[[32]byte][]byte
	held      map[[32]byte]struct{}
	reads     map[[32]byte]int
	fullBelow *FullBelowCache
}

func (f *missingScannerCompletionFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.reads[hash]++
	_, held := f.held[hash]
	data := append([]byte(nil), f.data[hash]...)
	f.mu.Unlock()
	if held {
		return nil, nil
	}
	return data, nil
}

func (f *missingScannerCompletionFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range entries {
		f.data[entry.Hash] = append([]byte(nil), entry.Data...)
	}
	return nil
}

func (f *missingScannerCompletionFamily) FullBelowCache() *FullBelowCache {
	return f.fullBelow
}

func (f *missingScannerCompletionFamily) release(hash [32]byte) {
	f.mu.Lock()
	delete(f.held, hash)
	f.mu.Unlock()
}

func (f *missingScannerCompletionFamily) snapshotReads() map[[32]byte]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	reads := make(map[[32]byte]int, len(f.reads))
	for hash, count := range f.reads {
		reads[hash] = count
	}
	return reads
}

type missingScannerCompletionTarget struct {
	hash   [32]byte
	nodeID NodeID
	wire   []byte
}

type missingScannerCompletionFixture struct {
	dest       *SHAMap
	family     *missingScannerCompletionFamily
	rootHash   [32]byte
	target     missingScannerCompletionTarget
	pathHashes map[[32]byte]struct{}
	budget     *AcquisitionBudget
}

func newMissingScannerCompletionFixture(t *testing.T, cache *FullBelowCache) *missingScannerCompletionFixture {
	t.Helper()
	source := New(TypeState)
	for i := 1; i <= missingScannerCompletionLeaves; i++ {
		var key [32]byte
		key[0] = 0x01
		key[1] = 0x02
		key[2] = byte(i >> 8)
		key[3] = byte(i)
		var value [12]byte
		binary.BigEndian.PutUint64(value[4:], uint64(i))
		if err := source.Put(key, value[:]); err != nil {
			t.Fatalf("put fixture leaf %d: %v", i, err)
		}
	}

	rootHash, err := source.Hash()
	if err != nil {
		t.Fatalf("hash fixture: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("serialize fixture root: %v", err)
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("walk fixture: %v", err)
	}

	target := missingScannerCompletionTarget{}
	data := make(map[[32]byte][]byte, len(wireNodes))
	for _, wireNode := range wireNodes {
		entry, err := FlushEntryFromWire(wireNode.Data, 0, TypeState)
		if err != nil {
			t.Fatalf("convert fixture node at depth %d: %v", wireNode.NodeID[32], err)
		}
		data[entry.Hash] = append([]byte(nil), entry.Data...)
		if target.hash != ([32]byte{}) || int(wireNode.NodeID[32]) != missingScannerCompletionHoleDepth {
			continue
		}
		node, err := deserializeNodeFromWire(wireNode.Data)
		if err != nil {
			t.Fatalf("decode candidate at depth %d: %v", wireNode.NodeID[32], err)
		}
		if _, ok := node.(*innerNode); !ok {
			continue
		}
		nodeID, err := ParseNodeID(wireNode.NodeID)
		if err != nil {
			t.Fatalf("parse candidate node ID: %v", err)
		}
		// Leave several depth-5 siblings ahead of the hole. This exercises
		// completion tracking for children visited before the first missing node.
		if selectBranchForPath(nodeID.ID(), missingScannerCompletionHoleDepth-1) < 3 {
			continue
		}
		target = missingScannerCompletionTarget{
			hash:   entry.Hash,
			nodeID: nodeID,
			wire:   append([]byte(nil), wireNode.Data...),
		}
	}
	if target.hash == ([32]byte{}) {
		t.Fatalf("fixture has no inner node at depth %d", missingScannerCompletionHoleDepth)
	}

	pathHashes := make(map[[32]byte]struct{})
	for _, wireNode := range wireNodes {
		nodeID, err := ParseNodeID(wireNode.NodeID)
		if err != nil {
			t.Fatalf("parse fixture node ID: %v", err)
		}
		if int(nodeID.Depth()) > int(target.nodeID.Depth()) ||
			!pathPrefixEq(nodeID.ID(), target.nodeID.ID(), int(nodeID.Depth())) {
			continue
		}
		entry, err := FlushEntryFromWire(wireNode.Data, 0, TypeState)
		if err != nil {
			t.Fatalf("convert path node at depth %d: %v", nodeID.Depth(), err)
		}
		pathHashes[entry.Hash] = struct{}{}
	}

	family := &missingScannerCompletionFamily{
		data:      data,
		held:      map[[32]byte]struct{}{target.hash: {}},
		reads:     make(map[[32]byte]int),
		fullBelow: cache,
	}
	dest, err := NewBacked(TypeState, family)
	if err != nil {
		t.Fatalf("create backed destination: %v", err)
	}
	if err := dest.AddRootNode(rootHash, rootData); err != nil {
		t.Fatalf("add fixture root: %v", err)
	}
	budget := NewAcquisitionBudget(8 << 20)
	dest.SetAcquisitionBudget(budget)
	t.Cleanup(dest.ReleaseAcquisitionBudget)
	return &missingScannerCompletionFixture{
		dest:       dest,
		family:     family,
		rootHash:   rootHash,
		target:     target,
		pathHashes: pathHashes,
		budget:     budget,
	}
}

type missingScannerCompletionFilter struct {
	hash [32]byte
}

func (f missingScannerCompletionFilter) ShouldFetch(hash [32]byte) bool {
	return hash != f.hash
}

func scanMissingScannerCompletionPass(t *testing.T, dest *SHAMap, filter SyncFilter) error {
	t.Helper()
	for call := 0; call < 64; call++ {
		result, err := dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 2048), 256, filter)
		if err != nil {
			t.Fatalf("completion scan slice %d: %v", call+1, err)
		}
		switch result.Status {
		case MissingNodesWaiting:
			return nil
		case MissingNodesMemory:
			t.Fatalf("completion scan exhausted bounded memory at slice %d: %+v", call+1, result)
		case MissingNodesComplete:
			return fmt.Errorf("completion scan reached complete while target remained withheld")
		}
	}
	return fmt.Errorf("completion scan did not finish a pass within 64 slices")
}

func repeatedReadsOutsideTargetPath(before, after map[[32]byte]int, pathHashes map[[32]byte]struct{}) int {
	repeated := 0
	for hash, count := range after {
		if _, onTargetPath := pathHashes[hash]; onTargetPath {
			continue
		}
		if delta := count - before[hash]; delta > 0 {
			repeated += delta
		}
	}
	return repeated
}

func TestMissingScannerSkipsCompletedDeepSiblingsAcrossPasses(t *testing.T) {
	fixture := newMissingScannerCompletionFixture(t, NewFullBelowCache())
	for range 2 {
		before := fixture.dest.BackedWalkStats().TraversalDiscoveries
		result, err := fixture.dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 2048), 256, nil)
		if err != nil || result.Status != MissingNodesMore {
			t.Fatalf("initial traversal slice = %+v, error=%v", result, err)
		}
		if got := fixture.dest.BackedWalkStats().TraversalDiscoveries; got <= before {
			t.Fatal("first traversal slice did not credit newly discovered local nodes")
		}
	}
	filter := missingScannerCompletionFilter{hash: fixture.target.hash}
	if err := scanMissingScannerCompletionPass(t, fixture.dest, filter); err != nil {
		t.Fatal(err)
	}
	firstReads := fixture.family.snapshotReads()
	completedReads := 0
	for hash, count := range firstReads {
		if _, onTargetPath := fixture.pathHashes[hash]; !onTargetPath {
			completedReads += count
		}
	}
	if completedReads < 10_000 {
		t.Fatalf("fixture did not traverse enough completed deep siblings: %d reads", completedReads)
	}

	discoveries := fixture.dest.BackedWalkStats().TraversalDiscoveries
	if err := scanMissingScannerCompletionPass(t, fixture.dest, filter); err != nil {
		t.Fatal(err)
	}
	secondReads := fixture.family.snapshotReads()
	if got := fixture.dest.BackedWalkStats().TraversalDiscoveries; got != discoveries {
		t.Fatalf("retry credited %d repeated observations as discoveries", got-discoveries)
	}
	if repeated := repeatedReadsOutsideTargetPath(firstReads, secondReads, fixture.pathHashes); repeated != 0 {
		t.Fatalf("second incomplete pass reread %d completed deep-sibling nodes", repeated)
	}
	if got, err := fixture.dest.Hash(); err != nil || got != fixture.rootHash {
		t.Fatalf("root changed while target was withheld: hash=%x err=%v", got[:4], err)
	}
}

func TestMissingScannerSkipsCompletedDeepSiblingsAfterCacheRotation(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*missingScannerCompletionFixture, uint32)
	}{
		{
			name: "eviction",
			prepare: func(fixture *missingScannerCompletionFixture, generation uint32) {
				for i := range 512 {
					var hash [32]byte
					hash[0] = byte(i % fullBelowCacheShards)
					binary.BigEndian.PutUint64(hash[24:], uint64(i+1))
					fixture.family.fullBelow.Insert(generation, hash)
				}
			},
		},
		{
			name: "expiry",
			prepare: func(fixture *missingScannerCompletionFixture, generation uint32) {
				for i := range 64 {
					var hash [32]byte
					hash[0] = byte(i % fullBelowCacheShards)
					binary.BigEndian.PutUint64(hash[24:], uint64(i+1))
					fixture.family.fullBelow.Insert(generation, hash)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Unix(10_000, 0)
			cache := newFullBelowCacheWithClock(64, time.Minute, func() time.Time { return now })
			fixture := newMissingScannerCompletionFixture(t, cache)
			filter := missingScannerCompletionFilter{hash: fixture.target.hash}
			if err := scanMissingScannerCompletionPass(t, fixture.dest, filter); err != nil {
				t.Fatal(err)
			}
			firstReads := fixture.family.snapshotReads()
			generation := cache.Generation()
			test.prepare(fixture, generation)
			if test.name == "expiry" {
				now = now.Add(2 * time.Minute)
				cache.Sweep()
			}
			if got := cache.Generation(); got != generation {
				t.Fatalf("cache generation changed during %s preparation: %d -> %d", test.name, generation, got)
			}
			if stats := cache.Stats(); stats.Evictions == 0 {
				t.Fatalf("%s preparation did not evict any cache marks: %+v", test.name, stats)
			}

			if err := scanMissingScannerCompletionPass(t, fixture.dest, filter); err != nil {
				t.Fatal(err)
			}
			secondReads := fixture.family.snapshotReads()
			if repeated := repeatedReadsOutsideTargetPath(firstReads, secondReads, fixture.pathHashes); repeated != 0 {
				t.Fatalf("second pass after %s reread %d completed deep-sibling nodes", test.name, repeated)
			}
		})
	}
}

func TestMissingScannerDelayedArrivalCompletesAndRetiresCursor(t *testing.T) {
	fixture := newMissingScannerCompletionFixture(t, NewFullBelowCache())
	filter := missingScannerCompletionFilter{hash: fixture.target.hash}
	const rounds = 2
	for round := range rounds {
		if err := scanMissingScannerCompletionPass(t, fixture.dest, filter); err != nil {
			t.Fatalf("delayed round %d: %v", round+1, err)
		}
		stats := fixture.dest.BackedWalkStats()
		budget := fixture.budget.Snapshot()
		if stats.CursorBytes > budget.Limit/acquisitionCursorBudgetFraction ||
			stats.CursorPeakBytes > budget.Limit/acquisitionCursorBudgetFraction ||
			budget.CursorUsed > budget.Limit/acquisitionCursorBudgetFraction {
			t.Fatalf("delayed round %d exceeded cursor partition: stats=%+v budget=%+v", round+1, stats, budget)
		}
	}

	discoveriesBeforeArrival := fixture.dest.BackedWalkStats().TraversalDiscoveries
	attached, entry, err := fixture.dest.AddKnownNodeByIDWithEntryContext(t.Context(), fixture.target.nodeID, fixture.target.wire)
	if err != nil {
		t.Fatalf("attach delayed subtree: %v", err)
	}
	if attached != NodeUseful {
		t.Fatalf("attach delayed subtree = %v, want useful", attached)
	}
	if err := fixture.family.StoreBatch(t.Context(), []FlushEntry{entry}); err != nil {
		t.Fatalf("persist delayed subtree: %v", err)
	}
	fixture.family.release(fixture.target.hash)

	completed := false
	for attempt := 0; attempt < 64; attempt++ {
		result, err := fixture.dest.ScanMissingNodesContext(WithTraversalBudget(t.Context(), 2048), 256, nil)
		if err != nil {
			t.Fatalf("completion slice %d: %v", attempt+1, err)
		}
		switch result.Status {
		case MissingNodesComplete:
			completed = true
		case MissingNodesPersistence:
			if err := fixture.dest.AcknowledgePersistedContext(t.Context()); err != nil {
				t.Fatalf("acknowledge completion slice %d: %v", attempt+1, err)
			}
		case MissingNodesMemory:
			t.Fatalf("completion slice %d exhausted bounded memory: %+v", attempt+1, result)
		}
		if completed {
			break
		}
	}
	if !completed {
		t.Fatal("scanner did not complete after delayed subtree arrival")
	}
	if got, err := fixture.dest.Hash(); err != nil || got != fixture.rootHash {
		t.Fatalf("completed root = %x, want %x (err=%v)", got[:4], fixture.rootHash[:4], err)
	}
	stats := fixture.dest.BackedWalkStats()
	if stats.TraversalDiscoveries <= discoveriesBeforeArrival {
		t.Fatal("arrival did not credit discovery of the newly available subtree")
	}
	if stats.CursorBytes != 0 || stats.ActiveFrames != 0 || stats.BlockedItems != 0 || stats.ProofEntries != 0 {
		t.Fatalf("completed scanner retained cursor work: %+v", stats)
	}
	if budget := fixture.budget.Snapshot(); budget.CursorUsed != 0 {
		t.Fatalf("completed scanner retained cursor budget: %+v", budget)
	}
}
