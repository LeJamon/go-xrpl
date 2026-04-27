# PR: On-disk validation archive (issue #267)

Implements the `onStale` / `doStaleWrite` pathway that goXRPL has been missing: stale validations pruned from the in-memory `ValidationTracker` are persisted to a new `Validations` table in the relational DB, written by a batched async writer so the receive path never blocks on I/O, with a configurable ledger-seq retention window.

RPC wiring of the historical archive into `validator_info` and other consumers is **out of scope** for this PR — only the service-layer interface is added. The full RPC response extension is a follow-up.

## Why this matters

`ValidationTracker.ExpireOld` (internal/consensus/rcl/validations.go:509) deletes stale validations from memory with no archival. Grep for `onStale|doStaleWrite|writeValidation|staleValidation` across `internal/consensus/` returns zero hits. Rippled's `RCLValidations.h:205-210` documents: _"Manages storing and writing stale RCLValidations to the sqlite DB"_ — historically consumed by `tx_history`, `validator_list_keys`, forensic tooling, and the `validator_info` admin RPC.

Separately, `ExpireOld` is today **dead code** — nothing calls it. This PR also wires it up (drive from the fully-validated callback in the engine) so the archive pathway actually runs in production.

## Rippled reference map

| Concern | Rippled file:line |
|---|---|
| `onStale` callback contract | `src/xrpld/consensus/Validations.h:~254` (`Adaptor::onStale`) |
| `RCLValidations::onStale` | `src/xrpld/app/consensus/detail/RCLValidations.cpp:~160-175` |
| `doStaleWrite` batched loop | `src/xrpld/app/consensus/detail/RCLValidations.cpp:~185-240` |
| Historical Validations DDL | `src/xrpld/app/main/DBInit.h` (pre-May 2019) — columns `LedgerSeq, InitialSeq, LedgerHash, NodePubKey, SignTime, RawData` plus four indexes |
| Job-queue handoff | `src/xrpld/app/consensus/detail/RCLValidations.cpp` (`jtWRITE` enqueue) |
| `validator_info` read consumer | `src/xrpld/rpc/handlers/ValidatorInfo.cpp` |

Rippled removed the on-disk table in May 2019 (commit c5a95f1eb5a5). The schema and batching pattern are still valid references; we're re-adding the archive because goXRPL needs it for forensic/RPC use.

## Design decisions

- **Package layout**: new `internal/consensus/archive/` with exported `Archive` type. Depends on `storage/relationaldb` (repository) — no reverse dependency. Consensus wiring (engine) depends on archive via interface — not the concrete type.
- **Backends**: SQLite **and** PostgreSQL (parity with existing `ledgers`/`transactions` tables). Both initialize schema in `RepositoryManager.Open()` matching the established pattern at `storage/relationaldb/sqlite/repository_manager.go:184-206` and `storage/relationaldb/postgres/repository_manager.go:139-192`.
- **Write path**: `ValidationTracker.SetOnStale(fn)` fires once per pruned validation inside `ExpireOld`. The callback is a cheap "enqueue to channel" — no DB I/O on the caller's goroutine. A dedicated writer goroutine reads from the channel, batches up to `BatchSize` rows, and writes each batch in a single transaction. Matches rippled's `jtWRITE` job-queue handoff semantics.
- **Raw bytes**: add `Raw []byte` to `consensus.Validation`. `parseSTValidation` populates it from the original input; outbound self-built validations re-serialize via an exported `SerializeSTValidation`. This mirrors rippled's `RCLValidation::getSerialized()` and avoids a parse→serialize round-trip at archive time.
- **Retention**: `RetentionLedgers uint32` config knob. On each ExpireOld sweep, after archive writes complete, we run a bounded DELETE `WHERE LedgerSeq < currentFullyValidatedSeq - RetentionLedgers`. Bounded by `DeleteBatch` (reuses the knob pattern already in `NodeDBConfig`). `RetentionLedgers = 0` disables retention (keep forever).
- **Flush on shutdown**: `Archive.Close(ctx)` drains the channel, writes the final batch, then returns. Engine.Stop() calls it before returning.
- **Engine wiring**: `ExpireOld` is currently never invoked. We call it from the fully-validated callback at `engine.go:228` — every time a new ledger is fully validated, prune validations below `max(0, seq - retainInMemoryLedgers)`. New config knob `InMemoryLedgers uint32` controls the in-memory window; defaults to 256 (matching rippled's minimum `online_delete`).
- **Read path**: minimal. Adds `ValidationArchive` to `types.Services` so RPC handlers can reach it. Repository methods: `GetValidationsForLedger(seq)`, `GetValidationsByValidator(nodeKey, limit)`, `GetValidationCount()`. No handler extension in this PR — that is a separate, smaller follow-up.
- **Fix the existing `ExpireOld` bug**: the current loop at validations.go:514-522 has a dangling `break` that exits the inner loop after inspecting one validation regardless. Since all validations for the same `ledgerID` share a `LedgerSeq` this is technically correct for the drop decision, but it leaks the `byNode` map (never removes the per-node pointer). Fix as part of this PR — we have to touch the function anyway and correctness matters for the archive's input stream.

## Files

### New

- `goXRPL/internal/consensus/archive/archive.go` — `Archive` struct, `New(Repo, Config)`, `OnStale(*consensus.Validation)`, `Flush(ctx)`, `Close(ctx)`. Channel + goroutine batching, retention DELETE loop.
- `goXRPL/internal/consensus/archive/archive_test.go` — unit tests for batching, flush semantics, close-drains-pending, retention arithmetic (uses mock repo).
- `goXRPL/internal/consensus/archive/config.go` — `Config` struct: `Enabled bool`, `RetentionLedgers uint32`, `BatchSize int`, `FlushInterval time.Duration`, `DeleteBatch int`, `InMemoryLedgers uint32` + `Defaults()`.
- `goXRPL/storage/relationaldb/validations.go` — exported types `ValidationRecord`, `ValidationsFilter`; interface `ValidationRepository`.
- `goXRPL/storage/relationaldb/sqlite/validation_repository.go` — SQLite impl. Uses the `ledgerDB` handle (cohabits with `ledgers` table) to avoid opening a third file.
- `goXRPL/storage/relationaldb/sqlite/validation_repository_test.go` — round-trip CRUD, batch insert, retention delete, index usage.
- `goXRPL/storage/relationaldb/postgres/validation_repository.go` — Postgres impl.
- `goXRPL/internal/testing/validationarchive/archive_test.go` — three required acceptance tests end-to-end (real SQLite, real ValidationTracker).

### Modified

- `goXRPL/internal/consensus/types.go` — add `Raw []byte` to `Validation`. Keep omitempty semantics — internal field, not JSON-serialized.
- `goXRPL/internal/consensus/adaptor/stvalidation.go`:
  - populate `v.Raw = append([]byte(nil), data...)` in `parseSTValidation` before returning.
  - export `SerializeSTValidation` (rename; keep `serializeSTValidation` as a thin wrapper for back-compat within the package).
- `goXRPL/internal/consensus/rcl/validations.go`:
  - add field `onStale func(*consensus.Validation)`.
  - add `SetOnStale(fn func(*consensus.Validation))`.
  - rewrite `ExpireOld` to (a) fire `onStale` for every dropped validation before deletion, (b) correctly remove entries from `byNode` too.
- `goXRPL/internal/consensus/rcl/validations_test.go` — add test for `SetOnStale` callback + fix for `byNode` cleanup.
- `goXRPL/internal/consensus/rcl/engine.go`:
  - accept optional `archive` on the Engine struct (via `NewEngine` option or setter).
  - in `Start()`, call `e.validationTracker.SetOnStale(archive.OnStale)` when archive is non-nil.
  - in the `SetFullyValidatedCallback` hook, after `OnLedgerFullyValidated`, call `e.validationTracker.ExpireOld(seq - inMemoryLedgers)`.
  - in `Stop()`, call `archive.Close(ctx)` before returning.
- `goXRPL/storage/relationaldb/interface.go` — add `ValidationRepository` to the `RepositoryManager` interface; add `Validation()` accessor.
- `goXRPL/storage/relationaldb/sqlite/repository_manager.go`:
  - add `validationRepo *ValidationRepository` field.
  - new `initValidationSchema(ctx)` method (DDL below).
  - wire in `Open()` (call after `initLedgerSchema`) and close in `close()`.
  - implement `Validation() relationaldb.ValidationRepository`.
- `goXRPL/storage/relationaldb/postgres/repository_manager.go` — parallel changes for postgres (DDL in `initSchema`, repo wiring, accessor).
- `goXRPL/config/database.go` — add new struct `ValidationArchiveConfig`. Validate ranges.
- `goXRPL/config/config.go` — add `ValidationArchive ValidationArchiveConfig \`toml:"validation_archive" mapstructure:"validation_archive"\`` field on `Config`.
- `goXRPL/internal/rpc/types/services.go` — add `ValidationArchive` field (interface) to the `Services` struct so handlers can query historical validations.

## DDL

SQLite:

```sql
CREATE TABLE IF NOT EXISTS validations (
    ledger_seq    INTEGER NOT NULL,
    initial_seq   INTEGER NOT NULL,
    ledger_hash   BLOB NOT NULL,
    node_pubkey   BLOB NOT NULL,
    signature     BLOB NOT NULL,
    sign_time     INTEGER NOT NULL,
    seen_time     INTEGER NOT NULL,
    flags         INTEGER NOT NULL,
    raw           BLOB NOT NULL,
    PRIMARY KEY (ledger_hash, node_pubkey)
);
CREATE INDEX IF NOT EXISTS idx_validations_seq        ON validations(ledger_seq);
CREATE INDEX IF NOT EXISTS idx_validations_node       ON validations(node_pubkey, ledger_seq);
CREATE INDEX IF NOT EXISTS idx_validations_sign_time  ON validations(sign_time);
CREATE INDEX IF NOT EXISTS idx_validations_initial    ON validations(initial_seq, ledger_seq);
```

PostgreSQL: identical columns, `BIGINT` for INTEGER, `BYTEA` for BLOB, otherwise same.

Primary key choice `(ledger_hash, node_pubkey)` matches the natural upsert semantics — a validator can only produce one validation per ledger, and we want idempotent re-archival if `ExpireOld` is re-driven. `ON CONFLICT DO NOTHING` on insert.

## Acceptance tests

All in `internal/testing/validationarchive/archive_test.go`. Use an in-process SQLite (temp dir) and real `ValidationTracker`.

1. **`TestValidationArchive_StaleValidationWrittenOnPrune`**
   - Construct `Archive` with `BatchSize=1, FlushInterval=10ms`.
   - Wire `tracker.SetOnStale(archive.OnStale)`.
   - `tracker.Add(v)` for a validation at `seq=100`.
   - `tracker.ExpireOld(200)` — v should become stale.
   - Wait for flush tick.
   - Query repo: `GetValidationsForLedger(100)` returns exactly `v` (hash, node, seq, raw matching).

2. **`TestValidationArchive_BatchedWriter_DoesNotBlockOnReceive`**
   - Construct `Archive` with a slow repo mock (100ms per write).
   - Fire `archive.OnStale` 1000 times in a tight loop under a 50ms deadline.
   - Assert the loop returns well under the deadline (channel-buffered, not synchronous).
   - Afterwards `archive.Flush(ctx)` drains — assert all 1000 rows eventually land in the repo.

3. **`TestValidationArchive_RetentionRespected`**
   - `RetentionLedgers=10, InMemoryLedgers=0`.
   - Archive 20 validations at seqs 1..20.
   - Call `archive.ApplyRetention(ctx, currentFullyValidatedSeq=20)`.
   - Query `GetValidationCount()` → 10 (seqs 11..20 remain).
   - Query `GetValidationsForLedger(5)` → empty; `GetValidationsForLedger(15)` → one row.

Supporting unit tests (in each package):

- `archive/archive_test.go`: `TestArchive_ChannelBackpressure_DropsOldest` (or `Blocks`, depending on choice — we'll pick *block with warning log*, matching rippled's bounded vector behavior). `TestArchive_CloseDrainsPending`. `TestArchive_FlushIsIdempotent`.
- `sqlite/validation_repository_test.go`: round-trip, duplicate insert is no-op, retention DELETE bounded by `DeleteBatch`.
- `rcl/validations_test.go`: `TestValidationTracker_ExpireOld_FiresOnStale`, `TestValidationTracker_ExpireOld_ClearsByNode`.

## Config schema

```toml
[validation_archive]
enabled            = true   # default: true when relationaldb is configured
retention_ledgers  = 10000  # 0 = keep forever
batch_size         = 128
flush_interval_ms  = 1000
delete_batch       = 1000   # bounded DELETE sweep size
in_memory_ledgers  = 256    # ExpireOld trigger: prune validations below (fullyValidatedSeq - in_memory_ledgers)
```

Validation: `retention_ledgers >= 0`, `batch_size >= 1`, `flush_interval_ms >= 10`, `in_memory_ledgers >= 1`, `delete_batch >= 1`.

## Execution tasks

Each task below is independently committable and keeps the tree green.

### Task 1 — `Raw []byte` on Validation + parser population

**Files:**
- Modify: `internal/consensus/types.go` (struct field)
- Modify: `internal/consensus/adaptor/stvalidation.go` (populate in parser + export `SerializeSTValidation`)
- Modify: `internal/consensus/adaptor/stvalidation_test.go` (assertion for Raw round-trip)

- [ ] **Step 1: Write failing test in `stvalidation_test.go`**

```go
func TestParseSTValidation_PopulatesRaw(t *testing.T) {
    orig := newTestValidation(t)
    blob := SerializeSTValidation(orig)
    parsed, err := parseSTValidation(blob)
    if err != nil { t.Fatal(err) }
    if !bytes.Equal(parsed.Raw, blob) {
        t.Fatalf("Raw mismatch: got %x want %x", parsed.Raw, blob)
    }
}
```

- [ ] **Step 2: Run** `go test ./internal/consensus/adaptor/... -run TestParseSTValidation_PopulatesRaw` → fails (no `Raw` field, no exported `SerializeSTValidation`).

- [ ] **Step 3: Add `Raw []byte` field to `consensus.Validation`** in `internal/consensus/types.go` (grouped with serialization-related fields; comment it as "Original wire bytes — nil for self-built until SerializeSTValidation is called").

- [ ] **Step 4: In `parseSTValidation`**, just before `return v, nil`:

```go
v.Raw = append([]byte(nil), data...)
```

- [ ] **Step 5: Export serializer**: rename `serializeSTValidation` → `SerializeSTValidation` across the package (keep a private wrapper if any test relies on the lowercase form — there should be none after rename).

- [ ] **Step 6: Run** `go build ./...` and `go test ./internal/consensus/adaptor/...` → passes.

- [ ] **Step 7: Commit**

```bash
git add internal/consensus/types.go internal/consensus/adaptor/stvalidation.go internal/consensus/adaptor/stvalidation_test.go
git commit -m "feat(consensus): capture raw wire bytes on parsed Validation"
```

### Task 2 — `SetOnStale` callback + fix `ExpireOld`

**Files:**
- Modify: `internal/consensus/rcl/validations.go`
- Modify: `internal/consensus/rcl/validations_test.go`

- [ ] **Step 1: Write two failing tests:**

```go
func TestValidationTracker_ExpireOld_FiresOnStale(t *testing.T) {
    vt := NewValidationTracker(1, 5*time.Minute)
    var fired []consensus.LedgerID
    vt.SetOnStale(func(v *consensus.Validation) { fired = append(fired, v.LedgerID) })

    v1 := mkVal(t, 100, nodeA)
    v2 := mkVal(t, 100, nodeB)
    vt.Add(v1); vt.Add(v2)
    vt.ExpireOld(200)

    if len(fired) != 2 { t.Fatalf("expected 2 stale fires, got %d", len(fired)) }
}

func TestValidationTracker_ExpireOld_ClearsByNode(t *testing.T) {
    vt := NewValidationTracker(1, 5*time.Minute)
    v1 := mkVal(t, 100, nodeA)
    vt.Add(v1)
    vt.ExpireOld(200)

    if vt.GetLatestValidation(nodeA) != nil {
        t.Fatal("ExpireOld left stale entry in byNode map")
    }
}
```

- [ ] **Step 2: Run** → fails (no `SetOnStale`, `byNode` leak).

- [ ] **Step 3: Add field + setter**:

```go
// inside ValidationTracker struct
onStale func(*consensus.Validation)

// new method
func (vt *ValidationTracker) SetOnStale(fn func(*consensus.Validation)) {
    vt.mu.Lock()
    defer vt.mu.Unlock()
    vt.onStale = fn
}
```

- [ ] **Step 4: Rewrite `ExpireOld`**:

```go
func (vt *ValidationTracker) ExpireOld(minSeq uint32) {
    vt.mu.Lock()
    onStale := vt.onStale

    var stale []*consensus.Validation
    for ledgerID, ledgerVals := range vt.validations {
        // All validations for a given ledgerID share LedgerSeq.
        var sample *consensus.Validation
        for _, v := range ledgerVals {
            sample = v
            break
        }
        if sample == nil || sample.LedgerSeq >= minSeq {
            continue
        }
        for nodeID, v := range ledgerVals {
            stale = append(stale, v)
            if byNode, ok := vt.byNode[nodeID]; ok && byNode == v {
                delete(vt.byNode, nodeID)
            }
        }
        delete(vt.validations, ledgerID)
        delete(vt.fired, ledgerID)
    }
    vt.mu.Unlock()

    // Fire callback outside the lock — callbacks may do I/O (archive).
    if onStale != nil {
        for _, v := range stale {
            onStale(v)
        }
    }
}
```

- [ ] **Step 5: Run** → tests pass. Also run `go test ./internal/consensus/rcl/...` to confirm no regression.

- [ ] **Step 6: Commit**

```bash
git add internal/consensus/rcl/validations.go internal/consensus/rcl/validations_test.go
git commit -m "feat(consensus): SetOnStale callback on ValidationTracker; fix ExpireOld byNode leak"
```

### Task 3 — ValidationRepository interface + types

**Files:**
- Create: `storage/relationaldb/validations.go`

- [ ] **Step 1: Write the file**:

```go
package relationaldb

import (
    "context"
    "time"
)

type ValidationRecord struct {
    LedgerSeq  LedgerIndex
    InitialSeq LedgerIndex
    LedgerHash Hash
    NodePubKey []byte // 33 bytes
    Signature  []byte
    SignTime   time.Time
    SeenTime   time.Time
    Flags      uint32
    Raw        []byte
}

type ValidationsFilter struct {
    LedgerSeq  *LedgerIndex
    LedgerHash *Hash
    NodePubKey []byte
    Limit      int
}

type ValidationRepository interface {
    Save(ctx context.Context, v *ValidationRecord) error
    SaveBatch(ctx context.Context, vs []*ValidationRecord) error
    GetValidationsForLedger(ctx context.Context, seq LedgerIndex) ([]*ValidationRecord, error)
    GetValidationsByValidator(ctx context.Context, nodeKey []byte, limit int) ([]*ValidationRecord, error)
    GetValidationCount(ctx context.Context) (int64, error)
    DeleteOlderThanSeq(ctx context.Context, maxSeq LedgerIndex, batchSize int) (int64, error)
}
```

- [ ] **Step 2: Add `Validation() ValidationRepository` to `RepositoryManager` interface** in `storage/relationaldb/interface.go`.

- [ ] **Step 3: Run** `go build ./...` → will fail on the two existing RepositoryManager impls (expected; wired in next tasks).

- [ ] **Step 4: Commit** once Tasks 4 + 5 compile together. Alternatively, add a temporary no-op `Validation()` returning `nil` on each manager to keep builds green in between — we'll do the latter so each task commits cleanly.

```go
// temporary stub on both repository_manager.go, removed in Task 4/5
func (rm *RepositoryManager) Validation() relationaldb.ValidationRepository { return nil }
```

- [ ] **Step 5: Build + commit**

```bash
go build ./...
git add storage/relationaldb/validations.go storage/relationaldb/interface.go \
        storage/relationaldb/sqlite/repository_manager.go \
        storage/relationaldb/postgres/repository_manager.go
git commit -m "feat(relationaldb): add ValidationRepository interface + stubbed accessor"
```

### Task 4 — SQLite ValidationRepository impl

**Files:**
- Create: `storage/relationaldb/sqlite/validation_repository.go`
- Create: `storage/relationaldb/sqlite/validation_repository_test.go`
- Modify: `storage/relationaldb/sqlite/repository_manager.go`

- [ ] **Step 1: Write failing round-trip test**:

```go
func TestValidationRepository_SaveAndGet(t *testing.T) {
    rm := newTestRM(t)
    repo := rm.Validation()
    rec := &relationaldb.ValidationRecord{
        LedgerSeq: 100, InitialSeq: 99,
        LedgerHash: [32]byte{1,2,3}, NodePubKey: make([]byte, 33),
        Signature: []byte{0xAB, 0xCD}, SignTime: time.Unix(1700000000, 0),
        SeenTime: time.Unix(1700000001, 0), Flags: 0x80000001, Raw: []byte{0xFE},
    }
    if err := repo.Save(context.Background(), rec); err != nil { t.Fatal(err) }

    got, err := repo.GetValidationsForLedger(context.Background(), 100)
    if err != nil { t.Fatal(err) }
    if len(got) != 1 { t.Fatalf("got %d rows", len(got)) }
    // field-by-field equality ...
}
```

Plus `TestValidationRepository_SaveBatch`, `TestValidationRepository_DuplicateIsNoop`, `TestValidationRepository_DeleteOlderThanSeq_Bounded`.

- [ ] **Step 2: Add `initValidationSchema` to `repository_manager.go`** (DDL block from above). Call it in `Open()` after `initLedgerSchema`.

- [ ] **Step 3: Implement `ValidationRepository` in `validation_repository.go`** — mirror the `LedgerRepository` style (struct holding `*sql.DB`, SQL constants, row-scan helpers).

- [ ] **Step 4: Remove the temporary `Validation() nil` stub**; wire the real repo.

- [ ] **Step 5: Run** `go test ./storage/relationaldb/sqlite/... -run Validation` → passes.

- [ ] **Step 6: Commit**

```bash
git add storage/relationaldb/sqlite/
git commit -m "feat(relationaldb/sqlite): implement ValidationRepository"
```

### Task 5 — PostgreSQL ValidationRepository impl

Mirror of Task 4. Adds the DDL to `postgres/repository_manager.go:initSchema` and creates `postgres/validation_repository.go` with matching interface impl (`$1,$2,...` placeholders, `ON CONFLICT DO NOTHING`, `BYTEA`/`BIGINT` types).

- [ ] Tests at `postgres/validation_repository_test.go` — skipped by default unless `POSTGRES_TEST_URL` env var is set (matches existing pattern in that package).

- [ ] Commit `feat(relationaldb/postgres): implement ValidationRepository`.

### Task 6 — Config struct

**Files:**
- Modify: `config/database.go`
- Modify: `config/config.go`
- Modify: `config/config_test.go`

- [ ] **Step 1: Write failing test** — load a toml string with a `[validation_archive]` table, assert parsed values.

- [ ] **Step 2: Add `ValidationArchiveConfig`**:

```go
type ValidationArchiveConfig struct {
    Enabled          bool   `toml:"enabled" mapstructure:"enabled"`
    RetentionLedgers uint32 `toml:"retention_ledgers" mapstructure:"retention_ledgers"`
    BatchSize        int    `toml:"batch_size" mapstructure:"batch_size"`
    FlushIntervalMs  int    `toml:"flush_interval_ms" mapstructure:"flush_interval_ms"`
    DeleteBatch      int    `toml:"delete_batch" mapstructure:"delete_batch"`
    InMemoryLedgers  uint32 `toml:"in_memory_ledgers" mapstructure:"in_memory_ledgers"`
}

func (c *ValidationArchiveConfig) Validate() error { /* range checks */ }
func DefaultValidationArchiveConfig() ValidationArchiveConfig {
    return ValidationArchiveConfig{Enabled: true, RetentionLedgers: 10000, BatchSize: 128, FlushIntervalMs: 1000, DeleteBatch: 1000, InMemoryLedgers: 256}
}
```

- [ ] **Step 3: Attach to `Config` struct**, include default in `DefaultConfig()`.

- [ ] **Step 4: Run** tests → pass.

- [ ] **Step 5: Commit** `feat(config): add validation_archive section`.

### Task 7 — Archive package (core)

**Files:**
- Create: `internal/consensus/archive/config.go`
- Create: `internal/consensus/archive/archive.go`
- Create: `internal/consensus/archive/archive_test.go`

`Archive` shape:

```go
type Archive struct {
    repo    relationaldb.ValidationRepository
    cfg     Config
    ch      chan *consensus.Validation
    done    chan struct{}
    wg      sync.WaitGroup
    logger  *slog.Logger
    lastSeq atomic.Uint32 // most-recent fully-validated seq seen; retention pivot
}

func New(repo relationaldb.ValidationRepository, cfg Config, logger *slog.Logger) *Archive { ... }
func (a *Archive) OnStale(v *consensus.Validation) { /* non-blocking enqueue or bounded block */ }
func (a *Archive) NoteFullyValidated(seq uint32) { a.lastSeq.Store(seq) }
func (a *Archive) Flush(ctx context.Context) error { ... }
func (a *Archive) ApplyRetention(ctx context.Context) (int64, error) { ... }
func (a *Archive) Close(ctx context.Context) error { ... }
```

- [ ] **Step 1: Tests** — `TestArchive_Enqueue_NonBlocking`, `TestArchive_Batches`, `TestArchive_CloseDrainsPending`, `TestArchive_ApplyRetention`, `TestArchive_FlushIdempotent`, `TestArchive_FiresFlushOnInterval`.

- [ ] **Step 2: Implement `archive.go`** — background goroutine reads from `ch`, accumulates up to `BatchSize`, flushes on full or on `FlushInterval` tick, calls `repo.SaveBatch`. Retention: on each full flush, if `cfg.RetentionLedgers > 0` and `lastSeq > RetentionLedgers`, call `repo.DeleteOlderThanSeq(lastSeq - RetentionLedgers, cfg.DeleteBatch)` — bounded to one call per flush so we don't stall on large sweeps.

- [ ] **Step 3: Run** `go test ./internal/consensus/archive/...` → passes.

- [ ] **Step 4: Commit** `feat(consensus/archive): batched async writer with retention`.

### Task 8 — Engine wiring

**Files:**
- Modify: `internal/consensus/rcl/engine.go`
- Modify: `internal/rpc/types/services.go`
- Modify: `internal/consensus/rcl/engine_test.go`

- [ ] **Step 1: Add optional archive parameter** to `Engine` (via a functional option `WithArchive(*archive.Archive)` or a setter `SetArchive`). Services struct gets a `ValidationArchive` field typed as a small interface (`GetValidationsForLedger`, `GetValidationsByValidator`).

- [ ] **Step 2: In `Start()`** after `NewValidationTracker`:

```go
if e.archive != nil {
    e.validationTracker.SetOnStale(e.archive.OnStale)
}
```

- [ ] **Step 3: In the fully-validated callback**, after `OnLedgerFullyValidated(...)`:

```go
if e.archive != nil {
    e.archive.NoteFullyValidated(seq)
}
if seq > e.inMemoryLedgers {
    e.validationTracker.ExpireOld(seq - e.inMemoryLedgers)
}
```

- [ ] **Step 4: In `Stop()`** before `e.eventBus.Stop()`:

```go
if e.archive != nil {
    _ = e.archive.Close(context.Background())
}
```

- [ ] **Step 5: Write test** `TestEngine_FullyValidated_TriggersExpireOld` covering: drive a validation below in-memory window, advance fully-validated seq, assert `ExpireOld` fired (use a `SetOnStale` spy).

- [ ] **Step 6: Run** `go test ./internal/consensus/rcl/...` + `go build ./...` → all green.

- [ ] **Step 7: Commit** `feat(consensus): wire archive into engine and trigger ExpireOld on fully-validated`.

### Task 9 — Acceptance tests (issue-specified)

**Files:**
- Create: `internal/testing/validationarchive/archive_test.go`

- [ ] Implement the three tests as described in the Acceptance section. Use a temp-dir SQLite via the existing `sqlite.NewRepositoryManager` + `Open()`. Use the real `ValidationTracker` + real `Archive`. No mocks.

- [ ] For `TestValidationArchive_BatchedWriter_DoesNotBlockOnReceive`: construct a slow repo wrapper that sleeps in `SaveBatch`. Channel buffer size (`BatchSize * 8`) must be enough to absorb 1000 enqueues without blocking.

- [ ] Run `go test ./internal/testing/validationarchive/... -v` → all three pass.

- [ ] Commit `test: acceptance tests for validation archive (issue #267)`.

### Task 10 — Verification

- [ ] `go build ./...`
- [ ] `go test ./internal/consensus/... ./storage/relationaldb/... ./internal/testing/validationarchive/... ./config/...`
- [ ] `./scripts/conformance-summary.sh | tail -30` — no regressions vs `main`.
- [ ] `go vet ./...`

## Verification plan (summary)

```
go build ./...
go test ./internal/consensus/archive/...
go test ./internal/consensus/rcl/... -run "ExpireOld|OnStale"
go test ./storage/relationaldb/sqlite/... -run Validation
go test ./internal/testing/validationarchive/...
./scripts/conformance-summary.sh | tail -30    # no regressions in existing suites
```

## Out of scope (follow-ups, link in PR description)

- Extending `validator_info` / `consensus_info` RPC responses to include `recent_validations` from the archive. Interface is in place; handler change is a trivial follow-up.
- Long-horizon retention with separate cold-storage table (partitioning by seq bucket).
- Gossip-replay tooling that consumes the archive (feasible but orthogonal).
- Pub/sub of `onStale` events for external forensics consumers.

## Review checklist

- [ ] All three issue-specified acceptance tests pass.
- [ ] `ExpireOld` no longer leaks `byNode` entries.
- [ ] `OnStale` callback fires outside the tracker's mutex (no lock order inversion with archive).
- [ ] Batched writer genuinely non-blocking — verified by the slow-repo stress test.
- [ ] Retention respects `DeleteBatch` ceiling; single sweep never blocks the writer for more than one bounded DELETE.
- [ ] SQLite **and** PostgreSQL backends both implement the interface; tests skip Postgres when env var absent.
- [ ] Config validated; invalid values produce clear errors at startup.
- [ ] `go build`, `go vet`, conformance summary all green.
