# goXRPL Performance Bottleneck Analysis

**Date**: 2026-03-11
**Scope**: Full codebase audit across SHAMap, TX Engine, Storage, Codec, Ledger Management, and Consensus layers.

---

## Table of Contents

1. [Critical Bottlenecks](#1-critical-bottlenecks)
2. [High Severity Bottlenecks](#2-high-severity-bottlenecks)
3. [Medium Severity Bottlenecks](#3-medium-severity-bottlenecks)
4. [Low Severity Bottlenecks](#4-low-severity-bottlenecks)
5. [Architectural Defects](#5-architectural-defects)
6. [Quick Wins](#6-quick-wins)
7. [Recommended Fix Order](#7-recommended-fix-order)

---

## 1. Critical Bottlenecks

### 1.1 Binary Codec Round-Trips in Transaction Threading

**Location**: `internal/tx/threading.go:46-90`

Every modified/inserted/erased entry during the threading phase performs a full codec round-trip:

```
bytes → hex.Encode → binarycodec.Decode → modify fields → binarycodec.Encode → hex.Decode → bytes
```

This is called for every touched entry in `apply_state_table.go:404` (insert), `:416` (modify), and `:435-472` (threadOwners). A Payment transaction touching 100+ entries triggers 400+ codec operations.

**Estimated overhead**: 30-40% of Apply() phase time.

**Fix**: Cache decoded fields during the transaction lifecycle. Work on Go structs directly instead of round-tripping through the binary codec.

---

### 1.2 Triple Decode in Metadata Generation

**Location**: `internal/tx/apply_state_table.go:547-709`

For each AffectedNode in transaction metadata, entry fields are decoded multiple times:

- `buildModifiedNode` (line 575-595): decodes both original and current via `extractLedgerFields()`
- `extractLedgerFields()` at `field_metadata.go:111-134`: another full `binarycodec.Decode` per call

Average transaction creates 1-5 AffectedNodes, each triggering 2-3 full binary decodes. No caching of decoded entry state exists — the same binary data is decoded multiple times in the same call stack.

**Estimated overhead**: 20-30% of metadata generation time.

**Fix**: Cache decoded entry state during ApplyStateTable lifetime. Decode once, reuse everywhere.

---

### 1.3 BinaryParser.ReadBytes() — Byte-by-Byte Allocation

**Location**: `codec/binarycodec/serdes/binary_parser.go:118-128`

```go
func (p *BinaryParser) ReadBytes(n int) ([]byte, error) {
    var bytes []byte
    for i := 0; i < n; i++ {
        b, err := p.ReadByte()
        bytes = append(bytes, b)  // allocates on every iteration
    }
    return bytes, nil
}
```

Every field read (Amount = 8-48 bytes, Hash256 = 32 bytes, PathSets = N*32 bytes) triggers N separate allocations via `append`. A transaction with 20+ fields averages 600+ allocations just from this function.

**Fix**: Pre-allocate with `make([]byte, n)` and use indexed assignment. Trivial change, major win.

---

### 1.4 Regex Compiled Per-Call in Amount Processing

**Location**: `codec/binarycodec/types/amount.go:351,580,695`

```go
regexp.MustCompile(`\d+`)       // line 351 — compiled per XRP validation
regexp.MustCompile(IOUCodeRegex) // line 580 — compiled per currency serialization
regexp.MustCompile(IOUCodeRegex) // line 695 — compiled per hex validation
```

Each Amount serialization/deserialization triggers 1-3 regex compilations. Transactions routinely have 5-10 amounts, so 10 transactions = 50-100+ regex compilations per block.

**Fix**: Pre-compile as package-level variables.

---

### 1.5 SHAMap RWMutex Held During Storage I/O

**Location**: `shamap/shamap.go` — `Get()`, `Put()`, `Delete()`

SHAMap operations hold the top-level `sm.mu` RWMutex while `descend()` may call `family.Fetch()` which blocks on Pebble disk reads (potentially 10ms+ latency). This blocks ALL concurrent access to that SHAMap during I/O.

```go
func (sm *SHAMap) Get(key [32]byte) (*Item, bool, error) {
    sm.mu.RLock()
    defer sm.mu.RUnlock()
    item, err := sm.findItem(key)  // may call descend() → Fetch() → disk I/O
    ...
}
```

Since there is a single SHAMap per ledger, one transaction blocking on I/O during SHAMap traversal blocks all other transactions and RPC reads.

**Fix**: Release lock before I/O operations, reacquire after. Or use lock-free read patterns (RCU).

---

### 1.6 Unbounded Memory Growth in Ledger Service

**Location**: `internal/ledger/service/service.go`

Two maps grow without bound:

```go
ledgerHistory map[uint32]*ledger.Ledger  // never evicted
txIndex       map[[32]byte]uint32        // never cleared
```

At 10s per ledger close, this retains ~600K ledgers per week. Each ledger holds SHAMap references (~10MB+). The txIndex at 1000 TPS accumulates 86.4M entries per day.

**Fix**: Use LRU cache with bounded size, or implement eviction policy.

---

## 2. High Severity Bottlenecks

### 2.1 ApplyStateTable.Succ() — O(n) Per Call

**Location**: `internal/tx/apply_state_table.go:221-276`

The `Succ()` method iterates the entire `items` map for every successor lookup:

```go
for k, entry := range t.items {
    if bytes.Compare(k[:], key[:]) > 0 {
        if !found || bytes.Compare(k[:], bestKey[:]) < 0 {
            bestKey = k
            ...
        }
    }
}
```

Additionally, a retry loop for deleted entries can call `base.Succ()` repeatedly. With 1000 entries and 500 deleted, sequential `Succ()` calls can trigger O(n^2) behavior. This is common in order book traversal during Payment transactions and pathfinding.

**Estimated overhead**: 15-25% in Payment/pathfinding scenarios.

**Fix**: Replace `map[[32]byte]*TrackedEntry` with a sorted data structure (skip list or B-tree) for O(log n) Succ().

---

### 2.2 TxQ.byFee — O(n) Insertion, O(n^2) Rebuild

**Location**: `internal/txq/txq.go:140-161, 227-235`

Transaction queue insertion uses sorted slice with `copy()`:

```go
q.byFee = append(q.byFee, nil)
copy(q.byFee[pos+1:], q.byFee[pos:])  // O(n) copy
q.byFee[pos] = c
```

On parent hash change, the entire `byFee` slice is rebuilt from scratch by iterating all account queues and re-inserting — O(n^2) total.

**Fix**: Use a heap or balanced tree.

---

### 2.3 Service.AcceptLedger() Lock Hold Time

**Location**: `internal/ledger/service/service.go:278-383`

`AcceptLedger()` holds a write lock for the entire ledger close cycle:

1. Calls `Close()` on ledger
2. Calls `SetValidated()`
3. Persists to nodestore
4. Iterates all transactions for callbacks (`collectTransactionResults`, lines 409-432)
5. Creates new open ledger

The lock is held for 100-500ms depending on transaction count. During this time, all RPC reads that need `Service.mu.RLock()` are blocked.

**Fix**: Minimize critical section. Move transaction iteration and event callbacks outside the lock.

---

### 2.4 Consensus EventBus — Small Buffer, Silent Drops

**Location**: `internal/consensus/events.go`

The event bus uses a 100-event buffered channel with silent drops:

```go
func (eb *EventBus) Publish(event Event) {
    select {
    case eb.eventCh <- event:
    default:
        // Channel full — event silently dropped
    }
}
```

With 30 validators, a single consensus round generates 30+ proposals + 30 validations = 60+ events per phase. Sequential processing in `run()` means one slow subscriber blocks all others.

**Fix**: Increase buffer to 1000+, add priority queue for critical events, log drops.

---

### 2.5 Nodestore Cache — Undersized Default

**Location**: `storage/nodestore/database.go:49-50`

```go
CacheSize: 2000  // only 2000 nodes cached
```

For a ledger with 10+ million objects, 2000 cached nodes yields a ~0.02% hit rate. Deep SHAMap traversals (account scanning, offer book walking) miss cache constantly, hitting Pebble for every node.

**Fix**: Scale default to 50K-200K based on available memory.

---

### 2.6 Repeated Hash Computation in FindDifference

**Location**: `shamap/shamap.go:1316-1489`

`FindDifference()` calls `Hash()` multiple times per tree traversal. For large trees, this means computing/retrieving hashes repeatedly for the same nodes without caching. `Hash()` at the root level triggers cascading recomputation through dirty nodes.

**Fix**: Cache root hash with a version counter to detect staleness. Memoize intermediate hash results.

---

### 2.7 SHAMap Write Lock Blocks All Readers

**Location**: `shamap/shamap.go:413-550`

`Put()`, `PutItem()`, and `Delete()` all hold WRITE lock for the entire tree traversal from root to leaf. This blocks ALL concurrent readers while traversing the entire tree depth.

**Fix**: Use read lock for traversal, only acquire write lock at the leaf level. Or use optimistic concurrency.

---

### 2.8 No Batch Writer Implementation

**Location**: `storage/nodestore/batch.go:62-146`

The `BatchWriter` is a stub that does synchronous writes:

```go
func (bw *BatchWriter) Write(hash Hash256, data []byte) <-chan error {
    status := bw.backend.Store(node)  // synchronous — no batching
}
```

Rippled accumulates writes in a batch buffer and flushes periodically. goXRPL sends each write individually, even when `StoreBatch()` is called. For ledger close with 50K node writes, Pebble sees 50K individual `Set` calls instead of one batch commit.

**Fix**: Implement actual batch accumulation with periodic flush.

---

### 2.9 FlushDirty Per-Node Lock Thrashing

**Location**: `shamap/shamap.go:1199-1246`

`flushNode()` acquires `inner.mu.Lock()` for every inner node during dirty flush, then iterates 16 branches inside the lock. This creates 16 lock cycles per inner node in the flush path.

**Fix**: Batch lock acquisitions. Acquire once per subtree, not per node.

---

### 2.10 JSON Map Intermediary in Serialization

**Location**: `codec/binarycodec/` — Encode path

The serialization path `Go struct → JSON map → binarycodec.Encode() → hex → bytes` creates excessive intermediate allocations:

1. `flatten.go:263-327` — `structToMap()` creates nested maps via reflection
2. `st_object.go:166` — `createFieldInstanceMapFromJson()` creates `map[FieldInstance]any`
3. `st_object.go:39` — `getSortedKeys()` allocates `[]FieldInstance` slice

A Payment with paths creates 50-100+ map allocations for a single transaction.

**Fix**: Implement direct struct-to-binary serialization bypassing JSON intermediary.

---

## 3. Medium Severity Bottlenecks

### 3.1 zeroHash Allocated Per Call

**Location**: `shamap/inner_node.go:166, 251`

```go
zeroHash := make([]byte, 32)  // allocated in updateHashUnsafe() and SerializeWithPrefix()
```

Called for every inner node update and serialization. For a 1M-node tree with frequent updates, this is ~32MB of unnecessary allocations.

**Fix**: Use a package-level constant: `var zeroHash [32]byte`.

---

### 3.2 Per-Child RWMutex in InnerNode.Child()

**Location**: `shamap/inner_node.go:78-86`

```go
func (n *InnerNode) Child(index int) (Node, error) {
    n.mu.RLock()
    defer n.mu.RUnlock()
    return n.children[index], nil  // lock for a single array read
}
```

Every `descend()` call in hot paths triggers a lock/unlock for a single array dereference. At scale (millions of tree operations), this serializes concurrent readers unnecessarily.

**Fix**: Use `ChildUnsafe()` where outer lock is already held. Or use atomic operations.

---

### 3.3 Hex Double-Pass (ToUpper After Encode)

**Location**: Throughout `codec/binarycodec/types/*.go`

```go
strings.ToUpper(hex.EncodeToString(buf[:]))
```

Two passes over every hash/amount field: one to encode, one to uppercase. For metadata with 20 affected nodes x 32-byte hashes = 40 string allocations.

**Fix**: Use a single-pass uppercase hex encoder.

---

### 3.4 Defensive Byte Copies in Storage Layer

**Location**: `storage/kvstore/memorydb/memorydb.go:52-54,64-66` and `storage/kvstore/pebble/pebble.go:258-270`

Every Get/Put/Iterator operation copies all bytes defensively:

```go
result := make([]byte, len(val))
copy(result, val)
```

For 10K operations/sec with 1KB average node size = 10MB/sec of allocation overhead just for copying.

**Fix**: Use borrowed references with proper lifecycle management where safe.

---

### 3.5 No sync.Pool Usage Anywhere

**Location**: Global — across storage, SHAMap, codec

Zero use of `sync.Pool` for high-frequency allocations:
- Batch objects in `StoreBatch()` loops
- Node stacks in SHAMap traversals
- Temporary byte slices in codec encoding
- Cache entries

For 50K nodes/sec during ledger close, this creates significant GC pressure.

**Fix**: Add sync.Pool for byte buffers, node objects, and codec scratch space.

---

### 3.6 Reflection in Flatten Hot Path

**Location**: `internal/tx/flatten.go:127-211`

`ReflectFlatten()` uses reflection for every transaction serialization:
- `getFlattenInfo()` uses sync.Map but still reflection-based (line 135)
- 15-30 reflection operations per transaction
- `fmt.Sprintf("%X", val.Uint())` for every UInt64 field (line 196) — allocates string

**Fix**: Generate type-specific flatten functions. Or use `encoding/json` tags with a cached field map.

---

### 3.7 fieldsEqual() via fmt.Sprintf

**Location**: `internal/tx/apply_state_table.go:892-911`

```go
func fieldsEqual(a, b any) bool {
    // ... map comparison ...
    return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)  // fallback
}
```

Called for every field change in ModifiedNode. Allocates and serializes complex structures as strings for comparison.

**Fix**: Use typed comparisons or `reflect.DeepEqual` (still not great, but avoids string allocation).

---

### 3.8 Negative Cache O(n) Eviction

**Location**: `storage/nodestore/negative_cache.go:175-214`

When the negative cache reaches `maxSize` (100K entries), eviction scans ALL entries:

```go
for hash, exp := range nc.entries {  // scan all 100K
    // find oldest 10K entries to evict
}
```

A single `MarkMissing()` call when cache is full triggers a 100K-entry scan + 10K deletes.

**Fix**: Use LRU list like the positive cache instead of O(n) scan.

---

### 3.9 time.Now() Under Lock

**Location**: `storage/nodestore/cache.go:20,98,108,150` and `negative_cache.go:91,115,119`

TTL expiry checks call `time.Now()` (a syscall) inside locked sections:

```go
func (e *cacheEntry) isExpired() bool {
    return time.Now().After(e.expiresAt)  // syscall inside lock
}
```

On high-frequency operations (1000s/sec), this adds latency while holding the lock.

**Fix**: Cache `time.Now()` locally per batch of operations. Check expiry outside the lock.

---

### 3.10 Negative Cache Double Lock

**Location**: `storage/nodestore/negative_cache.go:114-127`

`IsMissing()` does RLock → check → drop → WLock → delete (lock convoy pattern):

```go
nc.mu.RLock()
expiresAt, found := nc.entries[hash]
nc.mu.RUnlock()
// gap here — state may change
nc.mu.Lock()  // reacquire for write
if exp, ok := nc.entries[hash]; ok && time.Now().After(exp) {
    delete(nc.entries, hash)  // another time.Now() call
}
nc.mu.Unlock()
```

**Fix**: Use single lock acquisition, or accept small window of stale entries with RLock only.

---

### 3.11 Pebble Iterator Defensive Copies

**Location**: `storage/kvstore/pebble/pebble.go:253-270`

```go
func (i *iterator) Key() []byte {
    k := i.iter.Key()
    cp := make([]byte, len(k))
    copy(cp, k)
    return cp
}
```

Pebble returns borrowed references valid until `Next()`, but the wrapper copies defensively on every `Key()` and `Value()` call. Iterating 1000 keys = 2000 allocations.

**Fix**: Document lifecycle requirements and avoid copy where callers don't retain references.

---

### 3.12 MemoryDB Iterator Eagerly Snapshots Everything

**Location**: `storage/kvstore/memorydb/memorydb.go:89-121`

`NewIterator` collects ALL matching keys into memory, sorts them, and copies all values:

```go
for k := range m.db {
    if strings.HasPrefix(k, prefixStr) {
        keys = append(keys, k)  // unbounded
    }
}
sort.Strings(keys)
// then copy all values
```

For prefix scan of 1000 items with 1KB values = 1MB transient allocation per iterator.

**Fix**: Use lazy iteration for test DB, or accept this for test-only usage.

---

### 3.13 STArray Object Allocation per Element

**Location**: `codec/binarycodec/types/st_array.go:43-54`

```go
for _, v := range json.([]any) {
    st := NewSTObject(serdes.NewBinarySerializer(serdes.NewFieldIDCodec(...)))
    b, err := st.FromJSON(v)
    sink = append(sink, b...)
}
```

Creates new STObject + FieldIDCodec per array element. PathSet with 10 paths x 5 steps = 50+ instances created/discarded.

**Fix**: Reuse STObject across iterations. Pre-allocate sink.

---

### 3.14 getLedgerEntryType Manual Parsing with String Allocations

**Location**: `internal/tx/apply_state_table.go:712-876`

Manual byte-by-byte binary parsing to extract entry type, called multiple times per entry:
- `ledgerEntryTypeName()` returns string constants (60-line switch)
- `fmt.Sprintf("Unknown(0x%04x)", code)` for unknown types (allocation)
- Called 2-3 times per entry during threading and metadata

**Fix**: Cache entry type per TrackedEntry. Parse once on insertion.

---

### 3.15 String Allocations in Flatten

**Location**: `internal/tx/flatten.go:196,203,315,321`

```go
m[f.name] = fmt.Sprintf("%X", val.Uint())                    // per UInt64 field
m[f.name] = strings.ToUpper(hex.EncodeToString(buf[:]))       // per Hash256 field
```

15+ string allocations per transaction just for field formatting.

**Fix**: Use pre-formatted lookup tables or write directly to binary.

---

## 4. Low Severity Bottlenecks

### 4.1 Dynamic Stack Growth in Iterators

**Location**: `shamap/iterator.go:107-111`, `shamap/shamap.go:1346`

Iterator and `FindDifference()` stacks grow dynamically via `append()`. For deep trees, this triggers slice doubling and reallocation. Some stacks have `MaxDepth` hints available but don't use them.

**Fix**: Pre-allocate stacks with `make([]T, 0, MaxDepth)`.

---

### 4.2 Redundant Branch Checks with Per-Branch Locking

**Location**: `shamap/inner_node.go:60-68`, `shamap/shamap.go:869-881`

`onlyBelow()` loops through all 16 branches calling `IsEmptyBranch()`, each acquiring RWMutex for a single bit check. `BranchCount()` calls `bits.OnesCount16()` which is O(1) but could be cached.

**Fix**: Use bitmap checks without per-branch locking. Cache branch count on write.

---

### 4.3 Debug fmt.Println in Production Codec Path

**Location**: `codec/binarycodec/serdes/field_id_codec.go:53-56`

```go
fmt.Println(err)
fmt.Println(len(b))
```

Debug prints in the hot path of every field decode. Causes I/O syscalls for no purpose.

**Fix**: Remove immediately.

---

### 4.4 FindDifference Linear Filter

**Location**: `shamap/shamap.go:1541-1553`

Linear filter loop to exclude a single key from results. For large trees with many differences, this is repeated many times.

**Fix**: Use a set or hash map for O(1) lookup.

---

### 4.5 Redundant Entry Type Extraction in Threading

**Location**: `internal/tx/apply_state_table.go:395-431`

Entry type extracted twice — once from Current, once from Original (if Current is nil), then again inside `threadItem` via the binary codec.

**Fix**: Store entry type in TrackedEntry struct. Extract once on creation.

---

### 4.6 Multiple X-Address Processing Passes

**Location**: `codec/binarycodec/types/st_object.go:117-163`

Two passes over JSON fields to handle X-addresses: first creating a `processedJSON` map, then checking addresses again.

**Fix**: Single-pass processing.

---

### 4.7 Unbounded Goroutine Creation on Ledger Close

**Location**: `internal/ledger/service/service.go:348,366`

```go
go hooks.OnLedgerClosed(...)
for each tx:
    go hooks.OnTransaction(...)  // 1000 goroutines for 1000 txs
```

No worker pool, no limit on concurrent goroutines.

**Fix**: Use a bounded worker pool (10-50 goroutines).

---

### 4.8 Encoding Overhead in StoreBatch

**Location**: `storage/nodestore/kvstore_database.go:190-216`

Each node in `StoreBatch()` is individually encoded with `encodeNodeData()` which allocates a new `[]byte` per node:

```go
for _, node := range nodes {
    encoded := encodeNodeData(node)  // new allocation each iteration
    batch.Put(node.Hash[:], encoded)
}
```

For 50K nodes during ledger close = 50K allocations.

**Fix**: Use sync.Pool for encoding buffers.

---

## 5. Architectural Defects

### 5.1 No Lock Ordering Documentation

SHAMap has nested mutexes (SHAMap.mu → InnerNode.mu → LeafNode.mu). Service.mu protects TxQ access. TxQ.mu protects ledger access. No document specifies acquisition order. Deadlock risk exists if ordering is violated anywhere.

**Action**: Document lock hierarchy. Add deadlock detection in tests.

---

### 5.2 No Backpressure Between Consensus and Ledger

`BuildLedger()` and `StoreLedger()` adaptor calls have no backpressure mechanism. If ledger operations are slow, consensus proceeds regardless.

**Action**: Implement feedback channel from ledger to consensus.

---

### 5.3 Cache Duplication

```
LedgerCache (LRU, 256 ledgers)
+ Service.ledgerHistory (unbounded map)
= duplicate storage + inconsistency risk
```

**Action**: Remove ledgerHistory, use LedgerCache exclusively.

---

### 5.4 No Shared Node Cache Across SHAMaps

Each SHAMap caches deserialized nodes independently. If 5 concurrent SHAMaps traverse the same tree (e.g., snapshots for different RPC clients), each deserializes nodes independently — 5x the work.

**Action**: Implement shared read-only node cache at the nodestore level.

---

## 6. Quick Wins

Fixes that can be done in under 1 hour each with high return on investment:

| # | Fix | Location | Effort | Impact |
|---|-----|----------|--------|--------|
| 1 | ReadBytes pre-allocation | `binary_parser.go:118` | 5 min | HIGH |
| 2 | Pre-compile regexes | `amount.go:351,580,695` | 5 min | HIGH |
| 3 | Remove debug prints | `field_id_codec.go:53-56` | 2 min | LOW |
| 4 | Package-level zeroHash | `inner_node.go:166,251` | 5 min | MEDIUM |
| 5 | Increase nodestore cache | `database.go:49` | 5 min | HIGH |
| 6 | Pre-allocate iterator stacks | `iterator.go:107`, `shamap.go:1346` | 10 min | LOW |
| 7 | Single-pass uppercase hex | `types/*.go` (17 occurrences) | 30 min | MEDIUM |
| 8 | Cache entry type in TrackedEntry | `apply_state_table.go` | 30 min | MEDIUM |

---

## 7. Recommended Fix Order

### Phase 1 — Quick Wins (1-2 days)

Apply all items from section 6. These are low-risk, high-reward changes that reduce allocation pressure across the board.

### Phase 2 — Codec Hot Path (1 week)

1. Eliminate binary codec round-trips in threading (1.1)
2. Cache decoded fields for metadata generation (1.2)
3. Remove JSON map intermediary from critical serialization paths (2.10)

Expected result: 30-50% reduction in Apply() phase time.

### Phase 3 — Data Structure Improvements (1 week)

1. Sorted ApplyStateTable for O(log n) Succ() (2.1)
2. Heap-based TxQ.byFee (2.2)
3. LRU-based negative cache eviction (3.8)

Expected result: O(n^2) → O(n log n) for payment/pathfinding workloads.

### Phase 4 — Concurrency Improvements (2 weeks)

1. Reduce AcceptLedger() lock hold time (2.3)
2. Lock-free SHAMap reads / release lock during I/O (1.5)
3. Bounded ledger history with eviction (1.6)
4. Increase EventBus buffer + priority queue (2.4)
5. Add sync.Pool for high-frequency allocations (3.5)
6. Document lock ordering (5.1)

Expected result: Reduced lock contention, bounded memory growth, better concurrent throughput.

### Phase 5 — Architectural (1+ month)

1. Direct struct-to-binary serialization (bypass JSON)
2. Shared node cache across SHAMaps (5.4)
3. Async ledger close with background transaction collection
4. Implement real batch writer for nodestore (2.8)
5. Backpressure between consensus and ledger (5.2)

Expected result: Production-ready throughput for high-volume ledger operations.
