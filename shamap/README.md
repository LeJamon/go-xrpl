# SHAMap Specification

## Overview

SHAMap is a specialized data structure used in the XRP Ledger that combines features of:

- **Merkle Tree**: Each non-leaf node is labeled with the hash of its children.
- **Patricia Trie (Radix Tree)**: Efficient prefix tree with path compression.
- **Hexary Tree**: Each inner node can have up to 16 children (one for each hex nibble).

SHAMaps support fast, verifiable access to state and transaction data in a ledger.

---

## Data Structure

### Node Types

#### 1. `InnerNode`

- Represents a branch point in the tree.
- Contains up to 16 children (0–15), corresponding to hex nibbles.
- Empty slots represent absent paths.
- Stores:
  - A bitmap or mask indicating which children are present.
  - The hash of each present child.
- Hash is computed from the ordered set of **all 16 child positions**.

#### 2. `LeafNode`

- Contains a single key-value pair (a `SHAMapItem`).
- Represents the endpoint of a path in the tree.
- Hash is computed from both the key and value with appropriate prefix.

---

### `SHAMapItem`

Encapsulates the actual ledger data stored at a leaf node.

- `key`: 256-bit hash used to place the item in the trie.
- `value`: Serialized ledger object (e.g., Account, Offer, NFT).

---

### Tree Structure

- Keys are 256-bit hashes → 64 hex nibbles → max depth = 64.
- Each level corresponds to one hex digit (4 bits).
- Nodes compress paths where possible (if only one child exists).
- Leaf nodes appear conceptually at depth 64.

---

## Hash Calculation

### InnerNode Hash

**Critical**: InnerNode hashes **must include all 16 child positions** in order:

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

### LeafNode Hash

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

The root hash is simply the hash of the root InnerNode, calculated using the InnerNode hash algorithm above.

---

## Core Operations

### Item-Level

- `HasItem(hash) → bool`: Check if an item exists.
- `GetItem(hash) → SHAMapItem`: Retrieve the item.
- `AddItem(item)`: Insert a new item.
- `UpdateItem(item)`: Replace an existing item.
- `DeleteItem(hash)`: Delete an item.

### Tree-Level

- `GetHash() → Hash256`: Return root hash.
- `Snapshot(mutable: bool) → SHAMap`: Clone the tree for concurrent reads/writes.
- `SetImmutable()`: Prevent further modifications.

### Navigation & Iteration

- `FirstItem() → SHAMapItem`: Get first item in tree order.
- `NextItem(key) → SHAMapItem`: Get next item after given key.
- `PrevItem(key) → SHAMapItem`: Get previous item before given key.
- `FindRange(start, end) → ItemCollection`: Get items in key range.
- `ForEachItem(visitor)`: Apply function to each item in tree order.
- `ForEachNode(visitor)`: Apply function to each node (inner and leaf).

### Tree Comparison

- `Compare(other, maxDifferences) → DifferenceSet`: Compare two SHAMaps and return differences.
- `Equal(other) → bool`: Fast equality check using root hashes.
- `FindDifferences(other, visitor)`: Call visitor for items that differ between maps.

### State Management

- `IsValid() → bool`: Check if map is in valid state.
- `IsSynching() → bool`: Check if map is in syncing state.
- `SetSynching()`: Mark map as syncing.
- `ClearSynching()`: Clear syncing state.
- `IsBackedByStorage() → bool`: Check if backed by persistent storage.

### Sync & Proof

- `GetMissingNodes(maxCount) → NodeIdentifierList`: Identify missing nodes for sync.
- `AddKnownNode(nodeID, data)`: Add a known node from external source.
- `FetchRoot(hash)`: Fetch and set root from hash.
- `AddRootNode(hash, data)`: Add root node from external data.
- `GetProofPath(key) → ProofPath`: Generate a Merkle proof for a key.
- `VerifyProofPath(rootHash, key, proof) → bool`: Verify a Merkle proof.

### Serialization & Storage

- `SerializeRoot() → ByteArray`: Serialize root node for transmission.
- `GetNodeWithChildren(nodeID, depth) → NodeDataSet`: Get node with children to specified depth.
- `Serialize() → ByteArray`: Serialize entire tree.
- `Deserialize(data) → SHAMap`: Reconstruct tree from serialized data.
- `SaveToStorage(storage)`: Persist tree to storage backend.
- `LoadFromStorage(storage, rootHash) → SHAMap`: Load tree from storage.

### Maintenance & Debugging

- `OptimizeStructure() → int`: Optimize tree structure, return number of changes.
- `FlushToStorage() → int`: Flush dirty nodes to storage, return count.
- `ValidateStructure() → ValidationResult`: Check tree consistency and integrity.
- `GetStatistics() → TreeStatistics`: Return tree metrics (size, depth, etc.).
- `DumpStructure(includeHashes) → string`: Debug representation of tree structure.

---

## Technical Requirements

### Hashing

- **InnerNode** hash = SHA512Half of prefix + all 16 child hashes (32 bytes each, zeros for empty).
- **LeafNode** hash = SHA512Half of type-specific prefix + data + key (where applicable).
- SHA512Half = first 32 bytes of SHA512 hash.
- Hashes must be deterministic and collision-resistant.
- **Empty nodes have zero hash**.

### Hash Prefixes (4 bytes each, big-endian)

- `HashPrefixInnerNode`: `0x4D494E00` ("MIN\0")
- `HashPrefixLeafNode`: `0x4D4C4E00` ("MLN\0") - for account state
- `HashPrefixTransactionID`: `0x54584E00` ("TXN\0") - for transactions without metadata
- `HashPrefixTxNode`: `0x534E4400` ("SND\0") - for transactions with metadata

### Branch Selection Algorithm

Path through tree determined by key nibbles:
- Depth 0: Upper 4 bits of byte 0
- Depth 1: Lower 4 bits of byte 0
- Depth 2: Upper 4 bits of byte 1
- Depth 3: Lower 4 bits of byte 1
- Continue pattern for remaining bytes

### Tree Modification Rules

- **Insertion**: If key exists, update value. If not, create new leaf and split nodes as needed.
- **Deletion**: Remove leaf and consolidate empty inner nodes.
- **Node Splitting**: When two keys collide at same depth, create new inner node to separate them.
- **Path Compression**: Merge inner nodes that have only one child.

### Immutability & Versioning

- Trees can be marked immutable to prevent modifications.
- Support for creating versioned snapshots.
- Shared node structures between versions for memory efficiency.

### Concurrency Model

- Multiple concurrent readers allowed.
- Single writer at a time.
- Immutable snapshots can be read concurrently with writes to original.
- Implementation should handle concurrent access patterns appropriate to the platform.

---

## Performance Characteristics

### Time Complexity

- **Lookup**: O(log₁₆ n) where n is number of items
- **Insert/Update/Delete**: O(log₁₆ n)
- **Tree Comparison**: O(min(n₁, n₂)) where n₁, n₂ are tree sizes
- **Iteration**: O(n) for full traversal

### Space Complexity

- **Storage**: O(n) where n is number of items
- **Memory**: Depends on caching strategy and node sharing
- **Hash Computation**: O(log₁₆ n) stack space for tree traversal

### Optimization Guidelines

- Cache computed hashes to avoid recomputation.
- Use efficient node storage formats.
- Implement appropriate caching strategies for frequently accessed nodes.
- Consider lazy loading for large trees with persistent storage.
- Optimize for common access patterns (sequential reads, range queries).

---

## Notes

- SHAMap is deterministic: same inputs always produce the same root hash.
- Used for both ledger state (`stateMap`) and transaction history (`txMap`).
- Essential for consensus and ledger verification.
- **Hash compatibility requires exact implementation of the hash calculation algorithms above**.
- Tree structure must be identical across implementations for hash compatibility.