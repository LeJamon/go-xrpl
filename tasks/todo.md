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
