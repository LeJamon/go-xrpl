# Issue #411 — address remaining (partial + open) items

Branch: `worktree-issue-411-consensus-ledger-cleanup` (from main @ 95b38f98)

## Scope decision
- **#21 (split `state/` into subpackages): DEFERRED** — 2562 external call sites across 227 files; issue says "Consider"; an unreviewable architectural change unfit for a cleanup sweep. Documented on the issue.
- **#28 (ledgertrie O(n) insert/delete): doc-note only** — issue says "no fix expected" (scale review). Adding a tradeoff comment, not a structural change.

## Wave 1 (parallel, disjoint files)
- [ ] #13 adaptor `New()` carve-out (quorum/cookie/amendment-stance helpers; reuse `computeQuorum`)
- [ ] #14 `GetFeeVote` → struct
- [ ] #12 shared `BuildPseudoTx` helper (new internal/consensus/common) + document stateless/stateful DoVoting
- [ ] #22 genesis.go visibility consistency
- [ ] #31 unexport `LedgerCacheConfig` / `CacheStats`
- [ ] #20 document `openledger.Modify` blocking caveat
- [ ] #23 trim `ledger_timing.go` comment block
- [ ] #28 doc-note on ledgertrie O(n) tradeoff

## Wave 2 (parallel, disjoint packages)
- [ ] #9 rcl/engine.go: extract `ProposalTracker` + close-time vote state; split engine_test.go
- [ ] #10 decompose `shouldCloseLedger()`
- [ ] #18 split service.go → lifecycle/query/events/cache (same package)
- [ ] #19 `forEachFiltered` helper

## Wave 3 (ledger core)
- [ ] #15 extract skiplist → internal/ledger/skiplist/, negativeUNL → internal/ledger/negativeunl/
- [ ] #17 collapse duplicate `Reader` interface (amendments.go)
- [ ] #33 trim verbose ledger.go field/interface comments
- [ ] #35 tests: Insert/Update/Erase error paths, immutability, snapshot isolation

## Verification
- build + targeted tests after each wave; full `go build ./...` + consensus/ledger suites at end; gofmt, go vet
