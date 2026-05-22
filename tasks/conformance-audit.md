# Conformance audit log

Append-only record of `finalizing-goxrpl-branch` runs. Each block records what
was reviewed against which `rippled/` SHA so subsequent finalizations can do
incremental reviews instead of re-reading rippled from scratch.

## 2026-05-21 — PR #473 — fix/issue-470-ledger-hashes-close
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/473
- Review comment: https://github.com/LeJamon/go-xrpl/pull/473#issuecomment-4510359118
- Files reviewed (Phase 1):
  - internal/consensus/rcl/engine.go — 2 findings (1 Minor peer-LCL gate, 1 Minor MovedOn comment), 0 blocking
  - internal/consensus/rcl/proposals.go — 0 findings
  - internal/ledger/ledger.go — 1 Minor (assertHistoricalSkipListConsistent stricter than rippled), 1 Nit (decodeUint32Field rigidity), 0 blocking
  - internal/ledger/openledger/apply.go — 0 findings + 1 Nit (logger nil-handling)
  - internal/ledger/openledger/txqadapter.go — 0 findings
  - internal/ledger/service/ledger_query.go — 0 findings
  - internal/ledger/service/service.go — 0 findings (sibling-fork chain switch verified correct)
  - internal/ledger/state/affected_node.go — 0 findings
  - internal/ledger/state/directory.go — 1 Minor (book-vs-non-book heuristic vs rippled's explicit preserveOrder bool), 0 blocking
  - internal/peermanagement/discovery.go — 0 findings
  - internal/peermanagement/overlay.go — 0 findings
  - internal/tx/apply_state_table.go — 1 Minor (EmitEmptyPreviousFields STI_NOTPRESENT proxy fragile vs future MetaAlways-only fields), 1 Nit (sfFlags meta comment mis-described), 0 blocking
  - internal/tx/serialize.go — 0 findings
  - shamap/inner_node.go — 0 findings (comment-only change)
  - shamap/shamap.go — 1 Nit (SetChild loop comment misleading), 0 blocking
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — prior commit dd54397 already covered the cleanup; cleaning-ai-comments pass was a no-op
- Notes: All Minor findings are documented divergences with defensible rationale or near-term-correct heuristics; no blockers. Two flagged comment-accuracy nits (apply_state_table.go:2031-2032, shamap/shamap.go:558-569) left in place because the surrounding rationale is load-bearing and a surgical edit would be a content rewrite rather than a janitorial removal.

## 2026-05-22 — PR #509 — fix/issue-499-invariant-gaps
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/509
- Review comment: https://github.com/LeJamon/go-xrpl/pull/509#issuecomment-4518444212
- Files reviewed (Phase 1):
  - internal/ledger/ledger.go — 0 new findings (prior #473 nits unchanged; +6-line delta)
  - internal/ledger/service/snapshot_view.go — 0 findings
  - internal/rpc/types/services.go — 0 findings (1-line interface method addition)
  - internal/tx/amm/amm_create.go — 1 Blocking (LsfAMM flag-mask divergence)
  - internal/tx/apply_state_table.go — 0 new findings (prior #473 nits unchanged; +6-line delta)
  - internal/tx/apply_state_table_test.go — 0 findings
  - internal/tx/engine.go — 0 findings (LedgerSeq interface addition)
  - internal/tx/invariants/basic.go — 1 Blocking (XChainAddAccountCreateAttestation string typo), 2 Minor (M3 dup UInt32, M4 missing seq/flags), 0 Nit
  - internal/tx/invariants/invariants.go — 0 findings
  - internal/tx/invariants/invariants_test.go — 1 Blocking (mirror of basic.go B1), 1 Nit (missing test coverage)
  - internal/tx/invariants/offers.go — 1 Minor (IOU badCurrency missing), 1 Minor (XRPNotCreated message fidelity, no-fix), 1 Nit (Signum cleanup)
  - internal/tx/payment/flow_test.go — 0 findings
  - internal/tx/payment/pathfinder/pathfinder_test.go — 0 findings
  - internal/tx/payment/payment_xrp.go — 0 findings (LookupDestination switched to IsPseudoAccount via B2 fix)
  - internal/tx/payment/sandbox.go — 1 Nit (LedgerSeq fallback ordering)
  - internal/tx/trustset/trustset.go — 0 findings (switched to IsPseudoAccount via B2 fix)
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Pre-Phase-1 commit: c1a63c6 — chore(invariants): drop unused stubTx test helper (unblocked just lint)
- Review-fix commit: 3df37f3 — review(#499): address rippled-conformance findings (all 2 Blocking + 5 Minor + 3 Nit resolved, incl. AccountRoot.IsPseudoAccount helper + 4 new tests)
- Cleanup commit: 3caa79e — chore: clean ai-generated comments (2 section-label removals; rest were rippled citations / non-obvious whys, kept as load-bearing)
- Notes: B2 (LsfAMM) had broader blast radius than the review suggested — 4 production detection sites + 2 test assertions switched to AccountRoot.IsPseudoAccount() (mirrors rippled View.cpp:1138 isPseudoAccount). LsfAMM constant removed entirely; bit 0x02000000 collides with rippled's lsfTshCollect (hooks) and lsfLowDeepFreeze (RippleState). Wire-format collision risk surfaced by the review and tracked as out-of-scope for a future gap audit.

## 2026-05-22 — PR #511 — fix/issue-493-resource-manager
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/511
- Review comment: https://github.com/LeJamon/go-xrpl/pull/511#issuecomment-4518655441
- Files reviewed (Phase 1):
  - internal/peermanagement/resource/charge.go — 0 findings
  - internal/peermanagement/resource/consumer.go — 1 Minor (unlimited consumers debited local balance vs Consumer.cpp:106-114), 0 blocking
  - internal/peermanagement/resource/decay.go — 1 Minor (sub-second anchor not advanced vs DecayingSample.h:96), 0 blocking
  - internal/peermanagement/resource/disposition.go — 0 findings
  - internal/peermanagement/resource/fees.go — 1 Blocking (goimports alignment) — pure-Go lint, not conformance
  - internal/peermanagement/resource/gossip.go — 0 findings
  - internal/peermanagement/resource/kind.go — 0 findings
  - internal/peermanagement/resource/manager.go — 3 Minor (warn() nanosecond rate-limit gate, normalizeAddr byte-scan IPv6 mishandling, stale whenExpires on reactivation), 0 blocking; plus 2 Minor test-coverage gaps closed in fix (readmission-after-blacklist, re-import from same origin)
  - internal/peermanagement/resource/manager_test.go — coverage expanded with TestDrop_BlacklistAndReadmit and TestImport_ReplacesPriorContributionFromSameOrigin
  - internal/peermanagement/resource/tuning.go — 0 findings (all 6 constants byte-match Tuning.h)
  - internal/peermanagement/overlay.go — 0 findings (PeerDisconnectsResources now sources from real charge counter, retires PR #473 stand-in)
  - internal/peermanagement/peer.go — 1 Minor (concurrent Drop could over-count peerDisconnectsCharges vs PeerImp.cpp:352-361 strand serialisation), 1 Nit (lazy-Manager fallback under-described), 0 blocking
  - internal/peermanagement/bad_data_test.go — 0 findings
  - internal/peermanagement/peers_json_test.go — 1 Blocking (goimports alignment)
  - internal/peermanagement/squelch_test.go — 0 findings
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Fix commit: ec3020a — review(#493): address rippled-conformance findings (all blocking + minor + nit fixed)
- Cleanup commit: c951eed — chore(#493): clean ai-generated comments (-55 net lines)
- Notes: New resource/ subsystem ports rippled's Resource::Manager (Logic/Consumer/Charge/Fees/Tuning/DecayingSample/Gossip). The user opted to address ALL findings (blocker + minor + nit) in-PR rather than defer nits. Concurrent-Drop fix introduced new chargeDropFired atomic.Bool on Peer with corresponding once-per-peer assertion in TestPeer_Charge_DropDisconnects.
