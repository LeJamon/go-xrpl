# Ledger Persistence Recovery — Issue #126

## Problem

Every restart unconditionally calls `genesis.Create()`, discarding all previously accepted ledger state. NodeStore (Pebble) and RelationalDB are written to but never read back. The infrastructure for recovery already exists in the codebase — only the orchestration is missing.

Reference: https://github.com/LeJamon/go-xrpl/issues/126

---

## Rippled reference behavior

rippled supports a `START_UP` enum with modes:
- `FRESH` (CLI `--start`) — always wipe and create new genesis
- `NETWORK` (default) — create genesis only if no ledger in DB
- `LOAD` — require existing ledger, fail if absent
- `LOAD_FILE`, `REPLAY` — advanced modes (out of scope)

See: `rippled/src/xrpld/app/main/Application.cpp:1270-1316`

---

## Implementation Plan

### Step 1 — Add `StartupMode` config

- [ ] Define `StartupMode` type in `internal/ledger/service/` with values: `StartupFresh`, `StartupNormal`, `StartupLoad`
- [ ] Add `StartupMode` field to `service.Config`
- [ ] Add `--start` CLI flag to `internal/cli/server.go` (sets `StartupFresh`); default is `StartupNormal`

### Step 2 — Implement `latestLedgerHash()` helper

- [ ] In `internal/ledger/service/`, add `latestLedgerHash(ctx, relDB, nodeStore)` that:
  - If RelationalDB is configured: query `GetLatestValidatedLedger()` → return its `Hash` field
  - Else if NodeStore is configured: scan/iterate for `NodeLedger` entries to find the highest sequence
  - Return `(hash [32]byte, found bool, err error)`

> RelationalDB stores `LedgerInfo{Hash, Sequence, ...}` via `SaveValidatedLedger()`. The simplest recovery anchor is to ask it for the latest sequence+hash.

### Step 3 — Implement `loadLedger()` in service

- [ ] Add `loadLedger(ctx, hash [32]byte) (*Ledger, error)` to service:
  1. Fetch header node from NodeStore: `nodeStore.Fetch(ctx, hash)` → `Node{Type: NodeLedger, Data: 161 bytes}`
  2. Deserialize header: `header.DeserializeHeader(node.Data, true)` → `LedgerHeader`
  3. Create `NodeStoreFamily` from the existing nodeStore: `shamap.NewNodeStoreFamily(nodeStore)`
  4. Reconstruct state SHAMap: `shamap.NewFromRootHash(header.AccountHash, family, shamap.TypeState)`
  5. Reconstruct tx SHAMap: `shamap.NewFromRootHash(header.TxHash, family, shamap.TypeTransaction)`
  6. Assemble `Ledger` from header + both SHAMaps, mark as Validated

> `shamap.NewFromRootHash()` already exists (shamap.go:121-157) and supports lazy-loading of children on demand via `Family.Fetch()` — no need to deserialize the entire tree upfront.

### Step 4 — Update `Start()` to branch on startup mode

- [ ] Modify `service.Start()` to:
  ```
  switch config.StartupMode:
    case StartupFresh:
      createGenesis()
    case StartupNormal:
      hash, found = latestLedgerHash()
      if found: loadLedger(hash)
      else: createGenesis()
    case StartupLoad:
      hash, found = latestLedgerHash()
      if !found: return error("no existing ledger state found")
      loadLedger(hash)
  ```
- [ ] After loading, set `genesisLedger`, `closedLedger`, `validatedLedger`, `openLedger` accordingly
- [ ] Restore `ledgerHistory` with the recovered ledger (sequence → ledger)
- [ ] Restore `fees` from the loaded ledger's fee SLE

### Step 5 — Ensure SHAMap nodes are flushed on AcceptLedger

- [ ] Verify that `AcceptLedger()` calls `FlushDirty()` on both SHAMaps and passes the batch to `NodeStoreFamily.StoreBatch()` before or alongside `persistLedger()`
- [ ] If not wired, add the flush step in `persistToNodeStore()` before storing the header node

> Currently `persistToNodeStore()` iterates leaves via `ForEach()` but may not flush inner SHAMap nodes. Inner nodes are required for `NewFromRootHash()` lazy-loading to work at recovery time.

### Step 6 — RelationalDB: add `GetLatestValidatedLedger()`

- [ ] Check if `storage/relationaldb/` already exposes a method to fetch the latest validated ledger by sequence
- [ ] If not, add it to the `LedgerRepository` interface and implement for both PostgreSQL and SQLite backends

### Step 7 — Tests

- [ ] `TestStartupNormal_FreshDB`: no existing state → creates genesis (seq=1)
- [ ] `TestStartupNormal_ExistingState`: persist N ledgers, restart with `StartupNormal` → resumes from seq=N
- [ ] `TestStartupFresh_ExistingState`: persist N ledgers, restart with `StartupFresh` → resets to seq=1
- [ ] `TestStartupLoad_NoState`: `StartupLoad` with empty DB → returns error
- [ ] `TestStartupLoad_ExistingState`: persist N ledgers, `StartupLoad` → resumes from seq=N
- [ ] `TestFeeRecovery`: fees are correctly restored from loaded ledger state SLE

---

## Key files to touch

| File | Change |
|------|--------|
| `internal/ledger/service/service.go` | Branch `Start()` on startup mode |
| `internal/ledger/service/persistence.go` | Add SHAMap inner-node flush; ensure completeness |
| `internal/ledger/service/recovery.go` (new) | `latestLedgerHash()` + `loadLedger()` |
| `internal/cli/server.go` | Add `--start` flag, pass mode to service.Config |
| `storage/relationaldb/` | Add `GetLatestValidatedLedger()` if missing |
| `internal/testing/persistence/` (new) | Recovery integration tests |

---

## Infrastructure already in place (no changes needed)

- `shamap.NewFromRootHash()` — reconstructs SHAMap lazily from root hash + Family
- `header.DeserializeHeader()` — parses 161-byte header binary
- `nodestore.Database.Fetch()` — retrieves node by hash with caching
- `shamap.NodeStoreFamily` — bridges SHAMap and NodeStore
- `nodestore.Node{Type: NodeLedger}` — header stored by `persistToNodeStore()`

---

## Review

_To be filled after implementation._
