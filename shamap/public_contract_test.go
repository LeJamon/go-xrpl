package shamap_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

type contractFamily struct {
	mu      sync.RWMutex
	nodes   map[[32]byte][]byte
	batches int
}

func newContractFamily() *contractFamily {
	return &contractFamily{nodes: make(map[[32]byte][]byte)}
}

func (f *contractFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return bytes.Clone(f.nodes[hash]), nil
}

func (f *contractFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches++
	for _, entry := range entries {
		f.nodes[entry.Hash] = bytes.Clone(entry.Data)
	}
	return nil
}

func mapKey(prefix byte) [32]byte {
	return [32]byte{prefix}
}

func stateValue(label string) []byte {
	value := make([]byte, 12)
	copy(value, label)
	return value
}

func TestItemDataOwnership(t *testing.T) {
	key := [32]byte{1}
	input := []byte{1, 2, 3}
	item := shamap.NewItem(key, input)

	input[0] = 9
	if got := item.Data(); !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("item changed through constructor input: %v", got)
	}

	data := item.Data()
	data[1] = 9
	if got := item.Data(); !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("item changed through Data result: %v", got)
	}
}

func TestMutableForkIsolatesMutations(t *testing.T) {
	source := shamap.New(shamap.TypeState)
	key := [32]byte{1}
	sourceData := bytes.Repeat([]byte{1}, 12)
	forkData := bytes.Repeat([]byte{2}, 12)
	if err := source.Put(key, sourceData); err != nil {
		t.Fatal(err)
	}

	fork, err := source.MutableFork()
	if err != nil {
		t.Fatal(err)
	}
	if err := fork.Put(key, forkData); err != nil {
		t.Fatal(err)
	}

	item, found, err := source.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !bytes.Equal(item.Data(), sourceData) {
		t.Fatalf("source changed through fork: found=%v data=%q", found, item.Data())
	}
}

func TestPublicMapCRUDAndOwnership(t *testing.T) {
	sm := shamap.New(shamap.TypeState)
	key := mapKey(0x10)
	input := stateValue("original")
	wantOriginal := bytes.Clone(input)

	if err := sm.Put(key, input); err != nil {
		t.Fatal(err)
	}
	input[0] = 9

	if !mustHas(t, sm, key) || sm.Size() != 1 {
		t.Fatalf("inserted item missing: has=%v size=%d", mustHas(t, sm, key), sm.Size())
	}
	item := mustGet(t, sm, key)
	if got := item.Data(); !bytes.Equal(got, wantOriginal) {
		t.Fatalf("stored data = %v", got)
	}
	itemData := item.Data()
	itemData[1] = 9
	if got := mustGet(t, sm, key).Data(); !bytes.Equal(got, wantOriginal) {
		t.Fatalf("map changed through returned item: %v", got)
	}

	replacement := stateValue("replacement")
	if err := sm.Put(key, replacement); err != nil {
		t.Fatal(err)
	}
	if sm.Size() != 1 || !bytes.Equal(mustGet(t, sm, key).Data(), replacement) {
		t.Fatal("updating a key did not replace its value")
	}
	if err := sm.Delete(key); err != nil {
		t.Fatal(err)
	}
	if mustHas(t, sm, key) || sm.Size() != 0 {
		t.Fatal("deleted item remains visible")
	}
	if item, found, err := sm.Get(key); err != nil || found || item != nil {
		t.Fatalf("missing Get = (%v, %v, %v)", item, found, err)
	}
}

func TestPublicSnapshotContracts(t *testing.T) {
	source := shamap.New(shamap.TypeState)
	originalKey := mapKey(0x10)
	sourceOnlyKey := mapKey(0x20)
	snapshotOnlyKey := mapKey(0x30)
	if err := source.Put(originalKey, stateValue("original")); err != nil {
		t.Fatal(err)
	}

	mutable, err := source.SnapshotMutable()
	if err != nil {
		t.Fatal(err)
	}
	immutable, err := source.SnapshotImmutable()
	if err != nil {
		t.Fatal(err)
	}

	if err := source.Put(sourceOnlyKey, stateValue("source")); err != nil {
		t.Fatal(err)
	}
	if err := mutable.Put(snapshotOnlyKey, stateValue("snapshot")); err != nil {
		t.Fatal(err)
	}
	if err := mutable.Delete(originalKey); err != nil {
		t.Fatal(err)
	}

	if !mustHas(t, source, originalKey) || !mustHas(t, source, sourceOnlyKey) ||
		mustHas(t, source, snapshotOnlyKey) {
		t.Fatal("source observed mutable snapshot changes")
	}
	if mustHas(t, mutable, originalKey) || mustHas(t, mutable, sourceOnlyKey) ||
		!mustHas(t, mutable, snapshotOnlyKey) {
		t.Fatal("mutable snapshot observed source changes")
	}
	if !mustHas(t, immutable, originalKey) || mustHas(t, immutable, sourceOnlyKey) ||
		mustHas(t, immutable, snapshotOnlyKey) {
		t.Fatal("immutable snapshot changed after construction")
	}
	if err := immutable.Put(mapKey(0x40), stateValue("no")); !errors.Is(err, shamap.ErrImmutable) {
		t.Fatalf("immutable Put error = %v", err)
	}
	if err := immutable.Delete(originalKey); !errors.Is(err, shamap.ErrImmutable) {
		t.Fatalf("immutable Delete error = %v", err)
	}
}

func TestPublicIterationAndBounds(t *testing.T) {
	sm := shamap.New(shamap.TypeState)
	keys := [][32]byte{mapKey(0x30), mapKey(0x10), mapKey(0x20), mapKey(0x40)}
	for _, key := range keys {
		if err := sm.Put(key, stateValue(string([]byte{key[0]}))); err != nil {
			t.Fatal(err)
		}
	}

	var visited []byte
	if err := sm.ForEach(func(item *shamap.Item) bool {
		visited = append(visited, item.Key()[0])
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(visited, []byte{0x10, 0x20, 0x30, 0x40}) {
		t.Fatalf("iteration order = %x", visited)
	}

	upper := sm.UpperBound(mapKey(0x20))
	if !upper.Valid() || upper.Item().Key() != mapKey(0x30) {
		t.Fatal("UpperBound did not return the first strictly greater key")
	}
	if !upper.Next() || upper.Item().Key() != mapKey(0x40) {
		t.Fatal("UpperBound iterator did not advance in key order")
	}
	if upper.Next() || upper.Err() != nil {
		t.Fatalf("UpperBound end = next %v, err %v", upper.Valid(), upper.Err())
	}

	lower := sm.LowerBound(mapKey(0x30))
	if !lower.Valid() || lower.Item().Key() != mapKey(0x20) {
		t.Fatal("LowerBound did not return the greatest strictly lesser key")
	}
}

func TestPublicProofRoundTrip(t *testing.T) {
	sm := shamap.New(shamap.TypeState)
	key := mapKey(0x12)
	value := stateValue("proof value")
	if err := sm.Put(key, value); err != nil {
		t.Fatal(err)
	}
	if err := sm.Put(mapKey(0xa0), stateValue("sibling")); err != nil {
		t.Fatal(err)
	}
	rootHash, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := sm.GetProofPath(key)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.Found || len(proof.Path) == 0 {
		t.Fatal("proof did not report the existing key")
	}
	if !shamap.VerifyProofPath(rootHash, key, proof.Path) {
		t.Fatal("generated proof did not verify")
	}
	tampered := make([][]byte, len(proof.Path))
	for i := range proof.Path {
		tampered[i] = bytes.Clone(proof.Path[i])
	}
	tampered[0][0] ^= 1
	if shamap.VerifyProofPath(rootHash, key, tampered) {
		t.Fatal("tampered proof verified")
	}

	missing, err := sm.GetProofPath(mapKey(0x55))
	if err != nil {
		t.Fatal(err)
	}
	if missing.Found || len(missing.Path) != 0 {
		t.Fatal("absence was reported as an inclusion proof")
	}
}

func TestPublicFamilyPersistenceAndRootLoading(t *testing.T) {
	family := newContractFamily()
	sm, err := shamap.NewBacked(shamap.TypeState, family)
	if err != nil {
		t.Fatal(err)
	}
	values := map[[32]byte][]byte{
		mapKey(0x10): stateValue("one"),
		mapKey(0x20): stateValue("two"),
		mapKey(0xa0): stateValue("three"),
	}
	for key, value := range values {
		if err := sm.Put(key, value); err != nil {
			t.Fatal(err)
		}
	}
	rootHash, err := sm.Hash()
	if err != nil {
		t.Fatal(err)
	}

	if err := sm.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(context.Background(), entries)
	}); err != nil {
		t.Fatal(err)
	}
	family.mu.RLock()
	firstBatchCount := family.batches
	family.mu.RUnlock()
	if firstBatchCount != 1 {
		t.Fatalf("stored batches = %d", firstBatchCount)
	}
	if err := sm.StoreDirty(func([]shamap.FlushEntry) error {
		t.Fatal("clean map attempted a duplicate store")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := sm.AcknowledgePersistedContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	loaded, err := shamap.NewFromRootHash(shamap.TypeState, rootHash, family)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := loaded.Hash(); err != nil || got != rootHash {
		t.Fatalf("loaded root = %x, err %v", got, err)
	}
	for key, value := range values {
		if got := mustGet(t, loaded, key).Data(); !bytes.Equal(got, value) {
			t.Fatalf("loaded value for %x = %q", key[:2], got)
		}
	}
}

func TestPublicAcquisitionReconstructsRoot(t *testing.T) {
	source := shamap.New(shamap.TypeState)
	keys := [][32]byte{mapKey(0x10), mapKey(0x11), mapKey(0x80), mapKey(0xf0)}
	for _, key := range keys {
		if err := source.Put(key, stateValue(string([]byte{key[0], key[1]}))); err != nil {
			t.Fatal(err)
		}
	}
	rootHash, err := source.Hash()
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) < 2 {
		t.Fatal("source traversal did not include descendants")
	}

	acquired := shamap.New(shamap.TypeState)
	if err := acquired.AddRootNode(rootHash, nodes[0].Data); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes[1:] {
		nodeID, err := shamap.ParseNodeID(node.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := acquired.AddKnownNodeByID(nodeID, node.Data)
		if err != nil {
			t.Fatalf("add %x: %v", nodeID.Bytes(), err)
		}
		if result == shamap.NodeInvalid {
			t.Fatalf("add %x returned NodeInvalid", nodeID.Bytes())
		}
	}
	if err := acquired.FinishSync(); err != nil {
		t.Fatal(err)
	}
	if got, err := acquired.Hash(); err != nil || got != rootHash {
		t.Fatalf("acquired root = %x, err %v", got, err)
	}
	for _, key := range keys {
		if !mustHas(t, acquired, key) {
			t.Fatalf("acquired map is missing %x", key[:2])
		}
	}
}

func mustHas(t *testing.T, sm *shamap.SHAMap, key [32]byte) bool {
	t.Helper()
	found, err := sm.Has(key)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func mustGet(t *testing.T, sm *shamap.SHAMap, key [32]byte) *shamap.Item {
	t.Helper()
	item, found, err := sm.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !found || item == nil {
		t.Fatalf("key %x not found", key[:2])
	}
	return item
}

func TestLeafReaderIsReadOnlyView(t *testing.T) {
	payload := bytes.Repeat([]byte{1}, 12)
	wire := append(bytes.Clone(payload), byte(protocol.WireTypeTransaction))
	leaf, err := shamap.NewTransactionLeafFromWire(wire)
	if err != nil {
		t.Fatal(err)
	}

	var node shamap.NodeReader = leaf
	if node.Type() != shamap.NodeTypeTransactionNoMeta {
		t.Fatalf("node type = %v", node.Type())
	}

	data := leaf.Item().Data()
	data[0] = 9
	if got := leaf.Item().Data(); !bytes.Equal(got, payload) {
		t.Fatalf("leaf item changed through public reader: %v", got)
	}
}
