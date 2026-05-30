# Skipped Tests — Limitations and Rationale

This document explains the remaining skipped tests in `internal/testing/`,
grouped by the underlying limitation that prevents them from running.

---

## 1. Conditionally Skipped — Slow Tests (3 tests)

These tests run in normal mode but skip under `go test -short`. They are not
broken; they are guarded to keep CI fast.

| Test | File | Reason |
|------|------|--------|
| `TestAMMBookStep_StepLimit` | amm/amm_bookstep_test.go | Creates 2,000 offers |
| `TestAMMDelete/EmptyState_OperationsFail` | amm/amm_delete_test.go | Creates 522 accounts |
| `TestAMMDelete/MultipleDeleteCalls` | amm/amm_delete_test.go | Creates 1,034 accounts |

---

## 2. Manual Stress Tests (3 tests)

Ported from rippled tests marked `MANUAL_PRIO`. They deliberately create
thousands of offers to probe `tecOVERSIZE` boundaries and are intended for
manual runs only.

| Test | File |
|------|------|
| `TestPlumpBook` | oversizemeta/oversizemeta_test.go |
| `TestOversizeMeta_Full` | oversizemeta/oversizemeta_test.go |
| `TestFindOversizeCross` | oversizemeta/oversizemeta_test.go |

---

## 3. Missing Infrastructure: Network / Peer Layer (1 test)

| Test | File |
|------|------|
| `TestBatchNetworkOps` | batch/batch_test.go |

Requires peer-to-peer transaction relay, which is outside the scope of the
isolated test environment.

---

## 4. Missing Infrastructure: `ripple_path_find` RPC (13 tests)

These tests call the `ripple_path_find` RPC method to *discover* paths, then
verify the returned alternatives. The goXRPL test environment has no RPC server
and the path-finding algorithm runs inside `rippled`'s `PathRequests` module
which has not been ported. **Tracked in a dedicated issue.**

| Test | File | Notes |
|------|------|-------|
| `TestPath_SourceCurrenciesLimit` | payment/path_test.go | Tuning limits |
| `TestPath_PathFindConsumeAll` | payment/path_test.go | Full liquidity consumption |
| `TestPath_IssuesPathNegativeIssue5` | payment/path_test.go | Negative-path regression |
| `TestPath_AlternativePathsLimitReturnedPaths` | payment/path_test.go | Quality ordering |
| `TestPath_PathFind04` | payment/path_test.go | Bitstamp/SnapSwap liquidity |
| `TestPath_PathFind01` | payment/path_test.go | XRP → IOU via offers |
| `TestPath_PathFind02` | payment/path_test.go | IOU → XRP via offers |
| `TestPath_PathFind05` | payment/path_test.go | Multi-scenario path finding |
| `TestPath_PathFind06` | payment/path_test.go | Gateway-to-user path |
| `TestPath_ReceiveMax` | payment/path_test.go | Receive-max computation |
| `TestPath_AlternativePathConsumeBoth` | payment/path_test.go | Dual-path consumption |
| `TestPath_AlternativePathsConsumeBestTransfer` | payment/path_test.go | Transfer-rate quality |
| `TestPath_AlternativePathsConsumeBestTransferFirst` | payment/path_test.go | Transfer-rate ordering |

---

## 5. Engine Limitation: AMM in Payment Paths (3 tests)

| Test | File |
|------|------|
| `TestAMMBookStep_GatewayCrossCurrency` | amm/amm_bookstep_test.go |
| `TestFreeze_AMMWhenFrozen` | payment/freeze_test.go |
| `TestPath_AMMDomainPath` | payment/path_test.go |

Require auto-path discovery (`build_path`) that includes AMM pools as
liquidity sources alongside order-book offers.

---

## 6. Engine Limitation: Domain / Hybrid Offers (1 test)

| Test | File |
|------|------|
| `TestPath_HybridOfferPath` | payment/path_test.go |

Requires the Permissioned DEX *domain* concept where offers can be scoped to
a domain and hybrid offers are visible in both domain and open books.

---

## Summary by Category

| Category | Count | Actionable? |
|----------|-------|-------------|
| Slow / manual stress | 6 | No — run manually with `-short=false` |
| Network layer | 1 | No — outside test scope |
| `ripple_path_find` RPC | 13 | Tracked in dedicated issue |
| AMM in payment paths | 3 | Yes — implement auto-path with AMM |
| Domain / hybrid offers | 1 | Yes — domain-aware path finding |
| **Total** | **24** | |
