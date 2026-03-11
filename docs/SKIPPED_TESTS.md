# Skipped Tests — Limitations and Rationale

This document explains the 68 tests in `internal/testing/` that remain skipped,
grouped by the underlying limitation that prevents them from running.

---

## 1. Not Applicable to the Go Engine (17 tests)

These tests exist in the Go test suite as stubs ported from rippled but test
behaviour that lives outside the transaction engine. They will **never** be
unskipped because the functionality they exercise does not belong in
`internal/testing/`.

### RPC-layer tests (12)

| Test | File | Reason |
|------|------|--------|
| `TestPayChan_AccountChannelsRPC` | paychan/paychan_test.go | PayChan RPC handler |
| `TestPayChan_AccountChannelsRPCMarkers` | paychan/paychan_test.go | PayChan RPC pagination |
| `TestPayChan_AccountChannelsRPCSenderOnly` | paychan/paychan_test.go | PayChan RPC sender filter |
| `TestPayChan_AccountChannelAuthorize` | paychan/paychan_test.go | PayChan channel_authorize RPC |
| `TestPayChan_AuthVerifyRPC` | paychan/paychan_test.go | PayChan auth verify RPC |
| `TestMPT_InvalidInTx/OfferCreate_RejectsMPT` | mpt/mpt_test.go | MPT rejection is `passesLocalChecks` (RPC layer) |
| `TestMPT_InvalidInTx/TrustSet_RejectsMPT` | mpt/mpt_test.go | Same — RPC serialization constraint |
| `TestNftBuyOffersSellOffers` | nft/nftoken_test.go | `nft_buy_offers`/`nft_sell_offers` RPC |
| `TestSyntheticFieldsFromJSON` | nft/nftoken_test.go | JSON synthetic field injection (RPC) |
| `TestInnerSubmitRPC` | batch/batch_test.go | Raw blob submission (RPC) |
| `TestValidateRPCResponse` | batch/batch_test.go | RPC response validation |
| `TestPseudoTxn` | batch/batch_test.go | `passesLocalChecks` pseudo-tx rejection (RPC) |

### Non-engine tests (5)

| Test | File | Reason |
|------|------|--------|
| `TestRegression_JsonInvalid` | regression/regression_test.go | Tests the C++ JSON parser, not applicable to Go |
| `TestRegression_Secp256r1Key` | regression/regression_test.go | Low-level `SigningPubKey` manipulation; secp256r1 rejection is tested in the `crypto/` package |
| `TestCrossingLimits_AutoBridgedLimitsTaker` | offer/crossing_limits_test.go | Dead code in rippled — `testAutoBridgedLimitsTaker` is never called by `run()` and describes obsolete pre-strand flow behaviour |
| `TestDirectStepQuality` | quality/quality_test.go | Requires direct access to `toStrands`/`qualityUpperBound`/`flow` internals not exposed at the behaviour-test layer |
| `TestBookStepQuality` | quality/quality_test.go | Same — internal path-engine quality test |

---

## 2. Conditionally Skipped — Slow Tests (3 tests)

These tests run in normal mode but skip under `go test -short`. They are not
broken; they are guarded to keep CI fast.

| Test | File | Reason |
|------|------|--------|
| `TestAMMBookStep_StepLimit` | amm/amm_bookstep_test.go | Creates 2,000 offers |
| `TestAMMDelete/EmptyState_OperationsFail` | amm/amm_delete_test.go | Creates 522 accounts |
| `TestAMMDelete/MultipleDeleteCalls` | amm/amm_delete_test.go | Creates 1,034 accounts |

---

## 3. Manual Stress Tests (3 tests)

Ported from rippled tests marked `MANUAL_PRIO`. They deliberately create
thousands of offers to probe `tecOVERSIZE` boundaries and are intended for
manual runs only.

| Test | File |
|------|------|
| `TestPlumpBook` | oversizemeta/oversizemeta_test.go |
| `TestOversizeMeta_Full` | oversizemeta/oversizemeta_test.go |
| `TestFindOversizeCross` | oversizemeta/oversizemeta_test.go |

---

## 4. Missing Infrastructure: Transaction Queue (5 tests)

The goXRPL test environment does not implement a transaction queue. rippled
holds out-of-sequence transactions (`terPRE_SEQ`) in a queue and replays them
once the gap is filled. Without this, any test that submits transactions out of
order or relies on fee escalation cannot run.

| Test | File |
|------|------|
| `TestOrdering_IncorrectOrder` | ordering/ordering_test.go |
| `TestOrdering_IncorrectOrderMultipleIntermediaries` | ordering/ordering_test.go |
| `TestRegression_FeeEscalation` | regression/regression_test.go |
| `TestRegression_FeeEscalationExtremeConfig` | regression/regression_test.go |
| `TestBatchTxQueue` | batch/batch_test.go |

**What it would take:** Implement a `TxQueue` that buffers `terPRE_SEQ`
transactions and replays them on `env.Close()` in sequence order. Fee
escalation tests additionally require an open-ledger fee model.

---

## 5. Missing Infrastructure: Open Ledger Fee Model (4 tests)

rippled distinguishes between the *open ledger* (where fee escalation applies)
and *closed ledgers*. The goXRPL test environment applies all transactions at
a flat base fee with no open-ledger concept.

| Test | File |
|------|------|
| `TestSequenceOpenLedger` | batch/batch_test.go |
| `TestObjectsOpenLedger` | batch/batch_test.go |
| `TestOpenLedger` | batch/batch_test.go |
| `TestTicketsOpenLedger` | batch/batch_test.go |

**What it would take:** Implement an `OpenView` that tracks pending
transactions and computes escalated fees based on the queue depth, then expose
`env.SubmitToOpenLedger()` in the test environment.

---

## 6. Missing Infrastructure: Network / Peer Layer (1 test)

| Test | File |
|------|------|
| `TestBatchNetworkOps` | batch/batch_test.go |

Requires peer-to-peer transaction relay, which is outside the scope of the
isolated test environment.

---

## 7. Missing Infrastructure: `ripple_path_find` RPC (13 tests)

These tests call the `ripple_path_find` RPC method to *discover* paths, then
verify the returned alternatives. The goXRPL test environment has no RPC server
and the path-finding algorithm runs inside `rippled`'s `PathRequests` module
which has not been ported.

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

**What it would take:** Port rippled's `PathRequests` + `Pathfinder` modules
and expose a `ripple_path_find` RPC handler, or implement an equivalent
path-discovery algorithm in Go.

---

## 8. Engine Limitation: Rippling Through Intermediaries (4 tests)

The flow engine correctly handles same-issuer IOU payments (alice → gw → bob)
and cross-currency payments through offers, but it does not yet support
*rippling* — multi-hop IOU transfer chains where the currency passes through
intermediate accounts (alice → bob → carol) without offers.

| Test | File | Scenario |
|------|------|----------|
| `TestPath_IndirectPath` | payment/path_test.go | alice → bob → carol trust chain |
| `TestPath_IndirectPathsPathFind` | payment/path_test.go | Same, with path finding |
| `TestPath_NoRippleCombinations` | payment/path_test.go | NoRipple flag enforcement during rippling |
| `TestDepositAuth_NoRipple` | payment/depositauth_test.go | DepositAuth + NoRipple interaction |

**What it would take:** Fix the strand builder's `DirectStep` to propagate
balances through trust-line chains where the issuer differs at each hop.
The `NoRipple` flag must then be checked on each intermediate account's
trust line to block disallowed rippling.

---

## 9. Engine Limitation: Explicit Path Specification (7 tests)

Several tests construct payments with explicit `Paths` arrays that specify
multi-hop routes. While the payment engine supports `Paths` for single-hop
book steps (proved by `TestPath_ViaGateway` and `TestPath_XRPBridge`), complex
multi-issuer and self-payment paths fail with `tecPATH_DRY`.

| Test | File | Scenario |
|------|------|----------|
| `TestPath_IssuesRippleClientIssue23Smaller` | payment/path_test.go | Multi-hop path: alice→bob direct + alice→carol→dan→bob |
| `TestPath_IssuesRippleClientIssue23Larger` | payment/path_test.go | Larger multi-hop variant |
| `TestPath_AlternativePathsConsumeBestFirst` | payment/path_test.go | Dual-gateway with transfer rate |
| `TestFlow_SelfPayment2` | payment/flow_test.go | Self-payment USD↔EUR through offers |
| `TestFlow_SelfFundedXRPEndpoint` | payment/flow_test.go | Self-funded XRP endpoint via offer + path |
| `TestFlow_CircularXRP` | payment/flow_test.go | Circular XRP path |
| `TestSetTrust_PaymentsWithPathsAndFees` | payment/settrust_test.go | Payment paths + transfer rate |

**What it would take:** Fix the strand builder to handle multi-issuer path
steps and self-payment scenarios where the sender and receiver are the same
account but the path goes through external offers.

---

## 10. Engine Limitation: Freeze Enforcement in BookStep (2 tests)

The flow engine's `BookStep` does not check whether an offer owner's trust
line is frozen before consuming the offer. In rippled, frozen trust lines make
the owner's offers unavailable for consumption.

| Test | File |
|------|------|
| `TestFreeze_PathsWhenFrozen` | payment/freeze_test.go |
| `TestFreeze_OffersWhenFrozen` | payment/freeze_test.go |

**What it would take:** In `getNextOffer()` (or the offer quality check),
read the offer owner's trust line and skip the offer if
`lsfHighFreeze`/`lsfLowFreeze` is set on the issuer's side.

---

## 11. Engine Limitation: AMM in Payment Paths (2 tests)

| Test | File |
|------|------|
| `TestAMMBookStep_GatewayCrossCurrency` | amm/amm_bookstep_test.go |
| `TestFreeze_AMMWhenFrozen` | payment/freeze_test.go |

The first test does a cross-currency self-payment through an AMM pool and
gets `tecPATH_DRY` because the engine cannot auto-discover the AMM as a
liquidity source without explicit `build_path` support. The second test
verifies freeze interaction with AMM pools.

**What it would take:** Implement auto-path discovery (`build_path`) that
includes AMM pools as liquidity sources alongside order-book offers.

---

## 12. Engine Limitation: Domain / Hybrid Offers (1 test)

| Test | File |
|------|------|
| `TestPath_HybridOfferPath` | payment/path_test.go |

Requires the Permissioned DEX *domain* concept where offers can be scoped to
a domain and hybrid offers are visible in both domain and open books. The path
finder must be domain-aware.

---

## 13. Batch Infrastructure Gaps (3 tests)

| Test | File | Reason |
|------|------|--------|
| `TestBatchDelegate` | batch/batch_test.go | `DelegateSet.Apply()` is a stub — it does not serialize a proper Delegate SLE with permissions |
| `TestPreclaim` | batch/batch_test.go | Requires multi-signing in batch context: `SignerListSet` + `msig()` + `RegularKey` |
| `TestObjectCreate3rdParty` | batch/batch_test.go | Requires batch signature verification for 3rd-party signers |

**What it would take:**
- **DelegateSet**: Implement the full `DelegateSet.Apply()` matching rippled's
  `DelegateSet.cpp`, including SLE creation with permission fields.
- **Preclaim / 3rd-party signing**: Implement `checkBatchSign` with multi-sign
  verification (weight accumulation against signer list quorum).

---

## 14. Signer List: MultiSignReserve Amendment (1 test)

| Test | File |
|------|------|
| `TestOracle/NoMultiSignReserve_NoExpandedSignerList` | oracle/oracle_test.go |

When the `MultiSignReserve` amendment is **disabled**, signer lists should
charge `2 + numSigners` OwnerCount instead of 1. The Go `SignerListSet.Apply()`
always charges 1 regardless of amendment state.

**What it would take:** Add an amendment check in `SignerListSet.Apply()` that
uses the old reserve formula when `MultiSignReserve` is not enabled.

---

## 15. Deprecated Behaviour (1 test)

| Test | File |
|------|------|
| `TestMintFlagCreateTrustLines` | nft/nftoken_test.go |

The `tfTrustLine` NFToken mint flag was deprecated by the
`fixRemoveNFTokenAutoTrustLine` amendment. The test exercises the old
(buggy) behaviour that the amendment was created to fix. Since the amendment
is enabled by default, the flag is rejected.

---

## Summary by Category

| Category | Count | Actionable? |
|----------|-------|-------------|
| Not applicable (RPC, dead code, etc.) | 17 | No — permanently skipped |
| Slow / manual stress | 6 | No — run manually with `-short=false` |
| Transaction queue | 5 | Yes — implement TxQueue |
| Open ledger fee model | 4 | Yes — implement open-ledger fees |
| Network layer | 1 | No — outside test scope |
| `ripple_path_find` RPC | 13 | Yes — port path-finding algorithm |
| Rippling through intermediaries | 4 | Yes — fix DirectStep propagation |
| Explicit multi-hop paths | 7 | Yes — fix strand builder |
| Freeze in BookStep | 2 | Yes — add freeze check in offer iteration |
| AMM in payment paths | 2 | Yes — implement auto-path with AMM |
| Domain / hybrid offers | 1 | Yes — domain-aware path finding |
| Batch infrastructure | 3 | Yes — DelegateSet + multi-sign |
| MultiSignReserve amendment | 1 | Yes — amendment-gated reserve |
| Deprecated behaviour | 1 | No — intentionally skipped |
| **Total** | **68** | **~42 actionable** |
