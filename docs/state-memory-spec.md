# Flat state and bounded SHAMap memory

Status: Approved specification; bounded execution is mandatory. Date: 2026-10-12.

Related issue: [#2050](https://github.com/LeJamon/go-xrpl/issues/2050).
Implementation baseline: `fd84af198af71770e671de3457663193dd06079a`.

## 1. Decision and scope

Separate ledger execution state from the in-memory representation of its SHAMap commitment. Execute against ledger objects addressed directly by their 256-bit keys. Build the canonical SHAMap incrementally from changes using bounded traversal memory. Keep immutable serialized SHAMap nodes in NodeStore for acquisition, peer serving, historical views, and recovery.

A retained ledger identifies a version; it does not own an expanded decoded tree. Explicit operation handles own temporary working memory. Byte-accounted caches own reusable data independently of ledger lifetimes.

The first implementation keeps Pebble, the existing NodeStore format, and relational indexes. It adds one flat database for a selected durable state and bounded change layers above it. Historical or competing ledgers without a matching flat view remain readable through their retained SHAMap roots. A full flat archive with arbitrary historical MVCC queries is outside this first implementation.

This proposal extends [ADR 0004](adr/0004-storage-architecture.md); it does not change that accepted decision until the implementation gates below pass. The single-writer transaction engine remains in place.

### Required outcomes

- Decoded residency reaches a configured bound under repeated hot access and disjoint cold access.
- Ordinary transaction reads do not attach clean descendants to a long-lived ledger tree.
- Current state, retained history, fork views, acquisition, and queued persistence have explicit ownership and release rules.
- The implementation produces unchanged transaction results, metadata, ledger headers, state roots, transaction roots, and peer node bytes.
- Memory pressure has a defined progress path. It cannot silently discard dirty state or alter a transaction's protocol result.

Rewriting consensus, adopting MDBX, changing SHAMap hashing, parallelizing transaction application, and reducing configured history to conceal growth are outside scope.

## 2. Motivation and design precedents

At the baseline, normal reads attach decoded children, snapshots share roots, and path copies preserve untouched child pointers. `retainAcquisitionNode` only charges syncing maps. `StoreDirtyBatched` bounds serialization workspace, but retains the resulting clean graph. These mechanisms explain why dropping historical ledger handles alone cannot bound the latest tree's residency.

The issue's production profiles establish allocation trends, not a complete census of live owners. A local fixed-state diagnostic also reproduced growth in the latest map across disjoint read and mutation batches with zero acquisition loaded-node charge. Neither observation establishes infinite growth; both demonstrate missing residency control.

The useful precedent from Geth is explicit separation of recent change layers, persistence buffers, and clean caches. Its path-based archive design retains a persistent base and recent forward changes. A layer-count limit is not by itself a byte limit. [Geth design](https://geth.ethereum.org/docs/fundamentals/archive)

Erigon separates flat execution state from incremental commitment computation. Its commitment code expands and folds paths using a depth-bounded workspace. This specification adopts those architectural ideas while preserving XRPL's SHAMap structure and protocol behavior. It makes no claim that Ethereum performance measurements predict XRPL performance. [Erigon architecture](https://docs.erigon.tech/fundamentals/architecture), [commitment implementation](https://github.com/erigontech/erigon/blob/main/execution/commitment/hex_patricia_hashed.go)

## 3. Components and ownership

```text
Transaction engine
  -> transaction changes and rollback journal
  -> ledger builder
       -> flat base reader + immutable parent changes
       -> sorted final object changes
            -> incremental SHAMap builder
                 -> sealed ledger and immutable persistence segments
                      -> NodeStore durability barrier
                      -> flat values + version record + index outbox
                      -> index completion and durable publication

RPC / replay / peer serving
  -> explicit version handle
       -> matching flat view, or retained SHAMap root
       -> bounded operation workspace and shared caches
```

| Component | Owns | Lifetime and release |
| --- | --- | --- |
| Version descriptor | Ledger hash, parent hash, sequence, state root, transaction root, schema/epoch identity, completion state | Small metadata; sequence alone never identifies a version |
| Read lease | Descriptor, selected base snapshot or root reader, generation/dependency references, reserved scratch | One operation; explicit idempotent `Close` |
| Transaction changes | Writes, tombstones, original values and protocol metadata journal | Merge on successful application or discard on rollback |
| Ledger builder | Private current changes and references to immutable parent segments | Seal, cancellation or failed build |
| Sealed ledger | Immutable changes, node segments, header and transaction data | Until persistence completes and all dependent readers/builders release it |
| Shared caches | Immutable object values and optionally decoded node records | Admission and eviction by byte charge, independently of ledger history |
| Persistence worker | Reserved I/O workspace and a bounded set of sealed jobs | Durable completion, explicit cancellation handoff or retained retry |
| Retention service | Required roots, live disk segments and storage generation references | Release only when both configured history and active dependencies allow |

No finalizer is part of the correctness or budget-release protocol. Tests may use leak detection, but normal completion must close every lease.

## 4. Flat state representation and versioned reads

### Persistent layout

Use a separate Pebble namespace/database with these logical records:

- `object/<32-byte ledger key>`: canonical serialized SLE bytes.
- `meta/current`: schema version, state epoch, exact ledger hash, parent hash, sequence, state root, transaction root, commit identifier, and the identity of the durable backing evidence.
- `meta/advancing`: present only during a large transition; contains marker schema/checksum, attempt ID, source/target semantic descriptors, change-derivation algorithm version, expected ready-descriptor digest and writer epoch. Its presence makes the latest flat view unavailable to new readers.
- `outbox/<semantic commit ID>`: a bounded record referencing the durable header/transaction root and publication intent needed to complete relational indexing idempotently. Transaction payloads are streamed from durable nodes, not embedded as an unbounded outbox value.

Object keys use bytewise ledger-key ordering. The encoding must not use host endianness or string collation. Absence means no object; an overlay deletion is an explicit tombstone and must mask all older values.

A small transition writes `meta/current` and its object changes in the same atomic, synced batch, admitted against a batch byte ceiling. Larger transitions use the unavailable-during-advance protocol in section 7; they must not reconstruct a spilled ledger into an unbounded batch. A reader must never combine objects from one base version with another version's descriptor. Store schema and epoch identity explicitly so a rebuilt base cannot alias cached values from an earlier database incarnation.

The flat store contains trusted local materializations derived from verified roots and accepted changes. It is not an independently authenticated network input. Checksums protect stored records; invalid schema, descriptor mismatch, corruption, or incomplete import causes the materialization to be quarantined and rebuilt or bypassed. Hash verification remains mandatory for network node input and root-backed reads.

### Capturing a view

Opening a view atomically captures the flat descriptor, readiness marker and a consistent Pebble snapshot, then selects a bounded chain of immutable changes from that base to the requested version. Capture is linearizable: under the short base-capture guard check in-memory readiness, copy the expected descriptor/epoch, create the snapshot and register its lease. Release the guard before reading the snapshot metadata. Verify that its descriptor matches the expected version and its advancing marker is absent; on mismatch close and retry/fall back. Both commit paths install an in-memory writer reservation under the same guard before writes and install the new ready descriptor only after Sync. Callbacks and storage I/O occur after releasing the guard. A builder uses that stable view for its execution lifetime. It cannot observe a base advancing underneath it. A snapshot containing `meta/advancing` is ineligible for flat reads; the caller uses its requested authenticated root and preserves any local changes above that root.

Ledger-history entries retain root descriptors, not permanent Pebble snapshots. Historical RPC opens a scoped view when requested. If the current flat base cannot serve that exact lineage, it uses a non-retaining SHAMap reader at the requested root. The same fallback applies to forks whose common ancestor predates the available flat base. Capture the requested root and its retention lease as one operation, retain it through reader/iterator close, and merge any local changes above that same version. The root-reader adapter implements point and ordered access with bounded, non-retaining traversal; invoking existing retaining `Get`/iteration paths behind that adapter would recreate the original defect.

Every change layer records its exact parent version. Siblings at the same sequence remain distinct. A root is insufficient to identify a ledger because different ledgers can share an unchanged state root. Ledger version identity is the ledger hash with its exact header/roots. A view also carries a flat schema/epoch identity; rebuilding the flat base changes that view identity without creating a new ledger version. The stable semantic commit ID is derived from the ledger hash and publication-intent kind, excluding flat epoch and retry attempt ID. It keys both the outbox and relational idempotency record, so retries or rebuilds cannot create duplicate publication obligations. Attempt IDs only identify worker/recovery attempts.

### Lookup and ordered access

Point lookup searches the transaction-local state-change journal, current builder changes, sealed ancestors from newest to oldest, then the stable base. A tombstone terminates lookup. Cache keys must identify the immutable value or the exact version and ledger key; a cache keyed only by ledger-object key is invalid across versions.

Ordered iteration merges the base iterator and sorted overlay streams with newest-write precedence and deletion suppression. `Succ(k)` returns the smallest visible key strictly greater than `k`, matching the existing contract. Bounds, empty results, iterator errors, and cancellation propagate without converting storage failure into object absence.

The merge frontier is bounded by the admitted number of streams. Each stream and its metadata are charged. Large overlays spill into sorted immutable runs; bounded fan-in merges compact them before a configured chain/stream limit is reached. Spill files are indexed on disk for point lookup. Their indexes cannot become an uncharged all-keys heap map. Compaction cannot mutate segments still used by an older view.

### Branch selection and rebase

Flat-base advancement is serialized and checks that the expected parent descriptor equals `meta/current`, including ledger/parent hashes, both roots, epoch and schema identities. Sequence or state-root equality alone is insufficient. A sibling batch cannot overwrite it. A newly selected branch uses a root-backed view or a verified rebuilt flat base when no suitable flat lineage exists. Switching epochs publishes a fully prepared descriptor atomically; existing leases finish against their old base. Epoch retirement waits for bounded active dependencies, not every historical ledger descriptor.

## 5. Transaction and ledger changes

Adapt the existing read/write and atomic-apply contracts before changing transaction implementations. Preserve the engine's transaction ordering, amendment rules, fee handling, invariant checks, and retry behavior.

A transaction reads its own writes and can roll back all local changes. Nested sandboxes preserve their current merge/discard semantics. Applying a transaction atomically publishes its state changes and transaction/metadata effects together to the ledger builder. An error leaves the parent view unchanged. Preserve `ApplyStateTable` original/current/deleted-final bytes and fee-only recovery: a `tec` path may discard the full transaction table and apply a separate fee-only table. Destroyed XRP, state writes and transaction/metadata insertion belong to the same atomic ledger publication unit. This is logical in-process atomicity of the sealed version; cross-database durability follows the separate ordered protocol in section 7. Skiplist and NegativeUNL updates also use the staged writer.

Keep transaction metadata and original-value tracking separate from the final ledger delta. Multiple transactions may create, change and delete one object; coalescing the final delta must not remove affected-node metadata, owner-count effects, or intermediate values needed by invariants. Coalesce only after the protocol consumers of each transaction's changes have completed. Transfer committed results from the transaction journal into the ledger delta at that boundary; never reuse a ledger-coalesced original value as the next transaction's original value.

The transaction SHAMap is separate from the account-state SHAMap. It contains the existing transaction/metadata leaf encodings and does not inherit the parent ledger's transaction set. Preserve raw versus metadata-bearing leaf kinds, duplicate detection, counts and `TxExists` errors; transaction lookup must not consult account-state overlays. Build it with bounded sorted/spilled entries using the same canonical node encoder where applicable. Transaction records live in per-version pending segments indexed by transaction hash and then in the canonical transaction SHAMap; this draft does not add them to `object/` or introduce a redundant flat transaction table. The segment descriptor records version, root and count; each entry preserves leaf kind and exact bytes. `ForEachTransaction`, `TxExists` and counts resolve the same captured version as its state reader.

Private builder-owned data may be updated in place. Once sealed or exposed to another view, a segment is immutable. Existing `MutableFork` and `DetachedMutable` consumers must migrate to explicit view/builder ownership; deep-copying the resident state is not an acceptable replacement.

## 6. Incremental SHAMap commitment

### Inputs and outputs

Run commitment independently for account state and transactions, then seal both roots in one ledger descriptor. Account-state inputs are a verified parent root, an exact parent version, and final object changes sorted by their 256-bit SHAMap key. The transaction builder starts from the empty transaction map and its independently sorted transaction/metadata entries. Values use unchanged canonical SLE serialization. Output is the new root plus canonical serialized nodes needed to reproduce and serve that root. The builder also returns a bounded segment descriptor referencing those new nodes and the object delta; it becomes durable only through section 7. Descriptor indexes are paged/spillable rather than listing every node in memory.

### Algorithm

1. Merge the changed keys in ascending order, eliminating superseded writes only after transaction metadata is complete.
2. Traverse the parent's affected paths. Load and verify missing branch records by hash using a scoped read handle. Preserve untouched child hashes without expanding their descendants.
3. Apply inserts, replacements and deletions using the exact SHAMap leaf and inner-node rules. Splitting or collapsing a branch may require reading a surviving sibling; admit that work within the same bounded cursor.
4. When no remaining changed key can touch a subtree, compute its canonical hash, emit its node bytes to the bounded output writer, and collapse the cursor entry to its hash.
5. Finish at the root, checking all output writes and iterator errors before returning a sealed result. Every changed node is emitted with the correct map/leaf kind and canonical encoding, and storage records preserve their ledger sequence. Unchanged hash-only branches retain their parent closure dependencies; emitting a new root does not prove that those descendants are durable.

The resident traversal frontier is bounded by SHAMap depth and branch factor, not by total state size. This bound excludes sorting, output buffers, individual payloads and concurrently running builders; those are separately admitted and spillable. There must be no hidden collection of every decoded visited node or every output node.

Clean decoded inner records contain branch hashes and immutable metadata. They must not own arbitrary decoded descendants. Optional decoded caching is shared and byte-bounded. A traversal may keep bounded local handles for its current path. Cache admission is optional: a verified read may use operation workspace and release it without warming the cache.

Empty-tree roots, single-leaf placement, collision paths, deletion collapse, leaf type prefixes, state leaves, transaction leaves, and wire/prefix serialization are compatibility requirements. Ethereum's Patricia compression and its account/storage trie layout must not be imported into SHAMap.

### Work and fairness

Commitment is incremental over changed paths and any required sibling resolution. A routine ledger must not trigger a full-state walk. Large updates use resumable work outside consensus/service publication locks, with cancellation and reserved progress capacity. The logical algorithm remains deterministic regardless of chunk size, cache admission, spill threshold or scheduling.

## 7. Sealing, persistence and crash recovery

Distinguish three states:

| State | What may be used | What remains owned |
| --- | --- | --- |
| Building | Private mutable transaction/ledger view | Builder changes and base lease |
| Sealed | Immutable root and contents usable by explicitly dependent in-process work | All unpersisted node/delta segments and dependency leases |
| Durable | Root and required node closure survive restart; flat materialization is either matching or explicitly unavailable | Retention obligations and any active readers; RAM cache residency is optional |

Sealed does not mean validated, canonical, or durably published. Preserve the existing consensus and trusted-validation eligibility checks independently of these storage states.

### Ordered commit protocol

NodeStore, flat Pebble, and relational storage are independent durability domains. Use ordering and an idempotent commit record; do not assume a cross-database atomic batch.

1. Acquire a root-closure/reference lease covering the parent dependencies and destination writes. This is not a global `AcquireDurableSnapshot` lock. The lease remains valid through Sync and transfers to published descriptors/outstanding outbox records before pending ownership is released.
2. Write changed state/transaction nodes, header and any payload needed to reconstruct the commit/outbox. Synchronize NodeStore and persist the root-specific closure evidence described in section 8. A cache hit or successful unsynced batch is not a durability certificate. Failed or ambiguous Sync retains pending ownership, suppresses flat publication and retries under the lease; restart must recover/validate closure evidence before advancing.
3. For an eligible selected parent-to-child transition, apply flat mutations, `meta/current`, and the outbox in one synced flat batch if they fit the reserved batch ceiling. Otherwise use bounded advancement below. A superseded branch's nodes may remain durable without advancing the flat base.
4. Complete required relational writes in an idempotent relational transaction keyed by commit ID and checked against exact header/roots/publication intent. Stream its payload through bounded batches; transaction atomicity does not authorize an unbounded Go slice. An existing ID with conflicting contents is a corruption error. Clear the outbox only after that relational commit succeeds; uncertain outcomes are resolved by its committed ID before retry.
5. Release pending-write ownership after durability and dependent-reader checks. Cache admission of clean records is optional. Keep root retention independently of memory-cache residency.

Flat readiness and indexed publication are distinct: a ready flat version may serve exact-version execution/state reads while its outbox is index-pending; indexed RPCs and persisted history/tip announcements remain at the last index-complete version or return the existing not-ready behavior. Once indexing completes, the coordinator may publish those dependent capabilities. Flat advancement does not independently authorize consensus validation or indexed visibility. The outbox owns the required header, state/transaction roots and node closure until acknowledgement.

The foreground may publish an in-process sealed ledger through existing acceptance rules while persistence proceeds asynchronously. It must retain all dependencies. It must not advertise a durable checkpoint or restart-safe position prematurely. Publication locks contain only identity checks and pointer/descriptor swaps, never sorting, bulk I/O, waits for memory or full-state verification.

### Bounded advancement for large deltas

Only one flat writer advances a base. Under a short capture guard it installs an in-memory writer reservation and marks the base unavailable to new flat readers. It releases that guard, then synchronously persists `meta/advancing` with source/target identities before modifying any object. A failed or uncertain marker Sync stops object writes until the marker outcome is reconciled. Every queued write carries the writer epoch; cancellation/takeover joins the old writer and fences stale callbacks before new writes are admitted. Database process exclusivity prevents concurrent restart owners. Existing reader snapshots still see the complete old base. New operations use the root-backed path. Apply the immutable delta in admitted batches; do not release its ownership until completion or recovery handoff. This first version persists no chunk cursor: recovery replays deterministic puts/tombstones from the beginning. Each repeated operation is idempotent, and no recovery step requires all changes in memory.

After all object writes, atomically replace `meta/current`, create the bounded outbox record and remove `meta/advancing` in a synced final batch. The backend contract must guarantee that this synchronization covers all preceding chunks in that database. Only then re-enable new flat readers. No publication/capture lock is held during I/O.

A crash with the advancing marker present makes the flat base unusable, even though its old descriptor remains. That old descriptor is metadata only: after restart it cannot provide an old-state read because pre-advance Pebble snapshots no longer exist. Reconstruct the delta from the two retained roots with bounded paired traversal and replay it from zero, or rebuild from the target root. Temporary delta files are only an optimization and are not needed for crash correctness. Source/target roots stay in the durable retention inventory until the marker is cleared. Repeated application of writes and tombstones is idempotent. The fast single-batch and bounded multi-batch paths must have identical final contents and version identity.

### Failure cases

| Failure point | Required recovery |
| --- | --- |
| Before NodeStore Sync | Retain/spill immutable pending changes for retry; no durable publication |
| After node Sync, before flat batch | Old flat base remains authoritative; replay the batch or use the durable root-backed view |
| Flat batch fails or its outcome is uncertain | Reopen/read its atomic commit identifier and advancing marker; use only a ready version and repair an unfinished advance |
| Flat batch committed, relational update incomplete | Replay the durable outbox before exposing dependent indexed results |
| Cancellation while an I/O worker still reads segments | Request cancellation, then release ownership only after worker completion/handoff |
| Crash with temporary spill files | Discard unreferenced files; preserve only files included in a durable manifest or rebuild from the last published durable root |
| Missing backing node after publication | Treat as data-integrity failure; stop affected publication/recovery and reacquire explicitly, never report an absent ledger object |

Recovery reads the last committed descriptor, advancing marker and outstanding outbox before starting normal publication or generation retirement. Restore their required roots into the retention inventory first; transfer pending in-process root leases to that durable inventory before releasing them. A flat base is rebuildable from an authenticated retained root. Steady-state restart and rotation should reuse generation-bound completeness evidence; a base import/rebuild may do a deliberate full verification, but routine ledger publication must not.

## 8. Rotation and retained history

Memory leases and disk retention are separate. Evicting a decoded node is allowed only when its bytes can be reloaded through a valid durable or pending-segment dependency.

Every retained root, active read, sealed child and persistence job must participate in the retention inventory. Root capture and storage-lease acquisition are atomic with respect to generation retirement. Node hash identity is generation-independent, but evidence that its entire subtree is available is generation-dependent. A cache entry cannot certify durable completeness after its backing generation is retired. A generation mismatch invalidates the old availability evidence, not the content hash itself. Reconcile the descriptor through verified retention/proof handoff into the new generation; do not force a flat rebuild or full-state recertification on every rotation.

A durable closure record binds the ledger/header and both roots to the storage epoch, retained range and completed retention handoff. It is issued only after a verified full import or after a valid parent certificate plus verified, synced changed nodes covers all dependencies. The generation fingerprint alone is not a certificate. Store and sync the record before publishing a flat descriptor/advancing marker that relies on it. On rotation, required retention work establishes and syncs successor closure records before old-generation retirement; transfer the root inventory under the short publication guard. Reuse that required copying/verification work instead of adding a second full-state certification pass. Startup validates the records and generation handoff, or withholds the affected flat/history capability until recovery establishes it.

Use short metadata synchronization to register readers and defer physical retirement outside publication locks. Background retention copies/promotes the necessary closure before retiring a generation, reusing existing verified progress. A whole-generation pin is not the permanent history policy.

Operation counts, reader timeouts, pending segment bytes and retained disk bytes are bounded and observable. Historical/RPC leases carry explicit deadlines and close iterators/snapshots on cancellation; critical jobs are monitored and explicitly joined on cancellation. The outbox has configured pending-count, retained-byte and oldest-age thresholds: when a threshold is reached, stop advancing the flat base and admitting additional dependent persistence work, retain the bounded backlog, and report storage pressure. Apply equivalent admission limits to segments whose compaction is blocked by readers. Never keep accepting unlimited work merely because it has spilled to disk. Expirable RPC work may be cancelled by its existing service policy. Correctness-critical build/persistence dependencies may not be forcibly released: stop admitting more work, spill, or delay rotation and surface pressure. Disk exhaustion is an operational failure, not a transaction result.

Historical RPC and peer availability must continue to match the configured retention contract. Reducing the flat window does not reduce the advertised SHAMap history window.

## 9. Acquisition, replay and peer serving

Acquisition continues to validate node hashes, structure, requested paths, and root completeness. Store received nodes using the existing bounded acquisition and durable-generation machinery. Do not trust flat objects supplied by a peer as a substitute for an authenticated root.

To create a flat pivot, stream leaves from a complete acquired root into a new unpublished flat epoch using bounded sorting/batches. Reconstruct the root from the full imported leaf stream and compare it to the selected ledger header before publishing the base. Commit the descriptor only after the import and its backing root are durable. A root change, cancellation or failed import discards that unpublished epoch without disturbing the active one. This one-time import must remain off router/consensus locks and preserve ongoing successor collection.

Replay uses the same state-view and commitment interfaces as normal execution. It carries exact parent-version and generation evidence, and retains sealed segments until result ownership is transferred or cancelled. Preserve the current asynchronous replay preparation and short publication handoff.

Peer requests by root/path and by content hash continue to resolve canonical SHAMap bytes. Keep NodeStore as the durable content-addressed serving representation in this version. Where existing service rules allow serving a sealed in-process ledger, a scoped node resolver first checks its immutable pending segments and then NodeStore. The response retains those dependencies until serialization/send ownership is transferred; a pending-segment hit never supplies durable completeness evidence. Historical serving reads are non-retaining or admitted to the shared cache under their own priority. Flat-state availability alone must not mark a ledger complete for peer serving.

## 10. Memory accounting and pressure response

Extend `internal/membudget` rather than adding independent pools whose allowances can sum beyond available memory. Accounts are registered at startup; allocation-heavy operations reserve before allocating. Charge capacity, container/index overhead and bounded temporary duplication, not just payload length.

For the managed portions, enforce:

```text
cache records + active changes + pending persistence + read workspaces
  + acquisition/replay ownership + bounded queues and indexes <= managed budget
```

The equation counts unique allocations once. Shared-reference and ownership-transfer operations preserve the charge without a transient uncharged gap. Evicted entries with active borrowers remain charged until the final borrower releases them. In-flight duplicate loads and decoding copies have explicit workspace charges; coalesce loads where useful without an unbounded waiter registry.

| Account | Pressure response |
| --- | --- |
| Clean object/node caches | Evict unpinned cold entries or decline admission |
| Transaction and builder changes | Spill/coalesce within reserved workspace; backpressure new builds |
| Pending persistence | Spill immutable segments and throttle admission; preserve retry ownership |
| Persistence progress | Protected minimum sufficient to finish a bounded flush and release retained work |
| RPC/history/peer reads | Limit concurrency and scratch; cancel or reject service work under existing overload contracts |
| Acquisition/replay | Share existing admission and retain useful verified progress |

No memory wait occurs while holding a cache shard lock, service publication lock, rotation metadata lock, or a resource needed by the releasing worker. Oversized valid records use an explicitly accounted large-record/streaming path. Admission failure cannot become `tec`, `tem`, object-not-found, or an invented protocol size limit.

Start with a simple sharded byte-bounded cache and bounded admission for one-pass scans. Keep clean record buffers immutable; callers cannot retain iterator-owned memory after the iterator advances. Pool capacities and metadata are charged. Avoid finalizer-driven credit release and unbounded `sync.Pool` retention.

Pebble block caches, memtables/compaction, native allocations, goroutine stacks and GC headroom need explicit allowance outside or within the managed total, with each category counted exactly once. Go's memory limit is a soft runtime control and does not cover all process memory. Releasing logical credits makes Go objects collectible; it does not promise immediate RSS reduction. [Go memory guidance](https://go.dev/doc/gc-guide)

No production default budget split is selected by this draft. The implementation must derive a startup-valid set of protected minima and aggregate ceilings, and publish resolved values. A small configuration must retain enough capacity for at least one forward-progress path. This is a required sizing gate, not permission to ship unlimited defaults.

## 11. Integration boundaries

The following are proposed responsibilities, not new public API commitments:

| Area | Responsibility |
| --- | --- |
| Ledger state views | Exact-version `Read`, `Exists`, `Succ`/range access, `Rules()`, `LedgerSeq()` and scoped `Close`; preserve current byte ownership and error semantics |
| Transaction application | Private rollback-capable changes, `AdjustDropsDestroyed` and `ApplyAtomically`; merge final changes without changing metadata/invariants |
| Ledger builder | Parent binding, bounded overlays/spills, sealing and immutable dependency ownership |
| SHAMap commitment | Changed-path traversal, canonical serialization/hashing, bounded output and proofs tied to parent/generation |
| Flat storage | Consistent base capture, synced atomic advancement, verified import and epoch retirement |
| Ledger service | Selected-version publication, pending jobs, replay handoff, retention inventory and recovery outbox |
| NodeStore and backends | Canonical node persistence, generation-safe reads and durability barriers |

Existing integration points include `internal/ledger/view.go`, `internal/ledger/mutation.go`, `internal/tx/applystate`, `internal/ledger/service/persistence.go`, `internal/ledger/service/state_retention.go`, `shamap/snapshot.go`, `shamap/persistence_bounded.go`, and `storage/kvstore/pebble`.

Prefer internal interfaces with small explicit lifetimes. Do not expose flat-database transactions to transactors. Keep the transaction engine independent of the choice between a flat reader and a bounded root reader. Stable version `ForEach` is point-in-time and ascending, with callback early-stop and no internal publication lock held during callbacks. Reentrant reads are allowed against the captured version; concurrent mutation of a builder stays forbidden. Inventory transactional wrappers with currently unspecified iteration order before migration, and exercise any order-sensitive consumer through behavioral comparison rather than assuming sorting is harmless. `ForEachTransaction` remains a distinct operation. Preserve wrong-keylet-type behavior and the current owned-byte return contracts: flat adapters must check entry type and may not return aliases to mutable cache/iterator buffers. Existing exported SHAMap APIs need a caller inventory and compatibility adapters; removing an API is a separate explicit compatibility decision.

## 12. Required observability

Export current/peak managed bytes by account and ownership role; unique cache-record bytes and node counts; active borrowers; uncached decode workspace; dirty/spilled bytes; overlay depth and merge fan-in; pending jobs/bytes/age; admission waits/denials; eviction/reload counts; and leaked/invalid release counters.

Roles are fixed low-cardinality labels such as builder, current read, history read, peer serving, acquisition, replay, persistence and retention. Do not label metrics with ledger hashes or object keys. Role totals describe attributed ownership and must not double-count shared records.

Measure commitment time, durable-write time, publication lock time, consensus acceptance latency, allocations per ledger, GC CPU, storage reads/bytes, flat hit/fallback rate, generation-retirement lag, and disk spill/write amplification. Report Go live heap, process RSS, native/storage-engine usage and cgroup file-cache usage separately.

## 13. Verification and release gates

### Correctness and fault tests

| Workload/fault | Required assertion |
| --- | --- |
| Identical ordered transaction corpus on old/new views | Same TERs, metadata, serialized state/transaction nodes, roots and headers |
| Inserts/replacements/deletes, empty/single-item maps, long common prefixes, all branches | Canonical SHAMap behavior and byte equality; bounded traversal frontier |
| Repeated writes and create/delete cycles within/between transactions | Rollback, invariants and per-transaction metadata unchanged after ledger-delta coalescing |
| Ordered scans across overlays and spill runs | Strict successor ordering, newest precedence, tombstones, bounds and errors match the legacy view |
| Forks with the same sequence or state root, or stale epoch/schema | Full descriptor comparison; wrong-parent flat advancement rejected |
| Base capture racing with advancing-marker publication | Reader gets one old/new ready version or root fallback; never mixed flat contents |
| Fee-only recovery, destroyed XRP and transaction insertion failures | No reader observes mixed state/transaction roots or counts; correct `tec` rollback |
| Keylet mismatch, byte mutation by caller, reentrant/early-stop scans | Existing type/ownership/callback contracts preserved; storage errors remain errors |
| Shared reads, eviction and delayed borrowers | Credits retained until last release; no mutable alias or dangling iterator buffer |
| Failed or ambiguous node/flat/relational Sync and crash at every marker/chunk/commit boundary | Recover one coherent ready version; advancing bases are hidden; outbox is idempotent; no false durable checkpoint |
| Stale writer takeover and retry from the first chunk | Old callbacks cannot write; replay produces the same semantic version |
| Outbox pending reads, unavailable relational database and pinned compaction | Exact-version state reads remain distinct from indexed publication; count/byte/age limits stop backlog growth |
| Root-closure certificate transfer during rotation | No retirement before durable handoff; a fingerprint or warm cache alone is insufficient |
| Cancellation during build, import, replay or persistence | Explicit worker handoff and eventual exact resource release |
| Three or more rotations with cold historical reads and pending jobs | Required nodes remain reloadable; generation proofs cannot survive invalidation incorrectly |
| Tiny budgets, oversized valid values, full queues and slow disk | Progress or explicit operational failure without protocol-result changes, deadlock or unbounded growth |
| By-hash and root/path peer requests with caches cold | Same canonical node bytes and completeness/availability behavior |
| Flat import with omitted, duplicated, corrupt or wrong-root data | Import fails before publication |

Protocol behavior is checked against local rippled `3.3.0`, commit `00a178fb92ca49521b937ae1a99d863765ea8a90`, unless the implementation task explicitly selects another oracle. Source areas include `src/libxrpl/shamap` and the relevant ledger/transaction tests. Memory, scheduling, spill and retention policy are local implementation decisions.

Run the affected Go suites with the race detector, including production completion/handoff paths. A helper-only budget test is insufficient. The full protocol/conformance gate runs before switching production execution to the new path.

### Performance and soak protocol

Compare the same corpus, hardware, cache allowances, retention settings and storage backend against the baseline. Include hot keys, disjoint cold paths, large valid ledgers, offer/payment scans, peer/history readers, acquisition plus replay, and slow persistence. Report warm and cold behavior separately.

Use both a long fixed-state workload that eventually touches the whole key space and a growing-state workload. Sample across many history windows and at least three online-delete rotations without restarting or forcing GC. Decoded managed bytes must plateau within their configured allowance; dirty/pending/workspace peaks must remain within the admitted total. A decreasing cache configuration is not evidence of the architectural benefit.

Pass conditions are zero root/metadata/result mismatches, zero lost required nodes, zero ownership-accounting violations, and no recurring full-state certification on ordinary advancement. Quantify CPU, allocation, I/O, disk amplification and latency together.

Before implementation tuning, record numerical ceilings for p95/p99 consensus acceptance and commitment latency, replay throughput, storage-read amplification and transient disk use on the agreed reference workload. Those ceilings and the production resource defaults are unresolved measurements in this draft. No performance or rollout pass can be declared while they are unset; memory reduction alone is not sufficient.

## 14. Implementation sequence and recovery

1. **Ownership and baseline instrumentation.** Inventory callers and capture representative memory, throughput and latency baselines. Add exact-version view contracts and accounting tests for every execution mode.
2. **Flat materialization and authenticated reads.** Import a verified root, maintain the durable base/outbox, and use the matching flat view when ready. Every other read uses the authenticated SHAMap root with bounded workspace.
3. **Bounded transaction and ledger changes.** Use bounded overlays, spills, dependency leases and pressure tests for every ledger transition. Commitment and execution share the same authoritative bounded view.
4. **Incremental commitment and recovery.** Build roots/node bytes from the final delta. Exercise failure injection, forks, import, rotation and cold serving. Startup always reconciles advancing markers and outstanding index work before accepting new work.
5. **Production operation and measurement.** Select measured defaults, pass latency/I/O gates, update the storage ADR and operating documentation, and retain only the bounded execution and recovery contracts.

There is one authoritative execution engine for a ledger view, including its transaction map, commitment and nested application. It cannot switch midway through transaction application. Persist the schema/epoch compatibility identity required to interpret recovery records so restart cannot combine mismatched implementations. A flat materialization is a durable read optimization; it never replaces the authenticated root or selects another execution engine. Temporary work and all read leases remain within their configured memory and scratch budgets.

## 15. Decisions to settle during prototyping

The design fixes ownership, version isolation, canonical persistence and bounded-work requirements. These choices require measured prototypes before production defaults:

- Cache the serialized node, decoded node, or both, subject to one aggregate byte allowance; avoid duplicating the working set without a measured decode benefit.
- Select overlay spill thresholds, maximum streams/dependency depth, protected persistence capacity and operation concurrency from valid worst-case workloads.
- Decide whether frequent historical reads justify a later flat-history index. Root-backed history is the required first-version behavior.
- Select numerical performance ceilings and the reference hardware/corpus before evaluating optimization claims.

Any change to canonical encoding, error results, retained-history availability or publication safety is outside these tuning decisions and requires a separate protocol/compatibility review.


## 16. Operating bounded execution

Bounded execution is always active. Persistent NodeStore deployments use the flat materialization; standalone and in-memory deployments use a bounded in-memory SHAMap family with an explicitly owned temporary spill directory. The configured allowances cover the existing protected accounts plus the execution accounts. These are resource defaults, not measured production sizing recommendations:

```toml
[state]
memory_mb = 64
scratch_mb = 2048
cache_mb = 16
memtable_mb = 8
```

`memory_mb` caps shared transaction/ledger overlay ownership. `scratch_mb` caps retained spill files separately from RAM. The flat store reserves its cache, four memtables and storage workspace inside the shared process allowance. Root ownership and commitment/persistence workspace have protected accounts; startup rejects an aggregate configuration that cannot fit. Resolved accounts are logged and exposed by the existing memory-budget collector.

The flat database is `execution-state` beneath the configured local state directory (or the NodeStore parent directory). Temporary overlay runs live in its `scratch` subdirectory. Startup takes the flat database lock before removing abandoned temporary runs; canonical nodes, the advancement marker and the index outbox supply recovery state. Durable publication retries retain their bounded queue admission under storage failures.

Startup recovery reconciles the advancement marker and index outbox before accepting new work. Recovery either completes and resumes bounded execution or prevents startup while preserving its obligations for another attempt; there is no alternate execution selector or derived-state deletion mode.

Prometheus exposes bounded execution work, active root borrowers, flat hits/fallbacks, pending outbox count/bytes/age, advancement/failure state, and spill disk usage/peaks/denials. The existing memory metrics cover overlay, roots, flat storage and persistence allocations. These are managed ownership measurements; Go heap/RSS and storage-engine memory remain separate measurements.

`BenchmarkExecutionDisjointPathsRootFallback` measures authenticated-root reads with an in-memory Family and no flat materialization. It verifies the required root fallback, not a second execution engine or a rollout gate. The regression suite includes a 96-close, small-history retention workload with six rotations and an outstanding flat outbox. Representative long-running storage workloads, numerical latency/I/O ceilings, and measured production defaults remain required.

For local verification, `go test ./internal/testing/...` runs the transaction corpus with bounded ledger execution and journals. The fixture uses shared RAM/disk allowances and checks explicit release at test teardown.

Commitment streams canonical node batches directly into NodeStore. Build admission protects emission, and destination-root leases protect the resulting closure until service history, pending persistence, or the flat outbox takes ownership. NodeStore Sync remains the durability boundary. Service read leases separately retain exact state/transaction roots for the entire query or queued request; request completion releases them even when the history descriptor was evicted during the operation.
