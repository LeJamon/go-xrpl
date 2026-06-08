# shamap Package Refactoring

## Current State

The `shamap/` package has 42 files totaling ~10,000 lines (excluding test files). The core tree data structure is solid (path-copy persistence, lazy loading, concurrent descent), but the package suffers from significant organizational debt and code duplication.

### Line counts (source files only)

| File | Lines | Problem |
|------|-------|---------|
| shamap.go | 1756 | God file: tree ops, diff-finding, wire walking, helper types |
| sync.go | 969 | Two BFS missing-node walks, 4 `AddKnown*` methods |
| compare.go | 794 | Full stack-based + channel-based comparison (duplicated logic) |
| leaf_node.go | 658 | 3 near-identical leaf structs |
| inner_node.go | 594 | OK for inner node complexity |
| invariants.go | 500 | Two recursive walks (stop-on-first vs detailed) |
| iterator.go | 462 | UpperBound/LowerBound + helpers |
| proof.go | 426 | 3 verify methods, 2 are near-duplicates |
| node_id.go | 293 | OK — self-contained |
| cache.go | 289 | LRU caches |
| store.go | 157 | DeserializeFromPrefix + per-type parsers |
| completeness.go | 130 | CheckComplete walk |
| fetchpack.go | 104 | FetchPackNode walk |
| doc.go | 10 | Package doc |
| wire.go | 21 | Single trivial method |
| lru.go | 74 | Generic LRU list (OK) |
| memory_family.go | 61 | In-memory Family (OK) |
| node.go | 121 | Node interface + NodeType + BaseNode |
| family.go | 15 | Family interface (OK) |
| nodestore_family.go | 113 | NodeStore-based Family (OK) |
| overlay_family.go | 46 | Overlay Family (OK) |
| difference.go | 88 | Diff types (OK) |

## Problems

### P1. Three leaf structs with near-identical code

`AccountStateLeafNode`, `TransactionLeafNode`, `TransactionWithMetaLeafNode` differ only in:
- Hash prefix constant (sets which `protocol.HashPrefix*` to use)
- Wire serialization format (whether key is appended)
- Wire deserialization (whether key is derived from data or read from wire)

The `IsLeaf`, `IsInner`, `Item`, `SetItem`, `UpdateHash`, `Clone`, `Invariants`, `String` methods are effectively identical.

### P2. Duplicated comparison logic

- `FindDifference` in shamap.go returns `[]Key` via a stack walk
- `compare.go` has `Compare` returning `*DifferenceSet` via another stack walk
- `compar.go` has channel-based `Differences`/`DifferencesWithError` that duplicate the stack walk with channel-send instead of append
- `walkBranch` and `walkBranchWithChannel` are the same algorithm with different output

### P3. 10+ distinct traversal/walk patterns

Every feature reinvents tree walking:
- `walkToKey` / `walkToKeyForDirty` (path descent, duplicated!)
- `forEachUnsafe` (recursive DFS for Size/ForEach)
- `collectAllKeysUnsafe` / `collectAllKeysExceptUnsafe` (stack-based DFS)
- `walkSubtreeForMissing` (BFS with queue — sync.go)
- `getMissingNodesUnsafe` (another BFS — sync.go)
- `walkBranch` / `walkBranchWithChannel` (stack-based DFS — compare.go)
- `checkNodeComplete` (recursive DFS — completeness.go)
- `checkNodeInvariants` / `checkNodeInvariantsDetailed` (recursive DFS — invariants.go)
- `firstBelow` / `lastBelow` (helper walks — iterator.go)
- `walkFetchPackRec` (pre-order DFS — fetchpack.go)
- `walkWireNodesRec` (pre-order DFS — shamap.go)

### P4. `putItemUnsafe` vs `putItemWithNodeTypeUnsafe` duplication

~80% shared logic; the split-handling is implemented inline in both.

### P5. `NodeStack` / `pathEntry` are package-internal but live in shamap.go

Could be extracted but this is minor.

### P6. `wire.go` is a trivial wrapper (21 lines)

Just `SerializeRoot()` calling `sm.root.SerializeForWire()`. Doesn't need its own file.

---

## Proposed File Structure

```
shamap/
  shamap.go           # SHAMap struct, New/constructors, state, locking, top-level API
  node.go             # Node interface, NodeType, BaseNode (keep)
  inner_node.go       # InnerNode (keep)
  leaf.go             # Single generic leaf struct (REPLACE leaf_node.go)
  node_id.go          # NodeID (keep)
  keypath.go          # Path utilities: SelectBranch, getBranchAtDepth, findSplitDepth,
                      # childPathForBranch, selectBranchForPath, pathPrefixEq
                      # (EXTRACT from shamap.go + node_id.go)
  traverse.go         # Unified traversal primitives (NEW):
                      # - descendToKey (replaces walkToKey + walkToKeyForDirty)
                      # - forEachLeaf (replaces forEachUnsafe)
                      # - walkPreOrder / walkPostOrder (replaces wire/fetchpack walks)
                      # - collectKeys (replaces collectAllKeys*)
  compare.go          # Single difference engine (REWRITE):
                      # - Compare → DifferenceSet (stack-based)
                      # - DifferencesWithError → channel-based
                      # Internal: one comparison walk, two output adapters
                      # REMOVE: FindDifference from shamap.go
  sync.go             # Sync logic: AddKnown*, WalkMap, GetMissingNodes, missing-node
                      # walks (using traverse.go primitives)
  completeness.go     # CheckComplete (using traverse.go primitives)
  invariants.go       # Invariants checking (using traverse.go primitives)
  proof.go            # Proof path generation + verification
  iterator.go         # Iterator, UpperBound, LowerBound (keep)
  store.go            # DeserializeFromPrefix + per-type parsers (keep)
  fetchpack.go        # Fetch pack walk (using traverse.go primitives)
  cache.go            # TreeNodeCache, FullBelowCache (keep)
  lru.go              # Generic LRU list (keep)
  family.go           # Family interface (keep)
  memory_family.go    # In-memory Family (keep)
  nodestore_family.go # NodeStore-backed Family (keep)
  overlay_family.go   # OverlayFamily (keep)
  difference.go       # DifferenceItem, DifferenceSet types (keep)
  doc.go              # Package doc (keep)
```

## Migration Strategy

### Phase 1 — Extract keypath.go (low risk, no behavior change)

1. Move `getBranchAtDepth`, `findSplitDepth`, `childPathForBranch`, `selectBranchForPath`, `pathPrefixEq` to `keypath.go`
2. Move `SelectBranch` out of `node_id.go` into `keypath.go` (it's a path utility, not a NodeID method)
3. Delete `wire.go`, inline `SerializeRoot` into its single call site
4. Keep `NodeStack` in shamap.go for now (used by many things)

Status: [ ] Not started

### Phase 2 — Unify leaf nodes (medium risk, careful with serialization)

Replace the three leaf structs with a single generic struct:

```go
type leafKind uint8
const (
    leafAccountState leafKind = iota
    leafTransaction
    leafTransactionWithMeta
)

type LeafNode struct {
    BaseNode
    mu   sync.RWMutex
    item *Item
    kind leafKind
}
```

Dispatch on `kind` in:
- `Type()` → returns appropriate NodeType
- `updateHashUnsafe()` → uses correct prefix
- `SerializeForWire()` → includes key or not per kind
- `SerializeWithPrefix()` → includes key or not per kind
- Deserialization constructors → set kind + produce bytes

Kept surface:
- `NewAccountStateLeafNode(item)` → `NewLeafNode(leafAccountState, item)`
- `NewTransactionLeafNode(item)` → `NewLeafNode(leafTransaction, item)`
- `NewTransactionWithMetaLeafNode(item)` → `NewLeafNode(leafTransactionWithMeta, item)`

Status: [ ] Not started

### Phase 3 — Unify traversal (high risk, affects many callers)

Design a minimal `Walker` abstraction:

```go
// NodeAction is the callback signature for tree walks.
// Return false to stop early.
type NodeAction func(node Node, nodeID NodeID, depth int) (bool, error)

// WalkPreOrder visits node then children (for wire/fetchpack).
func (sm *SHAMap) WalkPreOrder(ctx context.Context, start Node, startID NodeID, depth int, fn NodeAction) error

// WalkPostOrder visits children then node (for flush).
func (sm *SHAMap) WalkPostOrder(ctx context.Context, start Node, startID NodeID, fn NodeAction) error

// WalkLeaves visits every leaf (for ForEach/Size/collectKeys).
func (sm *SHAMap) WalkLeaves(ctx context.Context, start Node, fn func(*Item) bool) error

// WalkMissing visits hash-only branches that are absent from the store.
func (sm *SHAMap) WalkMissing(start *InnerNode, startID NodeID, startHash [32]byte, startDepth int, filter SyncFilter, report func(MissingNode) bool) bool
```

Then reimplement:
- `forEachUnsafe` → `WalkLeaves`
- `collectAllKeysUnsafe` → `WalkLeaves`
- `walkWireNodesRec` → `WalkPreOrder`
- `walkFetchPackRec` → `WalkPreOrder`
- `walkSubtreeForMissing` → `WalkMissing`
- `getMissingNodesUnsafe` → delete, use `WalkMissing`
- `checkNodeComplete` → `WalkPreOrder` + fetch logic
- `checkNodeInvariants` → `WalkPreOrder` + invariants logic
- `walkBranch` / `walkBranchWithChannel` → `WalkLeaves` plus comparison logic

Status: [ ] Not started

### Phase 4 — Deduplicate putItem / compare / addKnown

After Phases 1-3, the code volume drops enough that remaining duplication becomes obvious:
- `putItemUnsafe` / `putItemWithNodeTypeUnsafe` → single `putItemUnsafe` taking a `nodeType` param
- `FindDifference` → delegate to `Compare`
- `walkBranch` / `walkBranchWithChannel` → single walk with output adapter

Status: [ ] Not started

### Phase 5 — File-level reorganization

- `wire.go` → delete (inline to call site)
- `NodeStack` → either keep in shamap.go or move to a file that needs it
- Rename/move functions as needed for cohesion

Status: [ ] Not started

---

## Implementation Order

```
Phase 1 (keypath.go extraction)         → ~1 session, no behavior change
Phase 2 (leaf unification)               → ~2-3 sessions, verify tests pass
Phase 3 (traversal unification)          → ~3-4 sessions, verify tests pass
Phase 4 (putItem/compare dedup)          → ~1-2 sessions, verify tests pass
Phase 5 (file cleanup)                   → ~1 session
```

## Verification

After each phase:
- `just test-core` green
- `just test-libs` green  
- `just build` green
- `just conformance` green
- `go vet ./shamap/...` clean
