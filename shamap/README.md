# SHAMap

SHAMap is the Merkle-like radix tree used by the XRP Ledger for ledger state
and transaction storage. It combines features of:

- **Merkle Tree**: Each non-leaf node is labeled with the hash of its children.
- **Patricia Trie (Radix Tree)**: Efficient prefix tree with path compression.
- **Hexary Tree**: Each inner node can have up to 16 children (one for each hex nibble).

SHAMaps support fast, verifiable access to state and transaction data in a ledger.

---

## Data Structure

### Node Types

#### Inner nodes

- Represent branch points in the tree.
- Contain up to 16 children (0–15), corresponding to hex nibbles.
- Empty slots represent absent paths.
- Store a bitmap indicating which children are present and the hash of each
  present child. For store-backed maps a branch may be *hash-only*: the hash
  is known but the child node is loaded lazily from the `Family` on demand.
- Hash is computed from the ordered set of **all 16 child positions**.

#### Leaf nodes

- Contain a single key-value pair (an `Item`).
- Represent the endpoint of a path in the tree.
- Come in three flavours (account state, transaction, transaction+metadata)
  that differ in hash prefix and wire format.

### Tree Structure

- Keys are 256-bit hashes → 64 hex nibbles → max depth = 64.
- Each level corresponds to one hex digit (4 bits).
- Inner nodes with a single child below them collapse to a leaf.
- Leaf nodes appear conceptually at depth 64.

---

## Hash Calculation

### Inner node hash

**Critical**: inner node hashes **must include all 16 child positions** in order:

```
innerNodeHash = SHA512Half(
    HashPrefixInnerNode +     // 4-byte prefix: 0x4D494E00
    childHash[0] +            // 32 bytes (or zeros if empty)
    childHash[1] +            // 32 bytes (or zeros if empty)
    ...
    childHash[15]             // 32 bytes (or zeros if empty)
)
```

- Empty child positions contribute 32 zero bytes
- Total input: 4 + (16 × 32) = 516 bytes
- If node has no children, hash is zero (all zeros)

### Leaf node hash

Depends on leaf type:

**Account State Leaf:**
```
leafHash = SHA512Half(
    HashPrefixLeafNode +      // 4-byte prefix: 0x4D4C4E00
    itemData +                // Serialized account data
    itemKey                   // 32-byte key
)
```

**Transaction Leaf (no metadata):**
```
leafHash = SHA512Half(
    HashPrefixTransactionID + // 4-byte prefix: 0x54584E00
    transactionData           // Serialized transaction
)
```

**Transaction Leaf (with metadata):**
```
leafHash = SHA512Half(
    HashPrefixTxNode +        // 4-byte prefix: 0x534E4400
    transactionData +         // Serialized transaction + metadata
    transactionKey            // 32-byte transaction hash
)
```

### Root Hash

The root hash is simply the hash of the root inner node, calculated using the
inner node hash algorithm above.

### Hash Prefixes (4 bytes each, big-endian)

- `HashPrefixInnerNode`: `0x4D494E00` ("MIN\0")
- `HashPrefixLeafNode`: `0x4D4C4E00` ("MLN\0") - for account state
- `HashPrefixTransactionID`: `0x54584E00` ("TXN\0") - for transactions without metadata
- `HashPrefixTxNode`: `0x534E4400` ("SND\0") - for transactions with metadata

### Branch Selection

Path through tree determined by key nibbles:
- Depth 0: Upper 4 bits of byte 0
- Depth 1: Lower 4 bits of byte 0
- Depth 2: Upper 4 bits of byte 1
- Continue pattern for remaining bytes

---

## API Overview

### Construction

- `New(mapType) *SHAMap` — in-memory map (`TypeTransaction` or `TypeState`).
- `NewBacked(mapType, family)` — map that flushes to and lazy-loads from a
  `Family` (persistent node store).
- `NewFromRootHash(mapType, rootHash, family)` — open an existing tree by
  root hash; children resolve lazily from the store.
- `Family` implementations: `NodeStoreFamily` (memory or PebbleDB via
  `NewMemoryNodeStoreFamily` / `NewPebbleNodeStoreFamily` /
  `NewNodeStoreFamily`) and `OverlayFamily` (copy-on-write overlay over a
  read-only base).

### Items

- `Put(key, data)` / `PutItem` / `PutWithNodeType` / `PutItemWithNodeType`
- `Get(key)` / `Has(key)` / `Delete(key)`
- `ForEach` / `ForEachCtx` — in-order leaf iteration.
- `UpperBound(key)` — iterator at the first item with key > the argument.
- `Size()` — leaf count (memoised on immutable maps).

### Lifecycle & snapshots

- `Hash()` — root hash.
- `Snapshot(mutable)` — O(1) structurally-shared copy; mutations are
  path-copy persistent so snapshots never observe changes.
- `SetImmutable()` / `State()` / `Type()` / `IsBacked()` / `SetFamily`.
- `FlushDirty(releaseChildren)` — serialize dirty nodes for the store;
  optionally release child pointers for lazy reload.

### Comparison

- `Compare(other, maxCount) (*DifferenceSet, error)` — full diff with
  added/removed/modified items.
- `FindDifference(other) ([]Key, error)` — just the differing keys.

### Synchronization (ledger acquisition)

- `StartSync` / `FinishSync` / `IsSyncing` / `IsComplete` / `SyncProgress`
- `AddRootNode(hash, wireData)` — install the root from a peer.
- `AddKnownNodeByID(nodeID, wireData)` — attach a peer-supplied node at the
  position given by its 33-byte SHAMapNodeID (path + depth).
- `AddKnownNodeFromPrefix(nodeID, prefixData)` — same, for
  `[HashPrefix][body]` fetch-pack data.
- `AddKnownNode(hash, wireData)` — hash-located attach (legacy tx-set path).
- `WalkMap` / `WalkMapParallel` / `GetMissingNodes` — enumerate referenced
  nodes that are neither in memory nor in the local store.
- `CheckComplete(ctx)` — full store-walk completeness report.

### Wire serving

- `SerializeRoot()` — root in wire format.
- `WalkWireNodes()` — pre-order (NodeID, wire blob) pairs for TMLedgerData.
- `GetNodeFatByPath(path, depth, budget, fatLeaves)` — rippled
  `SHAMap::getNodeFat` equivalent for TMGetLedger.
- `WalkFetchPackNodes(maxNodes)` / `VerifyFetchPackNode` — fetch-pack
  production and verification.

### Proofs

- `GetProofPath(key)` — Merkle proof, leaf-to-root wire blobs.
- `VerifyProofPath(rootHash, key, path)` / `VerifyProofPathWithValue`.

---

## Concurrency Model

- Each `SHAMap` has one RWMutex: multiple concurrent readers, single writer.
- Snapshots share subtrees with the source map; mutation paths shallow-clone
  every touched inner node (path-copy persistence), so shared nodes are never
  structurally modified.
- The per-node dirty flag is atomic and node hashes are accessed under each
  node's own lock, so flushing structurally-shared subtrees from different
  maps is race-free.
- Lazy loading installs children with a compare-and-swap (`SetChildIfNil`),
  so concurrent readers racing on the same branch observe one installation.

---

## Notes

- SHAMap is deterministic: same inputs always produce the same root hash.
- Used for both ledger state (`stateMap`) and transaction history (`txMap`).
- Essential for consensus and ledger verification.
- **Hash compatibility requires exact implementation of the hash calculation
  algorithms above**; tree structure must be identical across implementations.
