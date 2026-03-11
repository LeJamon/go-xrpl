# SHAMap Conformance Assessment (2026-03-10, updated)

## Overall: Mostly Conformant (no critical bugs)
- 144 test assertions pass, 0 fail
- All node hashing matches rippled exactly (SHA-512 Half with correct prefixes)
- Wire serialization matches for all 5 wire types
- Store (prefix) serialization matches
- Tree structure (BranchFactor=16, MaxDepth=64, SelectBranch) matches

## Verified Correct Areas

### Core Data Structure
- Node types: Go has InnerNode + 3 leaf types (AccountState, Transaction, TransactionWithMeta) -- matches rippled
- NodeID: depth(uint8) + id([32]byte) -- matches rippled
- BranchFactor=16, MaxDepth=64, isBranch_ uint16 bitmap -- all match

### Hashing
- Inner: SHA512Half(innerNode prefix + all 16 child hashes including zeros) -- matches
- AccountState: SHA512Half(leafNode prefix + data + key) -- matches
- Transaction: SHA512Half(transactionID prefix + data) -- matches (NO key)
- TxWithMeta: SHA512Half(txNode prefix + data + key) -- matches

### Wire Serialization (all correct)
- TransactionLeaf: [data][wireType:0] -- key NOT included (FIXED, previously was a bug)
- AccountState: [data][key][wireType:1] -- matches rippled
- TxWithMeta: [data][key][wireType:4] -- matches rippled
- Inner full: [16 x hash32][wireType:2] -- matches
- Inner compressed (branchCount < 12): [hash32][pos1]...[wireType:3] -- matches

### Prefix (Store) Serialization (all correct)
- InnerNode: [4-byte prefix][16 x hash32] = 516 bytes
- Leaf nodes: [4-byte prefix][data][key]
- Transaction (no meta): [4-byte prefix][data] (key derived)
- DeserializeFromPrefix dispatches correctly on prefix

### Tree Operations
- Add/Split: creates inner nodes at divergence point, same as rippled
- Delete/Consolidate: collapses single-child inner nodes via onlyBelow
- walkToKey / walkToKeyForDirty / dirtyUp: correct path manipulation

### Compare/Delta
- Stack-based DFS with hash-shortcutting -- same algorithm as rippled
- walkBranch semantics match rippled SHAMapDelta.cpp
- Go adds streaming (channel-based) version -- extra feature

### Iterator
- Begin(), UpperBound(id), LowerBound(id) -- all match rippled semantics

## Minor Deviations (non-critical)

1. **Leaf minimum size**: rippled asserts `item_->size() >= 12`, Go has no check
2. **Compare inner-inner for backed maps**: Go descends children, rippled compares stored hashes directly via getChildHash(i). Performance difference only.
3. **InnerNode.Clone()**: Go deep-clones recursively O(subtree), rippled shares children O(branchCount). Performance only.
4. **Unbacked snapshot**: Go O(tree) deep clone, rippled O(1) COW via cowid_. Backed maps use efficient flush+re-fetch path.
5. **Delete replacement leaf type**: Go derives from map type, rippled from original leaf. Same result in practice.

## Missing Features (vs rippled, not bugs)
- TreeNodeCache / canonicalize (node sharing across maps)
- fullBelowGen optimization (skip fully-synced subtrees)
- walkMapParallel (parallel sync traversal)
- descendAsync (async node loading)

## Key File Locations
- Go: `goXRPL/shamap/` -- inner_node.go, leaf_node.go, node_id.go, shamap.go, compare.go, store.go, wire.go, proof.go
- Rippled: `rippled/src/xrpld/shamap/` (headers) + `detail/` (implementations)
- Hash prefixes: `goXRPL/protocol/hash_prefix.go`
- Wire types: `goXRPL/protocol/wire_prefix.go`
