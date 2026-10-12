package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

func TestImportMapsPersistsCleanWireStateAndTransactionTrees(t *testing.T) {
	stateMap := cleanWireImportMap(t, shamap.TypeState)
	txMap := cleanWireImportMap(t, shamap.TypeTransaction)
	stateNodes := wireImportNodeCount(t, stateMap)
	txNodes := wireImportNodeCount(t, txMap)

	family := newConstructorRecordingFamily()
	store, _ := executionTestStore(t, family)
	stateRoot, txRoot, err := store.ImportMaps(context.Background(), stateMap, txMap, 73)
	if err != nil {
		t.Fatalf("ImportMaps: %v", err)
	}

	entries := family.takeEntries()
	counts := map[shamap.Type]int{}
	for _, entry := range entries {
		if entry.LedgerSeq != 73 {
			t.Fatalf("stored ledger sequence = %d, want 73", entry.LedgerSeq)
		}
		counts[entry.MapType]++
	}
	if counts[shamap.TypeState] != stateNodes || counts[shamap.TypeTransaction] != txNodes {
		t.Fatalf("stored clean wire nodes = state %d, transaction %d; want state %d, transaction %d", counts[shamap.TypeState], counts[shamap.TypeTransaction], stateNodes, txNodes)
	}

	for _, test := range []struct {
		name     string
		mapType  shamap.Type
		root     [32]byte
		wantSize int
	}{
		{name: "state", mapType: shamap.TypeState, root: stateRoot, wantSize: storeAllImportLeafCount},
		{name: "transaction", mapType: shamap.TypeTransaction, root: txRoot, wantSize: storeAllImportLeafCount},
	} {
		t.Run(test.name, func(t *testing.T) {
			loaded, err := shamap.NewFromRootHash(test.mapType, test.root, family)
			if err != nil {
				t.Fatalf("NewFromRootHash: %v", err)
			}
			items := 0
			if err := loaded.ForEach(func(*shamap.Item) bool {
				items++
				return true
			}); err != nil {
				t.Fatalf("ForEach: %v", err)
			}
			if items != test.wantSize {
				t.Fatalf("loaded item count = %d, want %d", items, test.wantSize)
			}
		})
	}
}

const storeAllImportLeafCount = 96

func cleanWireImportMap(t *testing.T, mapType shamap.Type) *shamap.SHAMap {
	t.Helper()
	source := shamap.New(mapType)
	for i := range storeAllImportLeafCount {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i+1))
		key := sha256.Sum256(seed[:])
		data := append(seed[:], seed[:]...)
		if mapType == shamap.TypeTransaction {
			key = sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), data)
		}
		if err := source.Put(key, data); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}

	root, err := source.Hash()
	if err != nil {
		t.Fatalf("source Hash: %v", err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		t.Fatalf("source SerializeRoot: %v", err)
	}
	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		t.Fatalf("source WalkWireNodes: %v", err)
	}
	if len(wireNodes) < 3 {
		t.Fatalf("fixture has %d wire nodes; want a multi-level tree", len(wireNodes))
	}

	clean := shamap.New(mapType)
	if _, err := clean.AddRootNodeWithEntry(root, rootData); err != nil {
		t.Fatalf("AddRootNodeWithEntry: %v", err)
	}
	for index, wireNode := range wireNodes {
		nodeID, err := shamap.ParseNodeID(wireNode.NodeID)
		if err != nil {
			t.Fatalf("ParseNodeID: %v", err)
		}
		if nodeID.IsRoot() {
			continue
		}
		result, _, err := clean.AddKnownNodeByIDWithEntryContext(context.Background(), nodeID, wireNode.Data)
		if err != nil {
			t.Fatalf("AddKnownNodeByIDWithEntryContext[%d] depth=%d wireType=%d: %v", index, nodeID.Depth(), wireNode.Data[len(wireNode.Data)-1], err)
		}
		if result != shamap.NodeUseful {
			t.Fatalf("AddKnownNodeByIDWithEntryContext depth=%d result=%v, want useful", nodeID.Depth(), result)
		}
	}
	return clean
}

func wireImportNodeCount(t *testing.T, sm *shamap.SHAMap) int {
	t.Helper()
	nodes, err := sm.WalkWireNodes()
	if err != nil {
		t.Fatalf("WalkWireNodes: %v", err)
	}
	return len(nodes)
}
