# Issue #1161 — full-rippled realignment of the keep-up/self-heal bundle

User decisions (2026-07-01): full rippled on the peer-LCL gate, the watchdog,
and catch-up; sig-cache stays ingress-only (my call); strip all issue-keepup
instrumentation, keep the fatal-path goroutine dump relabeled (my call).

## A. Peer-LCL gate → rippled checkLastClosedLedger semantics
- [x] getNetworkLedger: remove the GetTrustedSupport==0 peer-vote drop and the
      quorumPresent diagnostic machinery
- [x] checkLedger: remove the netSupport>ourSupport switch gate; safety moves
      to acquire-then-verify at the switch site (canSwitchToLedgerLocked =
      canBeCurrent + areCompatible, NetworkOPs.cpp:1948-1962)
- [x] Verify wired into handleWrongLedger AND OnLedger adoption walk
- [x] adaptor.preferredLCL: rippled getPreferredLCL structure (trie-preferred
      w/ stay-switch rules incl. lower-seq different-chain via ancestorOf;
      PreferredFromValidations no longer shadows the peer fallback);
      largestIssued = lastIssuedValidationSeq tracked in BroadcastValidation

## B. Watchdog + expireRound → rippled LoadManager/Consensus semantics
- [x] Removed close-driven fatal "ledger" loop + Service.SetStallPing plumbing;
      kept tick-driven "consensus" loop-liveness heartbeat + fatal abort
- [x] Expired past dwell: leaveConsensusLocked bow-out, accept ONLY behind the
      close-time gate (ResultAbandoned); no CT consensus → wait for checkLedger
- [x] Watchdog first-warn STW dump removed (abort-only), banner relabeled
- [x] Tests reworked: Expired_NoCT_WaitsForResync / Expired_WithCT_Accepts

## C. Catch-up → rippled LedgerMaster::doAdvance semantics
- [x] Timer-driven re-arm in maintenanceTick (+ tests)
- [x] All three cap-bypassing arming sites now honour maxConcurrentCatchup
- [x] History backfill: ReasonHistory serial backward walk after jump-adopt,
      tick-armed, store-only ingest, fixMismatch below-tip guard (+ test)
- [x] Request widths: collect 256 pre-dedup, cap 128 reply / 12 timeout

## D. Robustness
- [x] ErrNodeNotInStore sentinel; strict completeness walk for
      FinishSync/IsComplete (no phantom missing, no false complete);
      lenient request path unchanged (rippled collapse) (+ tests)
- [x] OnLedger-during-build: next-tick checkLedger re-derives via ungated
      votes + locally-held target — no re-delivery needed

## E. Strip issue-keepup instrumentation
- [x] All 8 sites stripped incl. entangled fields/consts/counters + test asserts
- [x] Watchdog dump kept on fatal path, relabeled
- [x] context.TODO(): left as-is — pre-existing repo convention (#185 note)

## F. Verify
- [x] build ./... clean; race clean on rcl/adaptor/inbound/shamap/watchdog/sigcache
- [x] Conformance: in-scope 1260/1260 (100%), fails = known out-of-scope suites
- [x] Primary lint (.golangci.yml): 0 issues on all touched packages
- [ ] Full go test ./... (running)
- [ ] Split into reviewable commits; push to origin/fix/issue-1161-selfheal-finalize
- [ ] Soak: xrpl-confluence 3r2g, 15k governor, ≥10 min lockstep

## G. Soak-driven round 2 (post-review, all pushed)
- [x] Island fix v1 (fcade701): validations-first getNetworkLedger
- [x] Island fix v2 (507f9e67): trie-inconclusive on unplaced majority;
      direct acquire of behind-closed trusted tips; OnLedger different-chain
      rewind; + all 4 review findings (monotonic validated, hook gating,
      unconditional prune, verify-before-wipe, backfill floor)
- [x] Prewarm acquired tx-set signatures (2dd0b6a8)
- [x] Soak iter3: self-heal proven end-to-end (island detected → chased →
      rejoined → full validations → validated 64→92)
- [ ] Soak iter4 (prewarm): verify stall cadence improves

## Review
Verification per commit: full tests, race (rcl/adaptor/service/inbound/shamap/
watchdog/sigcache), primary lint 0, in-scope conformance 1260/1260.
Adversarial diff review (3 lenses + refuters): 1 confirmed bug (validated
rewind) fixed; 2 refuted-but-real hardenings applied; iter27-trap concern
resolved by validations-first precedence.
Remaining known gap: 15k sustained smoothness is paced by build latency on
single-host soaks; prewarm (2dd0b6a8) is the current lever, measured by iter4.

# Issue #1280 — FlagsMasker completion audit

- [x] Inventory all 75 registered transaction types
- [x] Compare all 23 rippled 3.2.0 `getFlagsMask` overrides and the base mask
- [x] Check for invalid-bit masks left solely in `Validate`
- [x] Fix the residual MPTokensV2-dependent Payment mask
- [x] Pin exact masks and bad-fee precedence
- [x] Run transaction tests, vet, and build

## Finalization fixes

- [x] Match the mask predicate to the MPT Amount, not the legacy issuance field
- [x] Preserve rippled's ordered MPTokensV1/V2 Payment preflight semantics
- [x] Route MPTokensV2 Payments through the MPT-capable flow engine
- [x] Port valid-fee flag, path, SendMax, disabled-gate, and routing regressions
- [x] Remove the legacy standalone Payment issuance field and builder plumbing
- [x] Reject malformed MPT JSON, noAccount paths, and invalid MPT offer images
- [x] Preserve internal storage errors through MPT authorization and funding paths
- [x] Run focused tests, full transaction/integration coverage, build, vet, lint,
      and final rippled 3.2.0 review
- [x] Resolve the uncached CI goimports alignment failure and re-run lint
- [x] Fix the terminal resource-manager Start/Stop race exposed by core CI
- [x] Run focused race tests, strict lint, and the core package suite

## Review

Finalized PR #1308 against local rippled v3.2.0. Payment now uses the MPT Amount
as the authoritative asset identity, preserves rippled's V1/V2 preflight order
and TER precedence, and routes MPTokensV2 through Flow while retaining the V1
direct-transfer path. The standalone Go-only issuance field was removed.

The final review also fixed MPT wire/path serialization, cross-asset and
MPT-to-MPT offer crossing, destination checks, noAccount rejection, MPT offer
invariant parsing, pseudo-account authorization gating, canonical integral MPT
JSON parsing, and propagation of ledger storage/parse errors through shared MPT,
Offer, Check, AMM, and FlowCross code.

Verification:

- Focused state, payment, engine, invariant, mptutil, offer, check, AMM, MPT,
  deposit-preauth, and delegate suites pass uncached.
- `just build-all`, `just build-nocgo`, `just vet`, and `just lint` pass.
- The CI-only goimports finding in `strand.go` was corrected and reproduced
  locally with the formatter check before repushing.
- The core CI race exposed a pre-existing terminal lifecycle gap in the resource
  manager. Start/Stop are now serialized against late startup, and the exact
  core `-race` shard plus repeated manager/Components lifecycle tests pass.
- The full module test run passes outside the established out-of-scope
  conformance failures.
- Final conformance: 941 pass / 117 fail overall, 879 pass / 0 fail in scope;
  the 117 failures remain the out-of-scope Batch, Vault, XChain, and XChainSim
  suites.

# Issue #1287 — MPTokensV2 amendment-on engine support

## Plan

- [x] Map PR #1286's nine roadmap points to the v3 Go implementation and the
      rippled 3.2.0 tag, including all reachable call sites and parity tests.
- [x] Extend payment primitives (`EitherAmount`, `Issue`, `Book`, path nodes)
      with a coherent MPT arm and add reusable sandbox-aware MPT credit/funding.
- [x] Implement and wire `MPTEndpointStep`, including strand construction,
      reverse/forward liquidity, transfer fees, authorization, locks, and clawback.
- [x] Implement MPT order books end to end: book keys/directories, offer funding,
      crossing, transfer, rate/auth checks, and offer placement fields.
- [x] Implement amendment-on MPT apply behavior for AMM and Check transactions,
      including `lsfMPTAMM` ledger marking.
- [x] Thread `mpt_issuance_id` through book/path RPC request and response surfaces.
- [x] Port focused rippled parity cases and add Go unit/integration coverage for
      the new type arms, endpoint, book, AMM, Check, and RPC behavior.
- [x] Run formatting, focused tests incrementally, relevant integration suites,
      `just vet`, and `just build`; inspect the final diff for wiring and parity.

## Review

Implemented the coordinated MPT amount, sandbox, endpoint, book, offer, AMM,
Check, and RPC path. The protocol-bearing diff was checked against the local
rippled 3.2.0 implementations and their MPT, AMM-MPT, and Check-MPT test cases.
The rippled C++ tests were used as the parity oracle but were not executed.

Review findings fixed before handoff include zero-issuer asset rejection,
MPT endpoint check ordering, exact transfer-fee rounding and overflow handling,
owner-directory type discrimination, and preservation of issuer-agnostic IOU
rippling while MPT path matching remains issuance-specific.

Verification:

- Focused state, ledger service, payment, pathfinder, offer, AMM, Check, vault,
  RPC, and integration tests pass.
- `just vet`, `just build`, and `just lint` pass (`golangci-lint`: 0 issues).
- Feature and clean-base `just conformance --failing` results are identical:
  941 pass / 117 fail overall, 879 pass / 0 fail in scope. The 117 failures are
  the existing out-of-scope Batch, Vault, XChain, and XChainSim suites.
- The full module test run passes outside those same out-of-scope conformance
  failures.

## PR #1309 conformance remediation — rippled v3.2.0

- [x] Make Payment flags, preflight, preclaim, and Apply select the legacy MPT
      path only before MPTokensV2; route amendment-on MPT payments through Flow.
- [x] Preserve mpt_issuance_id in Payment path validation and serialization.
- [x] Make pathfinder index AMM liquidity in both directions, preserve hidden
      MPT assets on internal account nodes, reject maxed holdings/bad assets,
      and pass ledger timing plus all relevant amendments to every calculation.
- [x] Replace directional MPT transfer-rate rounding and partially funded
      book_offers conversion with exact nearest-even arithmetic.
- [x] Preserve permissioned-book domains and deterministic ordering in
      book_changes; align book_offers proof presence semantics.
- [x] Implement rippled's persistent path_find create/update/status/close state
      machine and canonical response amounts.
- [x] Treat an absent IOU issuer as not globally frozen in CheckCreate; make
      BookBase quality-zero and MPT ID parsing exact.
- [x] Port focused regression cases from rippled v3.2.0 for every finding and
      re-audit all changed production call sites.
- [x] Run formatting, focused/full tests, relevant conformance, vet, lint, and
      build; record exact results below.
- [x] Commit and push the completed remediation to the PR branch.

### Remediation review

- Reviewed against the exact local rippled v3.2.0 snapshot at
  `3c43f4614f87965298773279ff5b85d4c56c637b`. Both independent final audits are
  clean with no remaining blocking, minor, or nit findings.
- Resolved all 16 findings in the original conformance review plus adjacent
  audit findings in simulate validation, cumulative path ranking, exact
  full-liquidity retry selection, large-scale RPC Number precision, stale
  persistent-path updates, replacement ordering, and close latency.
- `just fmt`, focused Payment/engine/MPT/Check/Offer/keylet/ledger/RPC tests, and
  `go test -race ./internal/rpc/...` pass.
- `just vet`, `just lint` (0 issues), `just build-all`, and `just build-nocgo`
  pass.
- `just test` passes every package except the documented out-of-scope
  conformance suites. `just conformance --failing` reports 941 pass / 117 fail
  overall, with 879 pass / 0 fail in scope; the 117 failures remain confined to
  Batch, Vault, XChain, and XChainSim.

## PR #1309 base conflict resolution

- [x] Fetch the current `v3.0.0` base and inventory every conflicting hunk.
- [x] Resolve conflicts without regressing the rippled v3.2.0 behavior fixes.
- [x] Review the merged diff and run CI-pinned lint, vet, and build verification.
- [x] Commit and push the merge resolution; confirm GitHub reports the PR
      mergeable.

### Conflict-resolution review

- Merged the current `v3.0.0` base at
  `e4017e7e49e4e4d111e52343050d347ba0b85ddf`; the base and remote head were
  re-fetched and unchanged immediately before commit.
- Resolved the five conflicts in Payment preflight/apply code, the engine
  dispatch seam, transaction interfaces, and MPTokensV2 regression coverage.
  The combined result uses one `RulesAwarePreflighter` path for standalone,
  Batch-inner, and simulate validation; MPTokensV1 stays on direct transfer and
  MPTokensV2 routes through Flow.
- Rechecked the conflict decisions against rippled v3.2.0 `Payment.cpp` and
  retained both the base branch's broader MPT safety coverage and PR #1309's
  unique destination/Flow-routing regressions.
- `go test ./internal/tx/payment/...` and `go test ./internal/tx/engine/...`
  pass on the resolved tree.
- `just build`, `just vet`, and `just lint` pass; lint reports 0 issues.
- The staged merge has no unmerged paths, conflict markers, or whitespace
  errors.

# Issue #1306 — shared synthetic transaction metadata

## Plan

- [x] Trace every transaction-bearing RPC and stream response through the shared
      JSON rendering path on `v3.0.0`.
- [x] Compare NFT, offer, and MPT synthetic metadata behavior with the exact local
      rippled `3.2.0` tag.
- [x] Move synthetic-field injection to the narrowest shared renderer and remove
      handler-specific duplication.
- [x] Add focused regression tests for the shared renderer and affected RPC
      surfaces.
- [x] Run formatting, focused RPC tests, race coverage, vet, lint, and build.
- [x] Review the final diff for endpoint coverage, mutation safety, and rippled
      parity.

## Review

Implemented one shared JSON metadata enricher for `tx`, `account_tx`, simulate,
and validated transaction/account/book streams. Expanded ledger transactions
receive the MPT issuance ID but not NFT synthetic fields. `transaction_entry`
now leaves metadata untouched; despite the issue text, that is the exact rippled
3.2.0 behavior. API v2 `account_tx` also derives `delivered_amount` from the
unmodified transaction so `DeliverMax` response projection cannot erase the
Payment `Amount` fallback.

Oracle: clean local rippled tag `3.2.0` at
`3c43f4614f87965298773279ff5b85d4c56c637b`.

Verification:

- Focused API v1/v2 synthetic-field and endpoint tests pass.
- `go test ./internal/rpc/... ./internal/node/...` passes.
- `go test -race ./internal/rpc/... ./internal/node/...` passes.
- `just fmt`, `just vet`, `just lint`, `just build-all`, and `just test-core`
  pass; lint reports 0 issues.
- Independent final Go and rippled-conformance reviews found no defects.
