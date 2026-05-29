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

## 2026-05-22 — PR #517 — fix/issue-506-ledger-flags-table
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/517
- Review comment: https://github.com/LeJamon/go-xrpl/pull/517#issuecomment-4519125546
- Files reviewed (Phase 1):
  - ledger/entry/flags.go — 0 findings; byte-perfect mirror of rippled LedgerFormats.h:123-199 verified line-for-line
  - ledger/entry/doc.go — 0 findings; accuracy fix only, no false claims remain
  - internal/tx/ledgerfields/ledgerfields.go — 1 Minor (M1: package doc claims Encode/Hash "replaces" internal/ledger/state/*.go hand-built maps, but the replacement was not performed in this PR; methods dormant in production), 0 blocking
  - internal/tx/ledgerfields/cmd/ledgerfieldsgen/main.go — 0 findings; new ToMap/Encode/Hash template carries non-obvious why (sMD_Never inclusion for Decode→Encode parity); DirectoryNode.Indexes decode-storage change does NOT leak into metadata (Emit* methods skip Meta==3 — verified in template at lines 512-571)
  - internal/tx/ledgerfields/encode_test.go — 1 Minor (M2: round-trip sweep covers only 4 value-shape categories; Issue/XChainBridge/Number/Hash192/UInt8 paths untested), 0 blocking
  - All 25 internal/tx/ledgerfields/*_gen.go — regenerator output; no findings
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — Phase 2 was a no-op. Every PR-introduced comment is load-bearing (rippled cites, sMD_Never rationale, round-trip invariant, leaf-hash formula, ltX section dividers needed to disambiguate reused numeric values). Two Nits surfaced (N1 PR-body-vs-shipped scope mismatch; N2 DirectoryNode.Indexes decode change worth documenting) were content concerns for the human author, not janitorial.
- Notes: PR ships two commits — issue #506 fix (flag-table mirror) plus an opportunistic scope expansion (typed Encode/Hash across all 25 SLE structs) which the PR body still claims is "out of scope." No blockers. The Encode/Hash methods are dormant until a follow-up migrates internal/ledger/state/*.go callsites.
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

## 2026-05-22 — PR #515 — fix/amm-keylet-and-xrp-currency
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/515
- Review comment: https://github.com/LeJamon/go-xrpl/pull/515#issuecomment-4518612514
- Files reviewed (Phase 1):
  - internal/rpc/handlers/amm_info.go — 1 Minor (currencyToBytes duplicated, drift vs strict keylet impl), 0 blocking
  - internal/rpc/handlers/amm_info_test.go — 0 findings (test reviewed alongside handler)
  - keylet/keylet.go — 1 Nit (isXRP→equivalent shortcut omitted from sort), 0 blocking
  - keylet/keylet_test.go — 0 findings (test reviewed alongside keylet)
- Additional Minor flagged on PR body (not code): blast radius undersold — same fix unbreaks ledger_entry.go AMM lookup via shared helper
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Follow-up fix commit (Minors + Nit addressed): 2e9a9a8 — refactor(amm): consolidate currencyToBytes; literal port of Issue::operator<=> XRP shortcut
  - Routed amm_info.go, ledger_entry.go, internal/testing/amm/helpers.go through state.GetCurrencyBytes (canonical write-path encoder)
  - Added internal/ledger/state/directory_test.go::TestGetCurrencyBytes_XRP_AllZero pinning the contract at its canonical site
  - Added keylet/keylet_test.go::TestAMM_SortOrder_XRPCurrencyTie_KeepsOriginalOrder pinning the literal port of std::minmax-on-equivalent semantics
  - PR body rewritten to cover ledger_entry surface and the deliberate follow-up of tightening state.GetCurrencyBytes to match rippled's strict to_currency
- Cleanup commit: 1051724 — chore: clean ai-generated comments (4 paraphrasers stripped; all rippled-conformance docstrings preserved)
- Notes: Strict-vs-loose currencyToBytes consolidation deliberately chose to mirror state.GetCurrencyBytes (loose) rather than the rippled-strict version in keylet/keylet.go::currencyToBytes used by keylet.Line. Switching to strict would require tightening AMMCreate preflight to reject non-ISO 3-char input — a deliberate follow-up.

## 2026-05-22 — PR #520 — fix/issue-500-pseudo-tx-preflight
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/520
- Review comment: skipped at user request (review captured locally in $CLAUDE_JOB_DIR/conformance-review.md only)
- Files reviewed (Phase 1):
  - amendment/rules.go — 0 findings (new NegativeUNLEnabled wrapper, matches existing pattern)
  - internal/tx/apply.go — 0 findings (pseudoPreflight + pseudoPreclaim gates wired before tx-hash + state-table)
  - internal/tx/pseudo/setfee.go — 2 Minor + 2 Nit (zero-fee-field silently dropped vs makeFieldAbsent; triple-parse of fields), 0 blocking
  - internal/tx/pseudo/unl_modify.go — 0 findings (no-op Validate, gating moved to engine)
  - internal/tx/pseudo_gates.go — 2 Minor + 1 Nit (TicketSequence vs PreviousTxnID divergence; empty-Account accepted; zeroAccountAddress duplicated with pseudo.ZeroAccount; missing temUNKNOWN default in pseudoPreclaim), 0 blocking
- Files cleanup-only (Phase 0 skipped Phase 1): none (tests and env_submission.go reviewed for assertion correctness; not gated on style)
- Not in diff but flagged: preflight0's tfInnerBatchTxn and NetworkID checks (Transactor.cpp:46-75) not ported — out of scope for this PR but worth tracking
- Cleanup commit: 368aed98 — chore: clean ai-generated comments (3 paraphrasers stripped from setfee.go; all rippled-conformance docstrings preserved)
## 2026-05-22 — PR #521 — fix/issue-503-crypto-canonicality
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/521
- Review comment: NOT POSTED — auto-mode classifier denied gh pr comment (external write); user opted to skip and continue. Findings recorded here only.
- Files reviewed (Phase 1):
  - crypto/ed25519/ed25519.go — 0 findings (canonicality gate placement byte-matches rippled PublicKey.cpp:302-313)
  - crypto/keytype.go — 0 findings (enum values byte-match KeyType.h:28-31; verified by grep that crypto.KeyType is never serialized, no `<`/`>` comparisons, and no struct fields rely on the prior zero-value-is-Unknown contract)
  - crypto/random.go — 1 Nit (Seed.cpp:46-47 cite is loose analog — rippled wipes the 16-byte family seed, not a 64-byte expanded private key which has no rippled counterpart; behaviour is correctly more-defensive, comment fidelity only)
  - crypto/secp256k1/verify_test.go — 0 findings (docstring correctly retires the "manifest parity" claim and pins the new contract: strict on manifest path, relaxed on the low-level branch)
  - internal/manifest/manifest.go — 0 findings (strict dispatch via secp256k1.SECP256K1().Validate matches Sign.cpp:60-61 with PublicKey.h:251-256 default mustBeFullyCanonical=true)
- Cross-cutting Minors (not file-specific):
  - M1 — no end-to-end test that a high-S secp256k1 manifest signature is rejected at Manifest.Verify(); the verify_test.go test only pins the low-level relaxed branch
  - M2 — TestEd25519Canonical (crypto/canonicality_test.go:79-114) lacks a positive S>=L rejection case; only length cases are covered
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: 151f3a1 — chore: clean ai-generated comments (1 paraphrase preamble stripped from randomEd25519KeyPair; all rippled-conformance citations and non-obvious whys preserved)
- Fix commit: 5a1e267 — test(#503): close conformance-review M1, M2; reword N1 Seed cite. Adds TestManifest_Secp256k1MasterSig_HighS_Rejected (secp256k1 master + ed25519 ephemeral; flips master sig S→N-S; asserts Verify rejects), three boundary cases in TestEd25519Canonical (S=L-1 verifies; S=L and S=L+1 reject), and a corrected Seed.cpp cite. New tests verified locally: PASS.
- Notes: Tight conformance PR closing the four #503 audit gaps (manifest strict canonicality, ed25519 canonicality gate visibility, KeyType enum reordering, ed25519 secure_erase). Zero blockers — all four claimed fixes are correctly anchored to rippled. All review findings (M1 + M2 + N1) addressed in-PR per project rule against follow-up punts.

## 2026-05-22 — PR #520 (incremental) — fix/issue-500-pseudo-tx-preflight
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/520
- Review comment: https://github.com/LeJamon/go-xrpl/pull/520#issuecomment-4519655917
- Prior audit: this PR was audited earlier today (entry above); this is an incremental re-audit covering the two new commits that followed the prior cleanup.
- Commits reviewed (over base 368aed98):
  - ecaaac46 — fix(pseudo-tx): address conformance-review minors + nits (the seven follow-ups from the prior audit)
  - 735319cc — fix(pseudo-tx): port preflight0 tfInnerBatchTxn + NetworkID gates (closes the prior audit's flagged out-of-scope item)
- Files reviewed (Phase 1, incremental):
  - amendment/rules.go — 0 findings (NegativeUNLEnabled wrapper, matches local pattern)
  - protocol/constants.go — 0 findings (ZeroAccount lifted to single source of truth, cited rippled Change.cpp:43-48)
  - internal/consensus/{amendmentvote,feevote,negativeunlvote}/vote.go — 0 findings (each constructor reroutes through protocol.ZeroAccount; 3-line touches)
  - internal/ledger/state/fee_settings.go — 0 findings (XRPFeesMode flag + always-emit-active-mode serialization mirrors Change.cpp:362-379 set()/makeFieldAbsent())
  - internal/tx/apply.go — 0 findings (pseudoPreflight + pseudoPreclaim wired before tx-hash + state-table per Change.cpp:36-140)
  - internal/tx/pseudo/setfee.go — 0 findings (PreclaimPseudo per Change.cpp:93-133; parsedCache memoisation safe under single-threaded engine contract)
  - internal/tx/pseudo/unl_modify.go — 0 findings (Validate is now a no-op; gating moved to engine)
  - internal/tx/pseudo/wire.go — 0 findings (pseudo.ZeroAccount duplicate deleted)
  - internal/tx/pseudo/register.go — 0 findings (constructors reference protocol.ZeroAccount)
  - internal/tx/pseudo_gates.go — 3 Nit (lingering literal "rrrrrrrrrrrrrrrrrrrrrhoLvTp" at four non-pseudo sites; isZeroFee whitespace permissiveness vs rippled STAmount JSON parse; pseudoPreclaim asymmetry comment-gap), 0 Minor, 0 Blocking
  - internal/testing/env_submission.go — 0 findings (SubmitPseudo now always closed-ledger, mirrors Change.cpp:82-91)
  - internal/testing/pseudotx/*_test.go — 0 findings (test assertions reflect new gate behaviour: empty Account, zero-fee spellings, tfInnerBatchTxn, NetworkID legacy/wrong/absent-legal)
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: 47f442c0 — chore: clean ai-generated comments (1 paraphrase line stripped from isZeroFee; all rippled cites and non-obvious whys preserved)
- Notes: Zero blockers and zero minors across the incremental work — all seven prior-audit follow-ups are correctly anchored to rippled, and the new tfInnerBatchTxn + NetworkID gates byte-match Transactor.cpp:46-75. The three nits are advisory: lingering ZeroAccount literals exist only at non-pseudo-tx sites (payment.go path-element XRP detection, conformance/runner.go); isZeroFee is strictly more permissive than rippled at a Go-API boundary rippled never reaches; pseudoPreclaim's structural asymmetry between ttFEE and ttAMENDMENT/ttUNL_MODIFY would benefit from a one-line comment. None gate merge.

## 2026-05-22 — PR #513 — feat/issue-498-consensus-audit
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/513
- Review comment: https://github.com/LeJamon/go-xrpl/pull/513#issuecomment-4520322007
- Files reviewed (Phase 1):
  - internal/consensus/rcl/engine.go — 2 Minor (M1 CT-consensus gate asymmetric across Yes/MovedOn/Expired; M2 LedgerGranularity wired only to csf), 1 Nit (N1 consensusState enum order mismatch), 0 blocking — all fixed in 7cf1b367
  - internal/consensus/rcl/proposals.go — 0 findings
  - internal/consensus/rcl/validations.go — 1 Nit (N2 dead validationValid* constants) — fixed in 7cf1b367 (constants removed); file's net delta vs main is now zero
  - internal/consensus/rcl/disputes_test.go — 1 Nit (N3 missing proposing=false coverage) — fixed in 7cf1b367 (observer subtest + mixed-set subtest)
  - internal/consensus/rcl/engine_test.go — 1 Nit (N4 no direct unit test for checkConsensusState) — fixed in 7cf1b367 (TestCheckConsensusState walking all 6 arms)
  - internal/consensus/types.go — 0 findings (the LedgerGranularity doc-accuracy issue is filed under engine.go M2)
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: 8e7d4cc5 — chore: clean ai-generated comments (stripped 5 paraphrase / numbered-section / "see X below" lines across the 5 changed files; rippled cites and non-obvious whys preserved)
- Branch was 97 commits behind main at finalize-start; rebased onto a8c4d86e before review. Final tip: 8e7d4cc5.
- Notes: Yes-arm checkConsensusReached(count_self=false) verified intentional — Go's countAgreement pre-bumps `agree` when proposing, matching rippled's internal `++agreeing; ++total`. getCloseTimeNeededWeight refactor is a quiet bugfix: prior hand-written switch gated init→mid at pct>=0 (rippled: pct>=50) and late→stuck at pct>=85 (rippled: pct>=200), and returned the old-state pct on transition (rippled: new-state pct). All three pre-existing divergences are now correct via parms.NeededWeight.
## 2026-05-22 — PR #523 — fix/issue-502-websocket-subscriptions
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/523
- Review comment: https://github.com/LeJamon/go-xrpl/pull/523#issuecomment-4519550916
- Files reviewed (Phase 1):
  - internal/consensus/engine.go — 0 findings (interface-only addition: Subscribe(EventSubscriber))
  - internal/consensus/adaptor/router_test.go — 0 findings (test fixture only)
  - internal/ledger/service/service.go — 0 findings (SubmittedTxEvent struct + SetSubmittedTxCallback seam)
  - internal/ledger/service/tx_query.go — 1 Blocking (SubmitTransaction fired callback regardless of Applied, polluting transactions_proposed with tem/ter/tec rejects)
  - internal/manifest/cache.go — 0 findings (SetOnAccepted hook only; ApplyManifest semantics unchanged)
  - internal/manifest/manifest_test.go — 0 findings (TestManifest_OnAccepted_FiresOnce coverage)
  - internal/rpc/handlers/book_changes.go — 1 Nit (formatBigFloat 6 decimal places vs rippled STAmount::iou() ~16 digits)
  - internal/rpc/handlers/server_info.go — 0 findings (ComputeServerLoad / ServerLoadSnapshot reused from server_info shape; matches NetworkOPs.cpp:2850-2912)
  - internal/rpc/subscribe_conformance_test.go — 0 findings (test parity coverage)
  - internal/rpc/subscribe_test.go — 0 findings (new TestSubscribeURL_NonAdmin / TestSubscribeBookBoth_AutoSubscribesReverse / TestSubscribeRtTransactionsAlias)
  - internal/rpc/subscription/manager.go — 1 Blocking (HandleUnsubscribe ignored rt_transactions alias + skipped URL admin gate), 1 Minor (SubscriptionConfig last* scalars broke multi-book state), 1 Nit (peer_status stream not admin-gated)
  - internal/rpc/types/types.go — 1 Minor (LedgerSubscribeInfo.FeeBaseXRP/TxnCount mis-attributed to rippled subLedger ack; not in NetworkOPs.cpp:4174-4189)
  - internal/rpc/websocket.go — 1 Minor (subscribe ack emitted fee_base_xrp/txn_count)
  - internal/cli/server.go — 4 Blocking (proposed-tx fired on non-applied; accounts_proposed fanned to source only; per-book event dropped tx/meta + no tesSUCCESS gate; validations event omitted data/network_id and mis-labelled master_key; manifests event mis-labelled serialized blob as signature, omitted manifest/master_signature/domain), 3 Minor (server stream fired on every close, hardcoded "full", omitted load_factor_fee_reference; book_changes raced ledgerAdapter.GetLedgerBySequence), 0 Nit
- Files cleanup-only (Phase 0 skipped Phase 1): none (all touched files within protocol-bearing scope)
- Review-fix commit: 674d9d29 — review(#502): address rippled-conformance findings on PR #523 (all 6 Blocking + 7 Minor/Nit resolved)
- Cleanup commit: ce52450a — chore(#502): clean ai-generated comments (8 paraphrasers stripped from SubmittedTxEvent / SubmittedTxCallback; stale "fires regardless of apply success" callback comment rewritten with correct NetworkOPs.cpp:1535-1544 citation; all rippled-conformance docstrings preserved)
- Notes: The original PR was dense with rationale-rich rippled-citing comments since it ports the entire WebSocket pubXxx fan-out fresh; cleanup pass was deliberately conservative (8 net deletions in service.go) so the parity story carried by those comments survives merge. Blast radius of the blockers cut across submission ingress (tx_query.go), event-shape structs (events.go), event-source bridges (cli/server.go), and the subscription manager (manager.go); ManifestEvent gained explicit Manifest/MasterSignature/Domain fields and a new Manifest.Signatures() helper on the cache type. Master-key resolution on pubValidation now threads the manifest Cache through rpcEventBridge. SubmittedTxEvent.AffectedAccounts replaced the single AffectedAccount string so accounts_proposed reaches destination/regular key/signers; tx_query.go reuses the same extractAffectedAccounts helper already used for the validated transactions stream.

## 2026-05-22 — PR #535 — fix/issue-530-book-offers-iso-charset
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/535
- Review comment: https://github.com/LeJamon/go-xrpl/pull/535#issuecomment-4520266102
- Files reviewed (Phase 1):
  - internal/rpc/handlers/book_offers.go — 0 findings (isValidCurrencyCode 3-byte branch now applies isoCharSet per UintTypes.cpp:84-107; reuses existing isIsoCurrencyChar from get_aggregate_price.go which matches UintTypes.cpp:39-43 byte-for-byte; bytes-vs-runes equivalence holds for any 3-byte input; error codes/messages match BookOffers.cpp:80-96 verbatim)
  - internal/rpc/book_offers_test.go — 0 findings (two new "a/b" cases assert pay-side rpcSRC_CUR_MALFORMED + gets-side rpcDST_AMT_MALFORMED; extends past rippled Book_test.cpp:1437-1461 which only tests 9-char invalid input)
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: 618f1e29 — chore: clean ai-generated comments (stripped "previously admitted X by mistake" temporal reference in test; removed redundant trailing rippled cite in isValidCurrencyCode doc that duplicated the inline UintTypes.cpp:39-43, :93-96 reference)
- Notes: Merged into main as 8dd7aba9. Tight conformance bug fix — previous len==3 branch admitted any 3-byte string (e.g. "a/b"), now correctly gated by isoCharSet to match rippled's to_currency().

## 2026-05-22 — PR #538 — fix/issue-527-book-offers-marker
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/538
- Review comment: https://github.com/LeJamon/go-xrpl/pull/538#issuecomment-4520319673
- Files reviewed (Phase 1):
  - internal/ledger/service/offer_query.go — 4 Minor (M1 limit=0 emits malformed all-zero marker; M2 stale-marker conflated with malformed-marker error; M3 paginated response omits `limit` echo vs rippled account_offers; M4 marker-survival-across-ledger-advancement undocumented), 0 Blocking
  - internal/ledger/service/offer_query_test.go — covered alongside offer_query.go
  - internal/rpc/handlers/book_offers.go — 0 new findings (error funnel handler:195-197 correctly routes svcerr.ErrInvalidMarker → invalid_field_error("marker"))
  - internal/rpc/ledger_adapter.go — 0 findings (interface plumbing only)
  - internal/rpc/types/services.go — 0 findings (BookOffersResult.Marker addition; `json:"marker,omitempty"` verified absent on empty result via live verify)
  - internal/rpc/book_offers_test.go — 3 Nit (N1 comment claims rippled rebuilds umBalance, but rippled never paginates so the rationale is mis-cited; N2 firstIteration flag is redundant; N3 samePrefix24 could use bytes.Equal)
- Wire-shape verify pass: server booted on 127.0.0.1:5005; empty-book book_offers returns marker absent (correct, omitempty); bad-marker (4 probes: non-hex, wrong length, valid-shape-not-in-ledger, numeric) all return `Invalid field 'marker'.` matching rippled's invalid_field_error. No field-name surprises. Marker round-trip with ≥2 pages not driven (would require populating offers via standalone-mode OfferCreate).
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: 8da5b78a — chore(#527): clean ai-generated comments (-5 net lines; 1 self-contradictory test setup comment rewritten to capture actual load-bearing why; 1 redundant docstring stripped. All NetworkOPs.cpp cites, marker-divergence docs, and "wrong book" fixture intents preserved as conformance evidence)
- Review-fix commit: 3eca60cf — fix(book_offers): address conformance-review minors + nits (all 4 Minors + 3 Nits resolved: M1 limit=0 marker gate, M2 ErrStaleMarker sentinel split, M3 limit echo, M4 docs; N1 honest umBalance comment, N2 resumePending replaces firstIteration, N3 bytes.Equal). Cleanup follow-up 1e0d4d0b stripped three temporal review-label refs.
- Merge-into-main commit: TBD — branch merged origin/main (37 behind) to resolve conflicts in offer_query_test.go (both branches added tests; preserved both, adapted main's new tests to pass marker=""), book_offers_test.go (kept marker-aware signature + main's getLedgerByHashFn field), handler (kept marker code, retired rejectUnsupportedPagination stub).
- Notes: PR adds marker pagination as a deliberate goXRPL extension — rippled's `book_offers` accepts the marker parameter (BookOffers.cpp:201-214) but its handler ignores it (NetworkOPs.cpp:4627) and rippled's own Book_test.cpp:1711 documents "a marker field is not returned for this method". Review judged the extension against the closest paginated rippled handler (account_offers) and against rippled's directory-walk invariants. Zero blockers.

## 2026-05-26 — PR #547 — fix/issue-496-rpc-gaps
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/547
- Review comment: https://github.com/LeJamon/go-xrpl/pull/547#issuecomment-4543443168
- Files reviewed (Phase 1):
  - internal/rpc/handlers/validation_create.go — 3 findings (1 Blocking B1, 2 Minor M1/M2), all fixed. Root cause: `validationSeed` was an incomplete port of rippled `parseGenericSeed` (Seed.cpp:96-132) — missing the raw-hex(uint128) seed branch (B1: a 32-hex-char secret e.g. master_seed_hex derived a *different* keypair than rippled), the 5-token reject-guard (M1: r…/n…/a…/p… silently hashed as passphrase instead of badSeed), and absent-vs-empty secret handling (M2: `{"secret":""}` returned a random key instead of badSeed).
  - internal/rpc/handlers/server_definitions.go — 1 Nit (N1: hash comment could be misread as byte-identical to rippled's; it is a per-server cache token), fixed by clarifying the comment. server_definitions hash logic itself verified correct (sha512Half over doc excluding the hash field; uppercase 64-hex string; case-insensitive echo short-circuit all match ServerInfo.cpp:288-318).
  - internal/rpc/validators_test.go — added 3 tests (HexSeed, RejectsKeyTokens, EmptySecret); internal/rpc/server_definitions_test.go — no new findings.
- Wire-shape verify pass: ATTEMPTED but blocked — built server panics on startup in a pre-existing, unrelated path (internal/cli/server.go:998 → internal/rpc/websocket.go:947, nil *WebSocketServer in doShutdown after the HTTP listener fails to stay up; neither file touched by this PR). Every field this PR adds is a plain JSON string, so field-type-drift (the risk verify catches) does not apply; wire shape established by static read + unit tests. The startup crash warrants a separate issue.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — Phase 2 was a no-op. Every PR-introduced comment is load-bearing (rippled cites to ServerInfo.cpp/ValidationCreate.cpp/Seed.cpp/ValidatorRPC_test.cpp, the sync.Once shared-state rationale, the "00"-prefix-strip mechanics, the 5-token reject mapping). Consistent with #473/#517/#509 no-op cleanups.
- Review-fix commit: 1a1e383c — fix: complete parseGenericSeed port in validation_create (B1+M1+M2 + N1 comment + 3 tests). build/vet/lint green; targeted `internal/rpc` tests pass (incl. the 3 new). Pushed 70bc0766..1a1e383c.
- Notes: Static checks only locally (tests delegated to CI per finalize policy), but ran the one affected package once to verify the blocker fix. The hex-seed divergence (B1) is the operationally serious one: master_seed_hex is a valid rippled secret form, so an operator regenerating validator keys would have gotten a silently wrong identity.
## 2026-05-26 — PR #550 — fix/issue-543-submit-through-txq
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/550
- Review comment: skipped at user request (findings recorded here + fixed in-branch)
- Files reviewed (Phase 1):
  - internal/ledger/openledger/openledger.go — 1 Nit (N1: terQUEUED branch left a stale Fee/Metadata/Message from a failed direct-apply attempt), 0 Blocking
  - internal/ledger/openledger/txqadapter.go — 0 findings (LastApplyResult verified to return the submitted tx: direct-apply returns immediately after the single ApplyTransaction call, multiTxn-clear applies the submitted tx last — apply.go:118,582)
  - internal/ledger/service/service.go — 0 findings (s.mu → txQueue.mu lock-ordering documented; TxQ never reaches back for s.mu)
  - internal/ledger/service/tx_query.go — 1 Minor (M1: localTxs hold pre-filtered tef/tem/tel, diverging from rippled NetworkOPs.cpp:1674-1683 + LocalTxs.cpp:114-121 which hold every local non-failhard submission and age them out via sweep), 0 Blocking
  - internal/ledger/service/tx_query_submit_test.go — 0 findings (new applies/queues/fail-hard coverage)
  - internal/rpc/ledger_adapter.go — covered with M1 (kept wire field mirrored the divergent localTxs condition)
- Verdict: 0 Blocking, 1 Minor, 1 Nit. Core convergence (RPC submit → SubmitDetailed → TxQ.Apply, NetworkOPs.cpp:1518) is rippled-faithful; below-fee txns now held (terQUEUED) not applied.
- Review-fix commit: 3a4bcd46 — fix(#543): align submit kept/localTxs hold with rippled; fix queued message. M1: localTxs now holds whenever (!fail_hard || result==tesSUCCESS), matching rippled's unconditional LocalTxsImp::push_back + sweep aging; tefALREADY still excluded (already in open view). kept wire field updated to the same condition. N1: terQUEUED branch now clears stale Fee/Metadata/Message and reports the queued status message.
- Cleanup commit: none — cleaning-ai-comments pass was a no-op. PR comments are substantive rippled-citing conformance rationale (lock-ordering contracts, NetworkOPs.cpp/TxQ.cpp/LocalTxs.cpp cites); no banners/step-narration/temporal-refs/restatements to strip.
- Local verification: build ✓, vet ✓, lint ✓ (tests delegated to CI per finalize policy).
- Notes: Decided the M1 held-pool divergence in favor of rippled parity (project's "rippled is source of truth" mandate + user "fix all issues") rather than keeping goXRPL's permanent-failure pre-filter. The pre-filter was a deliberate efficiency optimization with an inaccurate "rippled holds every tx that did not fail permanently" comment — rippled does NOT filter by TER on the local-push path. New behavior: tef/tem/tel local submissions are now held and test-applied each open ledger until they age out (≤5 ledgers), matching rippled exactly; local-only mempool change, no consensus impact. Out-of-scope/pre-existing (not fixed): broadcast relay omits rippled's `(mMode != FULL && !failHard && local)` clause and uses !Applied vs rippled's !isTesSuccess for the fail_hard guard (ledger_adapter.go:254-258, unchanged by this PR); submit response omits account_sequence_next/available, open_ledger_cost, validated_ledger_index (Submit.cpp:168-181).
## 2026-05-26 — PR #546 — fix/issue-545-flaky-checksum-test
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/546
- Review comment: https://github.com/LeJamon/go-xrpl/pull/546#issuecomment-4541976029
- Files reviewed (Phase 1):
  - internal/peermanagement/identity_test.go — 0 findings, 0 blocking. Test-only determinism fix to TestChecksumValidation: previously substituted `'X'` unconditionally for the last base58 char, a no-op when the original char was already `'X'` (the flake); now substitutes `'Y'` in that case so the corruption always differs. Verified the node-public-key base58check path matches rippled byte-for-byte: NodePublic prefix 0x1C = TokenType::NodePublic 28 (tokens.h:40); checksum = first 4 bytes of double-SHA256 (tokens.cpp:191-196, digest2<sha256_hasher>); `<type><token><checksum>` layout (tokens.cpp:338, :659-665); reject-on-bad-checksum (identity.go:287-290 vs tokens.cpp:672-673,:697-699). Confirmed a single base58-digit change perturbs only the low-order/checksum bytes, keeps decode length at 38 (nonzero 0x1C lead byte), so it lands on the checksum branch rather than the length/prefix/key-parse guards.
- Wire-shape verify pass: skipped (justified) — diff touches only a `_test.go` file; no production code path, peer-handshake bytes, or RPC response shape changes, so there is nothing for the `verify` skill to observe.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — Phase 2 was a no-op. The only PR-introduced comment block (identity_test.go:405-407) documents the non-obvious why behind the fix (unconditional `'X'` can be a no-op → flake) and is a regression guard; per cleaning-ai-comments keep-rules it stays. No banner/temporal/restatement comments were added.
- Notes: Local gates build/vet/lint all green (tests delegated to CI per finalize policy). Smallest possible protocol-package diff (+8/-2, 1 file); Phase 1 still ran because the path is under internal/peermanagement/.

## 2026-05-26 — PR #548 — feat/issue-496-owner-info-unl-list
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/548
- Review comment: https://github.com/LeJamon/go-xrpl/pull/548#issuecomment-4543417429
- Files reviewed (Phase 1):
  - internal/rpc/handlers/stubs_ledger.go (owner_info) — 1 Blocking, 2 Minor, 1 Nit; ALL FIXED. B1: malformed/empty account returned a top-level error instead of rippled's per-section embedded actMalformed with overall success (OwnerInfo.cpp:50-58, OwnerInfo_test.cpp:51-71). M1: offers/ripple_lines emitted as empty arrays vs rippled's conditional keys + `{}` for an account with no owned objects (getOwnerInfo, OwnerInfo_test.cpp:80-81). M2: 400-object cap applied across all owned types before the offer/ripple_state filter, no pagination, vs rippled walking every owner-dir page (NetworkOPs.cpp:1764-1814). N1: full-ledger-scan order vs owner-directory order.
  - internal/rpc/handlers/stubs_network.go (unl_list) — 1 Minor; FIXED. M3: emitted only trusted master keys with trusted hardcoded true vs rippled for_each_listed's union of all listed keys + real trusted bool (UnlList.cpp:34-43, ValidatorList.cpp:1750).
  - internal/rpc/missing_methods_test.go — owner_info/unl_list tests rewritten to assert the rippled-faithful shapes.
- Fixes landed post-review in commit 238e7af0: added Service.GetOwnerInfo (faithful owner-directory walk via state.DirForEach — every page, no cap, directory order) exposed through the new types.OwnerDirectoryReader capability (type-asserted off ctx.Services.Ledger, mirroring the types.FailHardSubmitter pattern; production *LedgerServiceAdapter implements it). Added ValidatorListReader.ListedValidators (union across PublisherSnapshot validators, trusted = membership in TrustedValidators) implemented on RPCReader.
- Things verified correct (no change): index field injection matches STLedgerEntry::getJson uppercase to_string(key_) (STLedgerEntry.cpp:139); unl_list entry {pubkey_validator, trusted} shape + Admin/NO_CONDITION RBAC (Handler.cpp:180); N2 decode-failure fallback intentionally retained (defensive, owner_info-local, consistent with the account_objects handler).
- Wire-shape verify pass: skipped (justified) — the shape divergences are unambiguous from the literal map construction and corroborated by rippled OwnerInfo_test.cpp; fixes were proven with targeted unit tests (TestOwnerInfoMethod, TestUnlListMethod green) plus full-package runs (internal/rpc, internal/validator/list, internal/ledger/service) rather than driving the live app.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: e013c3ec — removed a name-restating docstring (toRPCAccountObjectItems), trimmed a redundant adapter-conversion clause, dropped a rot-prone "used by" reference. All other PR comments retained (rippled cites + load-bearing why, including the FailHardSubmitter-style capability rationale).
- Notes: Blocking gate hit after Phase 1; user elected to fix all findings (blocking + minors + nits) before Phase 2. Local gates build/vet/lint all green (tests delegated to CI per finalize policy).
- Post-finalize follow-up (commit 7b2e5498): a deeper behaviour re-check surfaced two edge-case divergences the mock-based unit tests did not exercise, both fixed. (1) owner_info validated the account with IsValidXRPLAddress (accepts X-addresses) whereas rippled's parseBase58<AccountID> is classic-only — an X-address slipped past the malformed branch and surfaced as a top-level internal error instead of per-section actMalformed; now gated on the classic-only types.IsValidClassicAddress (regression test added). (2) unl_list's ListedValidators unioned validators from every publisher snapshot including expired/unavailable lists, whereas rippled's keyListings_ counts only currently-applied (available) lists; now gated on Status == StatusAvailable, mirroring recomputeAndEmitLocked. Residual non-PR-specific caveats: embedded error objects carry goXRPL's standard extra `type` field (rippled omits it; the two rippled-tested fields error/error_message match), and per-object JSON fidelity rides on the shared binarycodec.Decode-vs-getJson behaviour already relied on by account_objects.
- Caveat fixes (commit a595b305): both residual caveats addressed. (1) The `type` leak was specific to owner_info embedding the raw *RpcError struct as a value (the top-level error path in server.go:474-487 already hand-builds a map without `type`). Added RpcError.ErrorObject() emitting exactly rippled inject_error's error/error_code/error_message (ErrorCodes.h:228-251) and used it for owner_info's embedded sections; test asserts the `type` key is absent. (2) Added a real-service integration test (TestGetOwnerInfo_WalksOwnerDirectory in offer_query_test.go) exercising the actual owner-directory walk + binarycodec.Decode round-trip for Offer and RippleState with uppercase index, "current" resolution, and the empty-owner-directory case — closing the codec-fidelity caveat for owner_info. Build/vet/lint green; affected-package tests pass.

## 2026-05-26 — PR #555 — feat/issue-496-print
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/555
- Review comment: https://github.com/LeJamon/go-xrpl/pull/555#issuecomment-4543410879
- Files reviewed (Phase 1):
  - internal/rpc/handlers/stubs_admin.go — 1 Minor, 1 Nit, 0 blocking. PrintMethod was a stub returning `{}`; now aggregates ledger/overlay/counters/last_close/state_accounting from wired services. Role parity OK (AdminHandler→RoleAdmin matches rippled Handler.cpp:144 Role::ADMIN, NO_CONDITION). Minor: state_accounting `transitions`/`duration_us`/`current_duration_us` emitted as raw uint64 (JSON numbers) at stubs_admin.go:97-98,102, whereas server_info.go:494-509 and rippled NetworkOPs.cpp:4843-4846 (`std::to_string`) emit them as strings — internal wire-type inconsistency in a debug-only admin tool, no client contract. Nit: rippled doPrint (Print.cpp:33-37) supports a string subtree-selector param; goXRPL ignores `params`. No field-level parity bar exists — rippled's doPrint is a free-form JsonPropertyStream dump of the Application subsystem tree.
  - internal/rpc/missing_methods_test.go — 0 findings (test). Covers ledger section, fully-wired aggregate, Admin role. Note: existing test asserts uint64 for counters, locking in the number rendering; would need updating if state_accounting switches to strings.
- Wire-shape verify pass: ran — booted standalone node on 127.0.0.1:5005, called `print` as admin: returns real `ledger` section (open=3/closed=2/validated=2, correct values); other sections absent in standalone (backing services not wired — matches the "included only when wired" design). server_info on same node confirmed `state_accounting` renders as strings (`"0"`), establishing the Minor finding's contrast. Probes: `print` with no params → no panic, clean success; string subtree-selector → ignored (full output). Could not capture populated `print` state_accounting (consensus inactive in standalone); number rendering confirmed statically + by server_info contrast.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: da2f4a5c — chore: clean ai-generated comments (removed 1 restated-assertion comment in missing_methods_test.go; PrintMethod doc comment kept — load-bearing rippled Print.cpp rationale + design-divergence why). No behavior change.
- Notes: Zero blocking findings → Phase 2 ran automatically. No review-fix commit (Minor + Nit do not gate). Local gates build/vet/lint all green; tests delegated to CI per finalize policy. Branch was 5 commits behind origin/main at finalize (under the 50 threshold; no rebase prompted).

## 2026-05-29 — PR #586 — feat/issue-565-rpc-audit-gaps
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/586
- Review comment: https://github.com/LeJamon/go-xrpl/pull/586#issuecomment-4574791379
- Scope: consensus_info (Engine.GetJSON), can_delete (new internal/ledger/shamapstore advisory-delete store), path_find doc-only correction. Phase 0 → Phase 1 (protocol-bearing: internal/consensus, internal/rpc, internal/ledger).
- Files reviewed (Phase 1):
  - internal/consensus/rcl/engine.go — 3 Blocking + 2 Minor, all fixed in d442b213.
    - B1 close_time emitted as JSON int; rippled to_string → string (ConsensusProposal.h:228-229). Fixed (fmt.Sprintf("%d")).
    - B2 current_ms gated on phase==establish; rippled gates on result_ (Consensus.h:994). Fixed — emit when e.ourTxSet (result_ analog) is set, from a retained currentRoundTime snapshot.
    - B3 converge_percent zeroed outside establish; rippled emits stored convergePercent_ unconditionally in full (Consensus.h:997). Fixed — new lastConvergePercent snapshot captured each phaseEstablish tick (via existing convergePercent() method), reset at round start; live avalanche path unchanged.
    - M1 validating used static IsValidator(); rippled adaptor_.validating() is dynamic (RCLConsensus.cpp:937). Fixed — IsValidator() && OpModeFull.
    - M2 our_position omitted for non-proposing observers; rippled emits result_->position for any node with a result (Consensus.h:989). Fixed — synthesize from e.ourTxSet + CloseTimes.Self in GetJSON (read-only; no consensus-semantics change).
  - internal/rpc/handlers/stubs_admin.go — 1 Minor fixed in d442b213: dropped strings.TrimSpace (rippled applies only boost::to_lower, no trim; CanDelete.cpp:53-54). Nits N1 (project-wide error-code numeric values; tokens already match rippled — intentionally not changed) and N2 (empty/all-numeric-64/>32-bit strings: Go returns clean invalidParams where rippled throws bad_cast — Go is the cleaner of the two; not changed) are deliberate non-fixes.
  - internal/rpc/handlers/consensus_info.go — 0 findings (handler shape {"info": getJson(true)} + empty standalone fallback match ConsensusInfo.cpp).
  - internal/rpc/handlers/path_find.go — 0 findings (doc-only; noEvents over plain JSON-RPC matches PathFind.cpp).
  - internal/ledger/shamapstore/store.go — 0 findings (advisoryDelete/getCanDelete/setCanDelete/getLastRotated + persistence + disabled-gating mirror SHAMapStoreImp.cpp:275-276).
  - internal/rpc/types/services.go, internal/rpc/types/errors.go, internal/consensus/engine.go, internal/cli/server.go — 0 findings (interface/field/wiring additions; notReady=13/notEnabled=12 match rippled).
- Test additions (in fix commit d442b213): GetJSON tests (close_time string, retained current_ms/converge_percent outside establish, observer our_position, dynamic validating) + can_delete tests (whitespace rejection pinning the M3 fix, empty string, mixed-case keyword, lowercase hex hash).
- Wire-shape verify pass: RAN on a live xrpl-confluence mixed network (3 rippled + 2 goXRPL, Kurtosis soak; goxrpl:latest built from this branch, image f679c905). consensus_info on goxrpl-0 sampled across phases and compared field-for-field against rippled-0 on the same network: our_position.close_time AND peer_positions.*.close_time emitted as JSON strings on both (B1 ✓); current_ms max 1000 on both, converge_percent max 20 on both (B2/B3 ✓ — values match the oracle exactly, growing from 0 then frozen as rippled does); validating=true while proposing (M1 ✓); our_position/peer_positions/disputes(object)/acquired/close_times all present and correctly shaped. Network reached validated_seq=94 with both goXRPL and both rippled nodes holding IDENTICAL validated hashes (0BC1FE97…) and matching ledger-14 hashes — no fork, confirming the phaseEstablish snapshot change is consensus-safe. can_delete M3 additionally pinned by unit test.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: 9cdcabcc — removed one restated-next-line comment in the can_delete handler; all other PR comments retained (load-bearing rippled-conformance citations). No behavior change.
- Notes: Blocking gate hit after Phase 1; user elected to fix all findings (3 Blocking + 2 Minor + 1 Minor + nits-as-applicable) before Phase 2. Fixes in d442b213, cleanup in 9cdcabcc, both pushed. Local gates build/vet/lint all green; affected-package tests (consensus/rcl, rpc/handlers, ledger/shamapstore) run locally and green; full suite delegated to CI. Branch 0 commits behind origin/main at finalize.

## 2026-05-29 — PR #587 — fix/issue-569-checkcash-delivered-amount
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/587
- Review comment: https://github.com/LeJamon/go-xrpl/pull/587#issuecomment-4574775560
- Files reviewed (Phase 1):
  - internal/tx/check/check_cash.go — 2 findings, 0 blocking. PR sets `delivered_amount` metadata for CheckCash per fix1623 in two paths. XRP DeliverMin (`applyCashXRPDeliverMin:294-297`): emit gated on fix1623 (DeliverMin implicit by function), value `min(sendMax,srcLiquid)` == rippled `xrpDeliver` on success path — matches CashCheck.cpp:322-324. IOU (`applyCashIOUAmount:579-583`): `checkCashMakesTrustLine || (isDeliverMin && fix1623)` proven equivalent (full truth table) to rippled's two branches CashCheck.cpp:472-474 + :479-480; value = flow `actualOut`. Minor: IOU delivered_amount paths have NO Go test coverage (rippled Check_test.cpp:904,919,934,945,1008,1137 assert verifyDeliveredAmount across IOU cases; Go TestCheck_Fix1623Enable covers only native XRP DeliverMin). Nit (pre-existing, out of diff scope): XRP DeliverMin underfunded source returns tecPATH_PARTIAL (check_cash.go:283-290) where rippled returns tecUNFUNDED_PAYMENT (CashCheck.cpp:307-319) — no delivered_amount impact (errors before any deliver()).
- Wire-shape verify pass: not run — diff touches neither internal/rpc/handlers/ nor internal/peermanagement/, so the verify trigger did not fire. delivered_amount JSON serialization is pre-existing metadata infra untouched by this PR.
- Files cleanup-only (Phase 0 not skipped; this file is non-protocol-bearing): internal/testing/check/check_test.go
- Cleanup commit: c46f3cec — chore: clean ai-generated comments (removed 1 restated-next-line comment in check_test.go that duplicated the adjacent require message; reference-bearing comments in check_cash.go and the WithFix1623 "why 200 XRP" comment kept). No behavior change.
- Notes: Zero blocking findings → Phase 2 ran automatically. No review-fix commit (Minor + Nit do not gate). Local gates build/vet/lint all green; tests delegated to CI per finalize policy. Branch was 0 commits behind origin/main at finalize (fresh; no rebase prompted).
## 2026-05-29 — PR #578 — fix/issue-564-mpt-tefinternal
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/578
- Review comment: https://github.com/LeJamon/go-xrpl/pull/578#issuecomment-4574761370
- Files reviewed (Phase 1):
  - internal/ledger/state/mptoken_entry.go — 0 findings, 0 blocking. SerializeMPTokenIssuance/SerializeMPToken now emit the four sMD_BaseTen UInt64 fields (OutstandingAmount, MaximumAmount, LockedAmount, MPTAmount) as decimal (%d) instead of hex (%X). Fixes #564 tefINTERNAL: binarycodec's encode path (st_object.go:253) parses these field values as base 10, so a hex string with A–F corrupted/errored. Repo sweep confirmed zero remaining %X on these four fields.
  - internal/tx/flatten.go — 0 findings, 0 blocking. Reflective UInt64 path now branches on definitions.IsBaseTenUInt64FieldName → decimal for base-ten fields, uppercase hex otherwise. Matches rippled STParsedJSON.cpp:441-449 (base 10 iff sMD_BaseTen) and STInteger.cpp:246-251 (getJson base 10 for sMD_BaseTen).
  - internal/tx/mpt/mptoken_issuance_create.go — 0 findings, 0 blocking. parseUInt64Field flipped base 16 → base 10 for MaximumAmount (an sMD_BaseTen UInt64), with numeric fallback mirroring rippled's isInt/isUInt branches (STParsedJSON.cpp:456-464).
  - internal/testing/mpt/builder.go — 0 findings (test helper). MPTAmount now embeds the issuance ID via NewMPTAmountWithIssuanceID when known so IsMPT() routes through the 33-byte MPT amount path instead of the lossy IOU path; falls back to NewMPTAmountDirect pre-creation.
- Field-set parity: definitions.IsBaseTenUInt64FieldName covers exactly {MaximumAmount, OutstandingAmount, MPTAmount, LockedAmount} = the only UINT64 SFields flagged sMD_BaseTen in rippled sfields.macro:142-147. One-to-one.
- Test coverage (read, not run — CI executes): codec/binarycodec/codec_test.go:233-253 (serialize) + :419-437 (deserialize) cover all four fields both directions incl. max int64 9223372036854775807 (the regressing value), citing rippled MPToken_test.cpp:189,1654.
- Wire-shape verify pass: N/A — no files under internal/rpc/handlers/ or internal/peermanagement/; MPT amounts surfaced via RPC ride the same coerceUInt64BaseTen decode path verified statically.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: 412014fc — chore: clean ai-generated comments (consolidated the duplicated decimal-string note in MPTokenIssuanceCreate.UnmarshalJSON's doc comment; all other comments kept — load-bearing sMD_BaseTen "why" + rippled-conformance evidence). No behavior change.
- Notes: Zero blocking findings → Phase 2 ran automatically. No review-fix commit needed (review found zero findings at any severity). Local gates build/vet/lint all green; tests delegated to CI per finalize policy. Branch was 0 commits behind origin/main at finalize.
## 2026-05-29 — PR #588 — fix/issue-571-rpc-error-codes
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/588
- Review comment: https://github.com/LeJamon/go-xrpl/pull/588#issuecomment-4574805581
- Files reviewed (Phase 1):
  - internal/rpc/types/errors.go — enum mirror exact (70+ constants vs ErrorCodes.h:42-160); 0 findings on the table itself. Constructor token/code pairs all match ErrorCodes.cpp:52-120.
  - internal/rpc/types/errors_test.go — 0 findings (strong regression guard: full enum + uniqueness + no-collision + 40 constructor pairs).
  - internal/rpc/handlers/vault_info.go — 0 findings (inline Code:21 → RpcErrorEntryNotFound; matches VaultInfo.cpp:101 bare token).
  - internal/rpc/{account_info,ledger_entry,missing_methods}_test.go — 0 findings (renumber assertions).
  - **2 BLOCKING (fixed in this branch)**: B1 tx.go/transaction_entry.go emitted error_code -1 for txnNotFound (rippled Tx.cpp:206 injects 29; transaction_entry is actually a bare "transactionNotFound" per TransactionEntry.cpp:71). B2 ledger.go/ledger_closed.go/ledger_current.go emitted -1 for lgrNotFound (rippled RPCHelpers.cpp:492,511 = 21). These untouched-by-the-PR handlers were within #571's scope.
  - 2 Minor (fixed): M1 bare-token errors wired error_code:-1 + error_message where rippled (inject vs direct jvResult) omits both — added a bareToken marker honored by server.go + websocket.go. M2 account_info mapped ErrLedgerNotFound to internal(73) instead of lgrNotFound(21).
  - 1 Nit (retracted): RpcErrorUnknown token "unknown" is in fact rippled's default ErrorInfo token (ErrorCodes.h:188) — already conformant.
- Wire-shape verify pass: not driven live; the sole wire question (error_code:-1 emission for bare tokens) was answered statically at server.go:564 and fixed. Affected packages (internal/rpc, internal/rpc/handlers, internal/rpc/types) pass locally.
- Fixes landed: commit 989a4491 (build/vet/lint green; targeted + full rpc/handlers/types tests green).
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — Phase 2 was a no-op. Every PR/fix comment is conformance-load-bearing (rippled cites, the "unused"-slot append-only discipline, ErrorCodes.h section labels). No AI cruft to scrub per the keep/protected rules.
- Notes: Blocking gate hit after Phase 1; user elected to fix all findings (blocking + minors + nits). Branch was 0 commits behind origin/main at finalize. Per finalize policy CI runs the full suite; here the affected packages were run locally because the fixes changed behavior.
## 2026-05-29 — PR #582 — fix/issue-568-trustset-codes
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/582
- Review comment: https://github.com/LeJamon/go-xrpl/pull/582#issuecomment-4574776943
- Phase 0: protocol-bearing (internal/tx/trustset/trustset.go) → Phase 1 ran.
- Files reviewed (Phase 1):
  - internal/tx/trustset/trustset.go — 1 Minor, 0 Blocking. Two TER-code corrections, both faithful to rippled SetTrust.cpp. (1) doApply non-existent-line default case now returns tecNO_LINE_REDUNDANT instead of tesSUCCESS — branch-for-branch matches SetTrust.cpp:698-708. (2) fix1578 now rejects tfSetNoRipple on a negative balance with tecNO_PERMISSION (SetTrust.cpp:577-585); perspective math (Balance.Signum vs bHigh) verified equivalent to saHighBalance/saLowBalance >= 0, early-return precedes any owner-count/View.Update so no partial state leaks. M1 (Minor, non-blocking, pre-existing root cause): the eager uQualityIn==QUALITY_ONE→0 normalization at trustset.go:386-388 makes QualityIn=QUALITY_ONE on a fresh zero-limit line short-circuit to tecNO_LINE_REDUNDANT, whereas rippled (raw uQualityIn at SetTrust.cpp:700, only uQualityOut normalized at :413) creates the line and returns tesSUCCESS. Economically pointless input, untested in rippled; flagged with optional fix (drop the eager normalization, own test pass).
  - internal/testing/trustset/result_codes_test.go (new) — 0 findings. TestTrustSet_NoRippleNegativeBalance mirrors rippled NoRipple_test.cpp:78-115 testNegativeBalance (both fix1578 on/off arms). TestTrustSet_NoLineRedundant adds coverage rippled itself lacks (no tecNO_LINE_REDUNDANT test in rippled/src/test/).
- Wire-shape verify pass: not applicable — no internal/rpc/handlers or internal/peermanagement files touched; result-code-only change, no JSON shape impact.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: 406f7b0b — removed 3 restated-next-line comments in result_codes_test.go (bob-trusts setup, no-line-exists precondition, alice-re-asserts action). Kept: both function docstrings with rippled cites, the negative-balance "why", and both rippled-cite comment blocks in trustset.go. No behavior change.
- Notes: Zero blocking findings → Phase 2 ran automatically (M1 is Minor, does not gate). Local gates build/vet/lint all green on both changed packages; tests delegated to CI per finalize policy. Branch 0 commits behind origin/main at finalize (no rebase needed).
## 2026-05-29 — PR #581 — fix/issue-574-decodeseed-validation
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/581
- Review comment: https://github.com/LeJamon/go-xrpl/pull/581#issuecomment-4574761567
- Files reviewed (Phase 1):
  - codec/addresscodec/codec.go — 0 blocking, 0 minor, 1 nit. DecodeSeed now enforces the type prefix (secp256k1 0x21 / ed25519 {0x01,0xE1,0x4B}) and an exact 16-byte body, returning ErrInvalidSeed instead of (a) panicking on short input via decoded[:3] or (b) silently coercing any non-ed payload to a secp256k1 seed. Matches rippled parseBase58<Seed> (Seed.cpp:84-94) → decodeBase58Token type-match + checksum (tokens.cpp:348-369), TokenType::FamilySeed=33=0x21. Wrong-prefix rejection corroborated by Seed_test.cpp:308-347 (testSeedParsing). No panic possible: Base58CheckDecode guarantees result len>=1 (base58check.go:31). ed25519 3-byte prefix is a deliberate, correct superset (ecosystem sEd… convention; rippled's seed parser is single-byte-only and returns no key type). Nit: two rippled testBase58 failure cases (empty string, invalid base58 char) handled correctly but not in the new test table — coverage suggestion, not a parity gap.
  - codec/addresscodec/seed_test.go — 0 findings (test). TestDecodeSeedValidation covers short/long bodies for both prefixes, wrong type prefixes (classic address, node public key), and 1-/2-byte payloads — mirrors rippled testSeedParsing intent.
- Wire-shape verify pass: not applicable (no internal/rpc/handlers or internal/peermanagement files in scope).
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — no AI-comment cruft to remove. The only PR-added comment is the TestDecodeSeedValidation docstring, which cites rippled Seed.cpp:88-93 (protected) and explains a non-obvious why; kept per Keep rules.
- Notes: Zero blocking findings → Phase 2 ran automatically; produced no edits. Local gates build (`go build ./...`)/vet/gofmt all green; golangci-lint could not acquire its lock (parallel job held it). Tests delegated to CI per finalize policy. Branch 0 commits behind origin/main at finalize.
- Correction (2026-05-29): the single nit is retracted. TestSeedDecode (seed_test.go:74-109, pre-existing, not in this PR) already covers all five rippled testBase58 failure vectors (Seed_test.cpp:106-110) — empty string, truncated, one-char-too-long, invalid char O, invalid char /, plus bad checksum. Final tally: 0 blocking, 0 minor, 0 nit; no code/test changes needed. Retraction posted: https://github.com/LeJamon/go-xrpl/pull/581#issuecomment-4575350299
## 2026-05-29 — PR #584 — fix/issue-576-nodestore-hash-header
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/584
- Files reviewed (Phase 1): skipped — no protocol-bearing files. Diff touches only storage/nodestore/{encoding.go,types.go,verify.go}; `storage/` is intentionally outside the Phase 0 protocol-bearing prefix set (codec / crypto / shamap / keylet / amendment + the internal protocol packages), and the nodestore is an opaque content-addressed blob store. The change's own thesis is that storage must NOT recompute protocol hashes, so rippled is not the correctness oracle for this diff.
- Files cleanup-only (Phase 0 skipped Phase 1): storage/nodestore/encoding.go, storage/nodestore/types.go, storage/nodestore/verify.go
- Cleanup commit: none — Phase 2 was a no-op. Every PR-introduced comment is load-bearing: encoding.go documents an intentional divergence from rippled's 9-byte on-disk blob header and cites rippled EncodedBlob.h:99-101 / DecodedBlob.cpp:32-39 (never strip); the types.go and verify.go doc comments record the opaque-key invariant (keys are caller-supplied SHA-512Half over object-type hash prefixes, never recomputed from the payload) — a non-obvious "why" that the cleaning-ai-comments keep-rules protect. No banner/temporal/restatement/name-paraphrase comments were introduced.
- Notes: The substantive change removes an incorrect SHA-256 hash-recompute in Node.IsValid / VerifyAll / VerifyNode (production keys are SHA-512Half, so the recompute mismatched every real node) and replaces it with a non-zero-key structural check. Branch 0 behind origin/main (no rebase needed), 1 commit ahead, clean tree throughout. Local build/vet/lint not re-run: zero edits were made in either phase, so the branch state is byte-identical to what CI already validated.

## 2026-05-29 — PR #583 — fix/issue-570-inbound-endpoints
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/583
- Review comment: https://github.com/LeJamon/go-xrpl/pull/583#issuecomment-4574770550
- Files reviewed (Phase 1):
  - internal/peermanagement/inbound_handlers.go — 2 Minor found, both FIXED in this branch (commit e2cfc016). New handleEndpointsMessage ingests inbound TMEndpoints into Discovery, replacing the dead EventEndpointsReceived path (hard-coded hops=1, never populated). Gating (tracking-converged + version==2, no charge — PeerImp.cpp:1201), oversized-frame reject (>=1024 — :1206), hops==0 socket-IP rewrite (:1234-1235), and malformed-skip-but-continue+charge (:1240-1247) all faithful. Minor 1 (FIXED): "endpoints-too-large" routed through chargeForReason default → FeeInvalidData (400); now an explicit case returns FeeUselessData (150) matching PeerImp.cpp:1208. Minor 2 (FIXED): ParseEndpoint accepted any host string (it stays lax for the outbound Connect hostname path); handleEndpointsMessage now adds a net.ParseIP check so non-IP hosts are charged malformed for every entry (hops>0 and hops==0), matching from_string_checked at PeerImp.cpp:1218-1226.
  - internal/peermanagement/events.go — 0 findings. Removed dead EventEndpointsReceived enum + Event.Endpoints field; no remaining repo references.
  - internal/peermanagement/overlay.go — 0 findings. Dispatches message.TypeEndpoints → handleEndpointsMessage in onMessageReceived; removed onEndpointsReceived. (overlay.go previously audited 0 findings, PRs #548-era.)
  - internal/peermanagement/inbound_handlers_test.go — 0 findings (test). 6 tests one per rippled branch, plus 2 added with the fix: oversized charge asserted strictly lighter than a malformed-entry charge (pins the feeUselessData routing), and TestHandleEndpoints_RejectsNonIPHost (hostname entries at hops>0/hops==0 dropped+charged while the valid sibling still ingests). internal/peermanagement/peer.go also touched (chargeForReason case).
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — Phase 2 was a no-op. All PR-added comments are load-bearing rippled-cite conformance evidence (PeerImp.cpp citations + issue #570 + non-obvious whys); no restated-next-line/banner/temporal cruft to strip.
- Review-fix commit: e2cfc016 — fix(peermanagement): match rippled charge type and IP validation for inbound TMEndpoints (both Minors resolved + 1 new test + 1 strengthened assertion). Local build/vet/lint (0 issues) + full internal/peermanagement package tests all green.
- Notes: Audit-log commit aa69cd98 was committed on the feature branch and collided with main's append-only block (other finalizes landed #582/#581/#584); resolved by merging origin/main and keeping all blocks. Behaviorally clean to merge: zero blocking findings, both Minors fixed. verify skill N/A — change emits nothing to JSON/wire surface (inbound protobuf → in-memory Discovery); unit tests exercise the codepath via onMessageReceived.

## 2026-05-29 — PR #638 — fix/issue-603-decode-strict
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/638
- Review comment: https://github.com/LeJamon/go-xrpl/pull/638#issuecomment-4576906589
- Files reviewed (Phase 1):
  - codec/binarycodec/codec.go — 1 Minor (M1), 0 blocking. DecodeBytes switched to ToJSONStrict + a `p.Remaining()!=0` trailing-byte guard. M1: the guard is unreachable — HasMore() and Remaining() are the same predicate (binary_parser.go:130-139), so the `for p.HasMore()` loop only exits with Remaining==0; trailing bytes are actually rejected by ReadField erroring on the garbage header (test `01101900` errors via ReadField, not the guard). Matches rippled, which also rejects trailing garbage via a field-parse throw, not a post-loop check (STObject.cpp:243-281). Non-blocking: dead-but-harmless; comment misattributes the mechanism.
  - codec/binarycodec/types/st_object.go — 1 Minor (M2), 1 Nit (N1), 0 blocking. ToJSON split into inner toJSON returning a sawEndMarker bool; new ToJSONStrict rejects a top-level end marker (errStrayEndMarker "object terminator", matching STTx.cpp:104-105). M2: the strict rule now applies to ALL decodes, whereas rippled only rejects in STTx (STTx.cpp:104) — the generic STObject(SerialIter&,...) ctor discards reachedEndOfObject (STObject.cpp:85-92). Acceptable/arguably-better: no well-formed top-level blob carries a trailing terminator. N1: a top-level ArrayEndMarker reports "object terminator" vs rippled's distinct "Illegal end-of-array marker in object" (STObject.cpp:259-264); cosmetic internal-string only.
  - codec/binarycodec/types/errors.go — 0 findings. errStrayEndMarker message matches rippled's throw string verbatim.
  - codec/binarycodec/codec_test.go — 0 findings (test). 4 stray-end-marker vectors + 1 trailing-byte vector; mirror STTx.cpp:104-105 / STObject.cpp:243.
- Files cleanup-only (Phase 0 skipped Phase 1): none — all 4 files protocol-bearing (codec/), Phase 1 ran.
- Cleanup commit: none — Phase 2 no-op. Every PR-added comment is a protected rippled/ cite (STTx.cpp:104-105, STObject.cpp:243) or a non-obvious why (toJSON bool contract, hex test-vector decoding); no restated-next-line/banner/temporal cruft to strip.
- Pre-existing divergences noted (out of scope, NOT introduced by #638): duplicate-field detection — rippled set() throws "Duplicate field detected" (STObject.cpp:285-293), goXRPL toJSON silently overwrites map keys; ArrayEndMarker inside a nested object — rippled throws "Illegal end-of-array marker in object" (STObject.cpp:259-263), goXRPL treats it as a normal break.
- Review-fix commit: 35a411b9 — all three findings resolved on user request. M1: dead p.Remaining()!=0 guard removed (field loop already consumes-or-errors, matching STObject.cpp:243). N1 (+ the noted pre-existing nested case): a top-level/nested ArrayEndMarker inside an STObject now errors with rippled's distinct "Illegal end-of-array marker in object" (STObject.cpp:259-263) — added errIllegalArrayEndMarker; only ObjectEndMarker terminates an STObject (STArray consumes its own ArrayEndMarker), so valid input is unaffected. M2: ToJSONStrict doc now states the generalization beyond STTx is intentional. Test `array end marker at top level` updated to assert the new message; codec package tests (incl. rippled STArray/nested-object/roundtrip vectors + fuzz seeds) green; build/vet/lint green.
- Notes: Zero blocking findings → clean to merge. Local build/vet/lint green (0 issues); tests delegated to CI per finalize policy. verify skill N/A — pure codec change, no RPC/peer wire surface.
## 2026-05-29 — PR #646 — fix/issue-591-txq-multitxn-fee
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/646
- Files reviewed (Phase 1): skipped — no protocol-bearing files. The PR's entire diff is a single file, `internal/testing/env_submission.go` (test harness). `internal/testing/` is intentionally outside the Phase 0 protocol-bearing prefix set, so the conformance review does not apply. The change mirrors rippled's m_localTX re-application (NetworkOPs::apply -> openLedger().modify) inside the test env's Submit path, but the production TxQ/engine code it exercises is unchanged on this branch.
- Files cleanup-only (Phase 0 skipped Phase 1): internal/testing/env_submission.go
- Cleanup commit: a2094619 — trimmed a 7-line call-site comment that duplicated the retryHeldReplacementsIntoQueue doc comment verbatim (same rippled citation, same lower-fee-drains-first rationale). The full rationale + rippled cite remain on the function doc; the function doc, the "Apply directly … held set not mutated" comment, and the heldSeqProxy one-liner were kept (non-obvious whys / rippled evidence). Build + vet + lint (0 issues) all green; tests delegated to CI.
- Notes: Branch 30 commits behind origin/main (under the 50-commit rebase threshold; no rebase forced). Clean tree throughout. verify skill N/A — no RPC/wire surface touched.
## 2026-05-29 — PR #636 — fix/issue-601-number-round-even
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/636
- Review comment: https://github.com/LeJamon/go-xrpl/pull/636#issuecomment-4576257677
- Files reviewed (Phase 1):
  - codec/binarycodec/types/number.go — 0 blocking, 0 minor, 1 nit. `normalize` reworked to round discarded low-order digits half-to-even (was truncation) and clamp sub-normal results to canonical zero. Faithful port of rippled `Number::normalize` (Number.cpp:177-227) + `Guard::round` (Number.cpp:136-171): constants (minMantissa 1e15 / maxMantissa 9999999999999999 / min/maxExponent ∓32768) match Number.h:43-48; scale-up (:189-193), scale-down+guard accumulation (:197-204, goXRPL keeps exact big.Int dropped/scale vs rippled's 16 nibble guard digits + sticky xbit — equivalent for to_nearest), underflow-clamp ordering before rounding (:206-210), half-to-even tie (:212-213), rounding carry / exponent bump (:215-220), final exponent overflow (:222-223) all match. goXRPL only uses the default to_nearest mode; directed modes intentionally not ported. Nit: scale-up loop dropped the old `m.Sign()!=0` guard — rippled self-guards zero (Number.cpp:180) but goXRPL relies on its sole caller parseAndNormalize returning canonical zero (number.go:124-126); behaviour still correct even if called with zero (loop terminates, underflow-clamps). Non-blocking.
  - codec/binarycodec/types/number_test.go — 0 findings (test). 11 new cases (tie-odd-up, tie-even-stay, above/below half, carry-overflows-exponent, 3-digit-drop half=500 even/odd, negative tie, 2 underflow clamps, 1 overflow); all hand-traced to rippled-conformant mantissa/exponent. rippled's Number_test.cpp directed-mode coverage (towards_zero/downward/upward) not mirrored — not relevant to the codec's to_nearest-only path.
- Wire-shape verify pass: not applicable (no internal/rpc/handlers or internal/peermanagement files in scope; codec has no JSON/wire response surface).
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: 4e939ec7 — removed a temporal reference ("after the normalize rework") from the TestNumberRoundTrip docstring. All other PR-added comments kept: rippled cites (Number::normalize, Guard) + non-obvious whys (dropped/scale fixed-point accumulation, "half is exact" precision invariant, underflow-clamp intent).
- Notes: Zero blocking findings → Phase 2 ran automatically; produced one comment trim. Local gates build (`go build ./...`) + vet (`go vet ./codec/binarycodec/types/...`) green, gofmt -l clean; golangci-lint could not acquire its lock (parallel job held it). Tests delegated to CI per finalize policy (all 11 test cases hand-traced instead). Branch 0 commits behind origin/main at finalize.
## 2026-05-29 — PR #641 — fix/issue-577-verify-inbound-header-hash
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/641
- Review comment: https://github.com/LeJamon/go-xrpl/pull/641#issuecomment-4576294879
- Files reviewed (Phase 1):
  - internal/ledger/inbound/inbound.go — 1 Nit, 0 blocking. GotBase now recomputes the header hash from the canonical wire bytes (Sha512Half(HashPrefix::ledgerMaster ++ AddRaw)) and rejects a peer whose header hash — or seq, when known — doesn't match the request. Guard expression byte-for-byte equivalent to rippled takeHeader (InboundLedger.cpp:830-831). Hash method is rippled's own ledger-hash invariant (InboundLedger.cpp:845-848 stores that exact blob under hash_). Failure path mirrors rippled's bad-data charge: router.go:2403-2407 logs + IncPeerBadData + removes acquisition + falls back. This is the #577 fix — previously a forged header was blindly accepted (h.Hash = l.hash). Nit: rippled's mSeq==0 seq-adoption branch (InboundLedger.cpp:839-840) is not mirrored, but dead today — all production New() callers pass a non-zero seq (no by-hash-only acquisition path in goXRPL).
  - internal/ledger/inbound/inbound_test.go — 0 findings (test). Adds TestGotBase_RejectsHeaderHashMismatch + TestGotBase_RejectsSeqMismatch, covering both guard disjuncts; rippled has no dedicated takeHeader unit test, so Go coverage exceeds rippled's direct coverage here.
  - internal/ledger/inbound/tracker_test.go — 0 findings (test). New encodeHeader helper derives the true byte-level hash; pre-existing acquisition tests updated to request the real hash (they previously used arbitrary 0xAA-style hashes the new guard would reject — confirms the guard is live).
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — Phase 2 was a no-op. All PR-added comments are load-bearing: the inbound.go recompute-rationale block cites rippled InboundLedger.cpp:830 + replay_delta.go and documents the epoch-0/round-trip why; the test docstrings carry the issue-#577 ref + takeHeader cite; encodeHeader's docstring explains why tests must request the true hash. No banner/temporal/restatement cruft introduced.
- Notes: Zero blocking findings → Phase 2 ran automatically; produced no edits. Local gates build (`go build ./...`)/vet (`./internal/ledger/inbound/...`)/lint (`just lint`, 0 issues) all green; tests delegated to CI per finalize policy. Branch 0 commits behind origin/main at finalize, clean tree throughout. verify skill N/A — change is in internal/ledger/inbound/ (peer ledger acquisition), not internal/rpc/handlers/ or internal/peermanagement/; no JSON/wire surface emitted, the guard is exercised by the two new unit tests.
## 2026-05-29 — PR #630 — fix/issue-598-ws-slot-leak
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/630
- Review comment: https://github.com/LeJamon/go-xrpl/pull/630#issuecomment-4576187998
- Files reviewed (Phase 1):
  - internal/rpc/websocket.go — 0 findings. ServeHTTP now releases the per-port conn slot on the failed-upgrade branch (websocket.go:150-160), the one path where closeConnection (the normal release site, :844-846) never runs because no WebSocketConnection is constructed. Restores rippled's accept/close counter symmetry: rippled scopes the count to the TCP session (onAccept ++count_ at ServerHandler.cpp:179, onClose --count_ at :390), so a thrown websocketUpgrade (:213-221) still hits onClose — goXRPL splits release across middleware/closeConnection and so must cover the upgrade-failure path explicitly. No double-release (success path releases once in closeConnection; new branch only runs on Upgrade error). Release is underflow-guarded (connlimit.go:34).
  - internal/rpc/websocket_test.go — 0 findings (test). TestWebSocketServer_FailedUpgrade_ReleasesSlot sends malformed upgrades (Upgrade: websocket, no Sec-WebSocket-Key → middleware classifies WS + skips its release, gorilla rejects), asserts no 503 leak, Count==0, and a legit client still connects at limit=1. Exercises the exact leak path; fails without the fix. No rippled test counterpart exists — the leak is structurally impossible in rippled (session-scoped count).
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — Phase 2 was a no-op. All PR-added comments are load-bearing: websocket.go:152-154 documents the non-obvious why (the middleware→closeConnection delegation gap); the test docstring cites issue #598 and explains the same gap; the test setup comment explains why the request is malformed in that specific way. No restated-next-line/banner/temporal cruft to strip.
- Notes: Out-of-scope follow-up — ConnLimiter.TryAcquire checks counts>=limit BEFORE incrementing (connlimit.go:23-26) whereas rippled increments then drops on c>=limit post-increment (ServerHandler.cpp:182); potential off-by-one in limit semantics, untouched by this PR. Local gates green: go build ./... clean, go vet ./internal/rpc/... clean, golangci-lint run ./internal/rpc/... 0 issues. Tests delegated to CI. Branch 0 commits behind origin/main at finalize; clean tree throughout. verify skill N/A — change is slot bookkeeping, not JSON/wire shape, and the file is not under internal/rpc/handlers/ or internal/peermanagement/.
## 2026-05-29 — PR #629 — fix/issue-595-closed-ledger-insuff-fee
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/629
- Review comment: https://github.com/LeJamon/go-xrpl/pull/629#issuecomment-4576183458
- Files reviewed (Phase 1):
  - internal/tx/preclaim.go — 0 findings, 0 blocking. The 6-line addition to checkFee's balance-below-fee branch is an exact branch-for-branch port of rippled Transactor::checkFee:304-316: returns TecINSUFF_FEE iff `feePayerBalance > 0 && !OpenLedger` (≡ rippled `balance > beast::zero && !view.open()`), else TerINSUF_FEE_B. Full 62-line checkFee read; fee-payer resolution helper (preclaim.go:264-282) matches rippled :295-302 incl. terNO_ACCOUNT.
  - internal/tx/checkfee_loadfeetrack_test.go — 0 findings (test). New TestCheckFee_InsufficientBalance enumerates all 4 truth-table cases (open × {zero, non-zero balance}), strictly more granular than rippled's single Regression_test.cpp:99-118 case (which confirms closed/non-zero/below-fee → tecINSUFF_FEE, applied=true, balance claimed to XRP(0)).
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — Phase 2 was a no-op. All 3 PR-introduced comments are load-bearing: preclaim.go cites rippled Transactor::checkFee:304-316 (conformance evidence, never strip); the test doc comment cites the same lines + explains the truth table; the inline "Fee of 100 drops" comment is a non-obvious test-setup why. No restated-next-line/banner/temporal/name-paraphrase cruft to strip.
- Notes: Branch 0 behind origin/main (no rebase needed), clean tree throughout. Local build (exit 0) + vet (exit 0) + lint (0 issues) all green; tests delegated to CI per finalize policy. No RPC/wire surface in the diff → verify skill N/A. Zero edits made in either phase, so the branch is byte-identical to what CI validated.
