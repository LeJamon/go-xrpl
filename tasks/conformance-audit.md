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
- Notes: PR adds marker pagination as a deliberate go-xrpl extension — rippled's `book_offers` accepts the marker parameter (BookOffers.cpp:201-214) but its handler ignores it (NetworkOPs.cpp:4627) and rippled's own Book_test.cpp:1711 documents "a marker field is not returned for this method". Review judged the extension against the closest paginated rippled handler (account_offers) and against rippled's directory-walk invariants. Zero blockers.

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
- Notes: Decided the M1 held-pool divergence in favor of rippled parity (project's "rippled is source of truth" mandate + user "fix all issues") rather than keeping go-xrpl's permanent-failure pre-filter. The pre-filter was a deliberate efficiency optimization with an inaccurate "rippled holds every tx that did not fail permanently" comment — rippled does NOT filter by TER on the local-push path. New behavior: tef/tem/tel local submissions are now held and test-applied each open ledger until they age out (≤5 ledgers), matching rippled exactly; local-only mempool change, no consensus impact. Out-of-scope/pre-existing (not fixed): broadcast relay omits rippled's `(mMode != FULL && !failHard && local)` clause and uses !Applied vs rippled's !isTesSuccess for the fail_hard guard (ledger_adapter.go:254-258, unchanged by this PR); submit response omits account_sequence_next/available, open_ledger_cost, validated_ledger_index (Submit.cpp:168-181).
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
- Post-finalize follow-up (commit 7b2e5498): a deeper behaviour re-check surfaced two edge-case divergences the mock-based unit tests did not exercise, both fixed. (1) owner_info validated the account with IsValidXRPLAddress (accepts X-addresses) whereas rippled's parseBase58<AccountID> is classic-only — an X-address slipped past the malformed branch and surfaced as a top-level internal error instead of per-section actMalformed; now gated on the classic-only types.IsValidClassicAddress (regression test added). (2) unl_list's ListedValidators unioned validators from every publisher snapshot including expired/unavailable lists, whereas rippled's keyListings_ counts only currently-applied (available) lists; now gated on Status == StatusAvailable, mirroring recomputeAndEmitLocked. Residual non-PR-specific caveats: embedded error objects carry go-xrpl's standard extra `type` field (rippled omits it; the two rippled-tested fields error/error_message match), and per-object JSON fidelity rides on the shared binarycodec.Decode-vs-getJson behaviour already relied on by account_objects.
- Caveat fixes (commit a595b305): both residual caveats addressed. (1) The `type` leak was specific to owner_info embedding the raw *RpcError struct as a value (the top-level error path in server.go:474-487 already hand-builds a map without `type`). Added RpcError.ErrorObject() emitting exactly rippled inject_error's error/error_code/error_message (ErrorCodes.h:228-251) and used it for owner_info's embedded sections; test asserts the `type` key is absent. (2) Added a real-service integration test (TestGetOwnerInfo_WalksOwnerDirectory in offer_query_test.go) exercising the actual owner-directory walk + binarycodec.Decode round-trip for Offer and RippleState with uppercase index, "current" resolution, and the empty-owner-directory case — closing the codec-fidelity caveat for owner_info. Build/vet/lint green; affected-package tests pass.

## 2026-05-26 — PR #555 — feat/issue-496-print
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/555
- Review comment: https://github.com/LeJamon/go-xrpl/pull/555#issuecomment-4543410879
- Files reviewed (Phase 1):
  - internal/rpc/handlers/stubs_admin.go — 1 Minor, 1 Nit, 0 blocking. PrintMethod was a stub returning `{}`; now aggregates ledger/overlay/counters/last_close/state_accounting from wired services. Role parity OK (AdminHandler→RoleAdmin matches rippled Handler.cpp:144 Role::ADMIN, NO_CONDITION). Minor: state_accounting `transitions`/`duration_us`/`current_duration_us` emitted as raw uint64 (JSON numbers) at stubs_admin.go:97-98,102, whereas server_info.go:494-509 and rippled NetworkOPs.cpp:4843-4846 (`std::to_string`) emit them as strings — internal wire-type inconsistency in a debug-only admin tool, no client contract. Nit: rippled doPrint (Print.cpp:33-37) supports a string subtree-selector param; go-xrpl ignores `params`. No field-level parity bar exists — rippled's doPrint is a free-form JsonPropertyStream dump of the Application subsystem tree.
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
- Wire-shape verify pass: RAN on a live xrpl-confluence mixed network (3 rippled + 2 go-xrpl, Kurtosis soak; goxrpl:latest built from this branch, image f679c905). consensus_info on goxrpl-0 sampled across phases and compared field-for-field against rippled-0 on the same network: our_position.close_time AND peer_positions.*.close_time emitted as JSON strings on both (B1 ✓); current_ms max 1000 on both, converge_percent max 20 on both (B2/B3 ✓ — values match the oracle exactly, growing from 0 then frozen as rippled does); validating=true while proposing (M1 ✓); our_position/peer_positions/disputes(object)/acquired/close_times all present and correctly shaped. Network reached validated_seq=94 with both go-xrpl and both rippled nodes holding IDENTICAL validated hashes (0BC1FE97…) and matching ledger-14 hashes — no fork, confirming the phaseEstablish snapshot change is consensus-safe. can_delete M3 additionally pinned by unit test.
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

## 2026-05-29 — PR #643 — fix/issue-600-payment-divergences
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/643
- Review comment: https://github.com/LeJamon/go-xrpl/pull/643#issuecomment-4576969058
- Files reviewed (Phase 1):
  - internal/tx/payment/payment.go — 2 findings, 0 blocking. (a) MaxPathSize 7→6 confirmed correct vs rippled Payment.h:30 (old 7 was a real bug); MaxPathLength=8 matches Payment.h:33. (b) New Payment.Preclaim() wired via the Preclaimer interface (transaction.go:59 → preclaim.go:61-65) — not dormant. telBAD_PATH_COUNT gating `OpenLedger && (hasPaths||sendMax||!native)` byte-matches Payment.cpp:349-358. temBAD_CURRENCY check (payment.go:201-203) placed correctly (after positivity, before temREDUNDANT) per Payment.cpp:154/159; native+MPT exclusions correct. Nit: badCurrency() string-compare won't catch a 40-hex currency that encodes to ISO "XRP" (rippled compares the 160-bit Currency at Payment.cpp:154) — pre-existing & systemic, identical to offer/trustset guards (offer_create_validate.go:94-98). Nit: badCurrency() helper duplicated across payment.go:170 and offer_create_validate.go:147.
  - internal/tx/payment/payment_iou.go — 1 Minor, 0 blocking. New-account funding in applyRipplePayment (Amount=XRP + SendMax/paths) faithfully mirrors rippled doApply:407-419 (seqno = deletableAccounts?ledgerSeq:1, create-before-flow, no-flags⇒skip dest-tag/deposit-auth) and the preclaim dest-branch codes telNO_DST_PARTIAL (gated open&&partial, Payment.cpp:308) / tecNO_DST_INSUF_XRP (Amount<accountReserve(0), Payment.cpp:319). Minor (structural, PRE-EXISTING): go-xrpl does destination-existence branching in Apply, whereas rippled does it in preclaim BEFORE the path-count check (Payment.cpp:296-360). Two consequences: (1) TER precedence — {missing dest + oversized paths} yields telBAD_PATH_COUNT (no fee) in go-xrpl vs tecNO_DST (fee) in rippled; (2) tecNO_DST_INSUF_XRP under tapRETRY bypasses the likelyToClaimFee=false no-fee short-circuit (applySteps.h:51 + applySteps.cpp:449-450, mirrored at go-xrpl apply.go:88-94). Not blocking: go-xrpl has always done dest checks in apply (applyIOUPayment:128-130 tecNO_DST), so this PR extends an existing pattern and is a net conformance gain (correct phase+code for path limits). Suggested elegant fix: migrate dest-existence branching into Payment.Preclaim() ahead of the path check, let Apply create unconditionally.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: fabbdead7411915f773309e43dc20159f06bb190 — removed one restated-next-line lead ("Check destination exists." above the Exists call), kept the funding rationale. Only cruft present; every other PR comment is rippled-cited or a load-bearing why. build/vet/gofmt/lint (0 issues) green after edit.
- Notes: Local gate was build/vet/lint only (tests delegated to CI per finalize policy); all green. Branch was 30 commits behind origin/main at finalize (<50 threshold, no rebase prompted). verify skill N/A — diff touches no internal/rpc/handlers or internal/peermanagement wire surface.
## 2026-05-29 — PR #637 — fix/issue-599-shamap-updatehashdeep
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/637
- Review comment: https://github.com/LeJamon/go-xrpl/pull/637#issuecomment-4576882139
- Files reviewed (Phase 1):
  - shamap/inner_node.go — 1 Minor (updateHashUnsafe live-child divergence is the latent root cause, not fully excised — defensible, on record), 0 blocking. New updateHashDeep() is line-for-line faithful to SHAMapInnerNode.cpp:216-229 (iterate non-empty branches, copy hashes[i]=child.Hash() only for live children, then updateHash). Hash-only/backed branches left untouched in both.
  - shamap/shamap.go — 0 findings. flushNode calls updateHashDeep() post-order at :1240 (after child loop + lock release at :1234, before SerializeWithPrefix at :1246), mirroring SHAMap.cpp:1139 (updateHashDeep before writeNode at :1144). No re-entrant lock.
  - shamap/invariants.go — 1 Nit (verifyNodeHash stale-preimage block at :165-181 duplicates InnerNode.Invariants() :445-451; harmless, arguably intentional), 0 blocking. Correctly closes the clone+UpdateHash() blind spot (recomputes from live children, can't see a stale cached preimage). Consistent parent→child lock ordering.
  - shamap/update_hash_deep_test.go — 0 findings (test). Three regression tests set up a preimage invisible to the in-memory hash, then assert the guard resyncs it / flushed bytes hash correctly / invariant detects it. go-xrpl-specific; rippled has no counterpart because the divergence cannot arise in rippled's single-source (hashes_ array) hash model.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — Phase 2 was a no-op. Every PR-introduced comment is load-bearing: the updateHashDeep / flushNode / verifyNodeHash comments each carry a rippled cite (SHAMapInnerNode.cpp:216-229, SHAMap.cpp:1139) AND a non-obvious "why" (the dual-source-hash divergence that makes a stale hashes[i] invisible to the node hash yet corrupt the serialized preimage). Test docstrings explain that same invisibility invariant, not the function name. No banner/temporal/restatement/name-paraphrase cruft introduced.
- Notes: Root cause framing — go-xrpl's updateHashUnsafe (inner_node.go:190-216) reads live children preferentially while rippled's updateHash (SHAMapInnerNode.cpp:201-214) reads the cached hashes_ array unconditionally, so rippled's hash and serialized preimage can never disagree but go-xrpl's can. PR closes the gap by mirroring rippled's walkSubTree updateHashDeep flush ordering rather than re-sourcing updateHashUnsafe — the rippled-faithful choice. Zero blocking findings → Phase 2 ran automatically, produced no edits. Local gates: just build (full module compile) OK, just vet clean, golangci-lint ./shamap/... 0 issues (global lint lock was held by a parallel job; scoped to the changed package). Tests delegated to CI per finalize policy. verify skill N/A — internal flush-path hashing, no JSON/RPC/wire surface to observe. Branch 30 commits behind origin/main at finalize (under the 50-commit rebase threshold).
## 2026-05-29 — PR #642 — fix/issue-604-clawback-pseudo-guard
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/642
- Review comment: https://github.com/LeJamon/go-xrpl/pull/642#issuecomment-4576907126
- Files reviewed (Phase 1):
  - internal/tx/clawback/clawback.go — 0 blocking, 1 minor, 1 nit. Adds clawbackHolderGuard rejecting a pseudo-account (tecPSEUDO_ACCOUNT when featureSingleAssetVault enabled) or AMM holder (tecAMM_ACCOUNT), evaluated after the holder-account existence read (terNO_ACCOUNT) and before the per-issue preclaim checks — branch-for-branch faithful to rippled Clawback.cpp:205-223 (the `if SAV && isPseudoAccount … else if sfAMMID` ladder at :212-216). applyMPT now reads the holder AccountRoot (new terNO_ACCOUNT path; previously absent) + runs the guard before the issuance lookup; applyIOU inserts the guard after its pre-existing holder read and before the issuer-flag checks. IsPseudoAccount (account_root.go:45) faithfully mirrors isPseudoAccount (View.cpp:1139-1150): AMMID-only is correct because VaultID ships with featureSingleAssetVault (SupportedNo) and the pseudo branch is therefore dormant, collapsing live behaviour to rippled's SAV-disabled `else if (sfAMMID)`. Issuer-account existence read (rippled terNO_ACCOUNT on !sleIssuer, Clawback.cpp:207) safely omitted — issuer is the tx signer, engine-guaranteed. Minor (non-gating): MPT path's new holder-read/guard branch is untested (guard is shared code covered by the IOU test; rippled's own suite has no MPT-clawback-from-pseudo case, so this is at the parity bar). Nit (no action): sfAMMID presence approximated by `AMMID != [32]byte{}`, equivalent except the impossible present-but-zero hash; matches the established go-xrpl AccountRoot pattern.
  - internal/testing/amm/amm_clawback_test.go — 0 findings (test). TestClawback_AMMAccountHolder mirrors rippled AMM_test.cpp testAMMClawback (lines 7330-7346), asserting tecAMM_ACCOUNT (SAV off) and tecPSEUDO_ACCOUNT (SAV on) against an AMM pseudo-account set as the clawback holder — the precise rippled parity bar for this fix.
- Wire-shape verify pass: not applicable — no internal/rpc/handlers or internal/peermanagement files touched; preclaim TER-code/result change, no JSON/wire surface.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: none — Phase 2 was a no-op. Every PR-introduced comment is load-bearing: rippled-cite conformance evidence (Clawback.cpp:202-216 at three sites) plus non-obvious whys (preclaim ordering, the SAV-subsumes-AMMID note, the AMMClawback amendment dependency in the test, and the confusing Amount.issuer-subfield-as-holder mechanic). No restated-next-line/banner/temporal cruft introduced.
- Notes: Zero blocking findings → Phase 2 ran automatically and produced no edits. Local gates build/vet/lint all green (lint: 0 issues); tests delegated to CI per finalize policy. Branch 30 commits behind origin/main at finalize (< 50 threshold; no rebase requested), 1 commit ahead.
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
- Pre-existing divergences noted (out of scope, NOT introduced by #638): duplicate-field detection — rippled set() throws "Duplicate field detected" (STObject.cpp:285-293), go-xrpl toJSON silently overwrites map keys; ArrayEndMarker inside a nested object — rippled throws "Illegal end-of-array marker in object" (STObject.cpp:259-263), go-xrpl treats it as a normal break.
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
  - codec/binarycodec/types/number.go — 0 blocking, 0 minor, 1 nit. `normalize` reworked to round discarded low-order digits half-to-even (was truncation) and clamp sub-normal results to canonical zero. Faithful port of rippled `Number::normalize` (Number.cpp:177-227) + `Guard::round` (Number.cpp:136-171): constants (minMantissa 1e15 / maxMantissa 9999999999999999 / min/maxExponent ∓32768) match Number.h:43-48; scale-up (:189-193), scale-down+guard accumulation (:197-204, go-xrpl keeps exact big.Int dropped/scale vs rippled's 16 nibble guard digits + sticky xbit — equivalent for to_nearest), underflow-clamp ordering before rounding (:206-210), half-to-even tie (:212-213), rounding carry / exponent bump (:215-220), final exponent overflow (:222-223) all match. go-xrpl only uses the default to_nearest mode; directed modes intentionally not ported. Nit: scale-up loop dropped the old `m.Sign()!=0` guard — rippled self-guards zero (Number.cpp:180) but go-xrpl relies on its sole caller parseAndNormalize returning canonical zero (number.go:124-126); behaviour still correct even if called with zero (loop terminates, underflow-clamps). Non-blocking.
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
  - internal/ledger/inbound/inbound.go — 1 Nit, 0 blocking. GotBase now recomputes the header hash from the canonical wire bytes (Sha512Half(HashPrefix::ledgerMaster ++ AddRaw)) and rejects a peer whose header hash — or seq, when known — doesn't match the request. Guard expression byte-for-byte equivalent to rippled takeHeader (InboundLedger.cpp:830-831). Hash method is rippled's own ledger-hash invariant (InboundLedger.cpp:845-848 stores that exact blob under hash_). Failure path mirrors rippled's bad-data charge: router.go:2403-2407 logs + IncPeerBadData + removes acquisition + falls back. This is the #577 fix — previously a forged header was blindly accepted (h.Hash = l.hash). Nit: rippled's mSeq==0 seq-adoption branch (InboundLedger.cpp:839-840) is not mirrored, but dead today — all production New() callers pass a non-zero seq (no by-hash-only acquisition path in go-xrpl).
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
  - internal/rpc/websocket.go — 0 findings. ServeHTTP now releases the per-port conn slot on the failed-upgrade branch (websocket.go:150-160), the one path where closeConnection (the normal release site, :844-846) never runs because no WebSocketConnection is constructed. Restores rippled's accept/close counter symmetry: rippled scopes the count to the TCP session (onAccept ++count_ at ServerHandler.cpp:179, onClose --count_ at :390), so a thrown websocketUpgrade (:213-221) still hits onClose — go-xrpl splits release across middleware/closeConnection and so must cover the upgrade-failure path explicitly. No double-release (success path releases once in closeConnection; new branch only runs on Upgrade error). Release is underflow-guarded (connlimit.go:34).
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

## 2026-05-29 — PR #661 — fix/issue-608-jsonrpc-batch
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/661
- Review comment: https://github.com/LeJamon/go-xrpl/pull/661#issuecomment-4577804940
- Files reviewed (Phase 1) — against rippled ServerHandler.cpp:638-994:
  - internal/rpc/server.go — 1 Blocking (B1), 1 Minor (M1). B1: empty `params:[]` returned HTTP 400, but rippled accepts it (size==0 zero-iteration path → 200 with `[]`, :648-653); the 400 guard fires only for missing/null/non-array params (:643-647). M1: malformed batch elements emitted go-xrpl's XRPL-token `result` envelope instead of rippled's per-element JSON-RPC make_json_error shape — non-object → `{request, error:{error:{code:-32601,message}}}` (:658-665); method-less object → element fields echoed at top level with distinct messages "Null method"/"method is not string"/"method is empty" (:764-808; make_json_error :594-603, double-nested by design). The well-formed-unknown-method case already matched rippled.
  - internal/rpc/batch_test.go — TestBatch_MalformedReturns400 asserted empty-array→400 with a comment mis-citing ServerHandler.cpp:642-648 (those lines do not reject an empty array) — a false conformance claim baked into the test. Fixed alongside B1.
- Files cleanup-only (Phase 0 skipped Phase 1): none — Phase 1 ran (internal/rpc/server.go protocol-bearing).
- Findings resolved IN-BRANCH (commit 8a884ec9, at user request — all blocking + minor + nit fixed, not deferred):
  - B1 (Blocking): empty `params:[]` now returns 200 with `[]`; only missing/null/non-array → 400. The unmarshal distinguishes null (elements stays nil) from `[]` (non-nil empty slice). Tests: dropped empty-array→400, added TestBatch_EmptyArrayReturnsEmptyReply (200 `[]`) and a null-params→400 case; comment corrected to cite :643-647 + :648-653.
  - M1 (Minor): added makeBatchJSONError (mirrors make_json_error :594-603, incl. the intentional double-nested error.error) + batchMalformedElement; non-object and the three method-less shapes now match rippled byte-for-byte. New TestBatch_MalformedMethodElements covers Null method / non-string / empty; TestBatch_NonObjectElement rewritten to assert `{request, error.error.{code,message}}`.
  - N1 (Nit): dropped an unnecessary int64() conversion in internal/testing/payment/fund_new_account_test.go:84 (unconvert) — pre-existing, inherited from the 19-commits-stale base; cleared full-module golangci-lint.
- Cleanup commit: a956dd6b — removed two restated-next-line comments in TestBatch_DispatchesEachElement (the "(element itself)" semantic is documented on dispatchBatchElement). All other PR comments kept: most cite rippled ServerHandler.cpp (conformance evidence) or document non-obvious whys (nil-vs-empty-slice json semantics, credential-mask security note, the "do not flatten" double-nesting warning).
- Notes: Branch 19 commits behind origin/main at finalize (< 50 → no rebase). Static gates green before and after each commit: just build, just vet, golangci-lint run (0 issues, full module). Tests delegated to CI per finalize policy. verify skill NOT run — change is in internal/rpc/server.go, not internal/rpc/handlers/ or internal/peermanagement/, so the Step-3.4 trigger did not fire; the novel top-level JSON-array wire shape is exercised against the real httptest response bytes by batch_test.go. This branch's audit log is stale (ends at #629); origin/main has newer #653/#647 blocks, so this entry will append-collide on merge — resolve by keeping all blocks.
## 2026-05-29 — PR #660 — fix/issue-615-retired-vote-obsolete
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/660
- Review comment: https://github.com/LeJamon/go-xrpl/pull/660#issuecomment-4577818737
- Files reviewed (Phase 1):
  - amendment/registry.go — 0 findings, 0 blocking. `registerRetired` now `register(name, SupportedYes, VoteObsolete, true)` — exact match to rippled `retireFeature` = `registerFeature(name, Supported::yes, VoteBehavior::Obsolete)` (Feature.cpp:396-399, comment :393-394 "no code controlled by the feature... need to be supported, but do not need to be voted on"). All three downstream consumers verified parity-correct (and improved): GetDesired() now excludes retired via the VoteObsolete skip (table.go:182-189 ≡ doValidation AmendmentTable.cpp:824 — the primary fix; old VoteDefaultYes re-proposed retired amendments); consensus stance now VoteObsolete (adaptor.go:396-397 & :1312-1313 ≡ constructor AmendmentTable.cpp:572-573, previously fell through to silent abstain); feature RPC now reports vetoed="Obsolete" (feature.go:213-214 ≡ injectJson AmendmentTable.cpp:1012-1013, previously returned false). Genesis enable path unchanged — real ledger uses DefaultYesFeatures() (genesis.go:90-91, registry.go:222-225) which already excluded retired via `&& !f.Retired`; retired code paths run unconditionally (rippled removed the gate). `Supported` unchanged (SupportedYes).
  - amendment/amendment_test.go — 0 findings (test). TestRetiredFeaturesExcludedFromDesired pins the invariant on a fresh NewAmendmentTable() (empty enabled set), proving exclusion by the Obsolete vote alone — not the enabled-set escape — i.e. the actual failure mode the fix addresses. No rippled unit-test counterpart; rippled exercises obsolete via broader voting tests.
- Files cleanup-only (Phase 0 skipped Phase 1): none — Phase 1 ran (amendment/registry.go is protocol-bearing).
- Cleanup commit: none — Phase 2 was a no-op. Both PR-added comment blocks are load-bearing: registry.go:165-169 cites rippled retireFeature (Supported::yes, VoteBehavior::Obsolete) and documents the genesis/Retired-flag cross-file invariant (see rules.go); the test docstring explains the empty-enabled-set rationale. No banner/temporal/restated-next-line cruft introduced.
- Notes: Zero blocking findings → Phase 2 ran automatically; produced no edits. Local gates: go vet (amendment + consensus/adaptor + rpc/handlers + ledger/genesis) clean, `just build` rc=0, `just lint` → 1 hit that is PRE-EXISTING & unrelated (internal/testing/payment/fund_new_account_test.go:84 unconvert) — not in this PR's diff, introduced by #600 (b8d3eed0), already fixed on origin/main by 0588f338 ("drop redundant int64 conversion flagged by unconvert"); clears on rebase/merge. PR's own files are lint-clean. Tests delegated to CI per finalize policy. Branch 19 commits behind origin/main at finalize (under the 50-commit threshold → no rebase required), 1 commit ahead, clean tree throughout. verify skill N/A — diff touches no internal/rpc/handlers/ or internal/peermanagement/ file (feature.go RPC handler is an unchanged downstream consumer, audited by read against AmendmentTable.cpp:1012-1013, not by HTTP probe).
## 2026-05-29 — PR #653 — fix/issue-606-amount-add-currency-guard
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/653
- Review comment: https://github.com/LeJamon/go-xrpl/pull/653#issuecomment-4577510339
- Files reviewed (Phase 1):
  - internal/ledger/state/amount_arithmetic.go — 1 minor, 0 blocking. New currency guard in Amount.Add (`:35`) mirrors rippled areComparable (STAmount.cpp:132-141): rejects two real differing currencies, tolerates issuer mismatch; result tagged with `a` per operator+ v1 rule (STAmount.cpp:395-401). Sub = Add(b.Negate()) inherits the gate. M1 (latent): the new error path is discarded at ~270 `x,_:=a.Add(b)` callsites → a trip yields zero Amount{} instead of rippled's thrown runtime_error (STAmount.cpp:388-390); proven unreachable on current paths (AMM operands cleared to empty-currency Number space; trust-line deltas wrapped by alignToBalance), so non-blocking robustness note only.
  - internal/tx/amm/formulas.go — 0 findings. adjustLPTokens result now tagged via toSTAmountIssue(lptAMMBalance,…) not lpTokens (`:73,78`), matching AMMHelpers.cpp:172-184. lpTokensOut/calcLPTokensIn switched to mulRoundForAsset(…,lptBalance) to re-tag with LP-token issue after toIOUForCalc strips it — matches multiply(lptAMMBalance,frac,rm) tagging with amount.issue() (AMMHelpers.cpp:44-66,111-132,284-290).
  - internal/tx/amm/math.go — 0 findings. toIOUForCalc clears Currency/Issuer (Number space); mulRoundForAsset re-tags from the asset arg. Matches rippled Number/STAmount split.
  - internal/tx/amm/trustline.go — 0 findings. New alignToBalance retags a Number-space delta with the balance's issue before Add/Sub at the 3 trust-line credit sites (`:84,263,415`), mirroring rippleCredit deriving issue from the line not the passed amount.
  - internal/tx/invariants/amm.go — 0 findings. toIOUForInvariant clears tag for the unitless relative-distance check (rippled withinRelativeDistance).
  - internal/tx/payment/amm_swap.go — 0 findings. toNumber clears tag for unitless swap math; issue reapplied via fromNumber.
  - internal/ledger/state/amount_add_test.go — 0 findings (test). Covers each areComparable branch + operator+ tagging: native mismatch, currency mismatch, currency-less Number (both orders + Number+Number), tolerated issuer mismatch (v1-tagged), matched IOU.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: 61f52f72180cc14649b8b8bb3c785e31c96e6203 — trimmed the trivial "amt is a value copy … preserves the IOU value" Go-semantics tails from toIOUForCalc/toIOUForInvariant/toNumber (3 files, -3 lines). Amount is an all-value struct so those tails explained trivial value-copy semantics; the load-bearing Number-space rationale + rippled cites (incl. the verbose Add doc block and the test docstrings) were all kept.
- Notes: Branch 45 commits behind origin/main at finalize (< 50 threshold → no rebase). Local build/vet/lint all green before and after cleanup; tests delegated to CI per finalize policy. verify skill N/A — diff touches internal/ledger + internal/tx only, no internal/rpc/handlers/ or internal/peermanagement/ wire surface. Zero blocking findings → Phase 2 ran automatically.

## 2026-05-29 — PR #647 — fix/issue-590-loadfee-harness
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/647
- Review comment: https://github.com/LeJamon/go-xrpl/pull/647#issuecomment-4576979202
- Files reviewed (Phase 1):
  - internal/tx/engine.go — 0 findings. New `EngineConfig.EnforceLoadFee bool` (additive). Doc comment is a non-obvious why citing rippled Transactor.cpp minimumFee→scaleFeeLoad.
  - internal/tx/preclaim.go — 0 blocking. checkFee load-scaled floor (scaleFeeLoad + telINSUF_FEE_P) mirrors Transactor.cpp:278-290; the EnforceLoadFee gate runs before the fee==0 short-circuit, matching rippled ordering (floor at 278-290 precedes zero-fee tesSUCCESS at 292-293).
  - internal/testing/{env.go,env_submission.go} + internal/testing/conformance/runner.go — 0 findings (test harness). LoadFeeTrack threading + txqLoadFeeLookup transcription of TxQ_test.cpp verified faithful: "clear queue failure (load)" setRemoteFee(5×)+reset (~3995-4009); "Queue full drop penalty" raiseLocalFee ×30 + lowerLocalFee loop (~4673-4685).
- Files cleanup-only (Phase 0 skipped Phase 1): none — Phase 1 ran (engine.go + preclaim.go protocol-bearing).
- Cleanup commit: none — Phase 2 was a no-op (every PR comment is a rippled/TxQ_test.cpp cite or non-obvious why).
- Findings resolved IN-BRANCH (commit 7f368981, at user request — superseding the original "follow-up" recommendation):
  - M1 (was Minor, pre-existing): production TxQ direct-apply/accept path internal/ledger/openledger/txqadapter.go ApplyTransaction did NOT enforce the load-scaled floor rippled applies on its open OpenView (Transactor.cpp:278-290). FIXED — now sets EnforceLoadFee:true (no-op at normal load). apply.go ApplyConfig.FeeTrack doc updated.
  - N1: checkFee's two floor branches deduped into enforceFeeFloor helper.
  - N2: gating kept by design — investigation confirmed OpenLedger:true would wrongly re-reject fee=0 txns (SetRegularKey free password change) and also toggles pseudo-tx gating (pseudo_gates.go:93). Pinned with new TestCheckFee_EnforceLoadFee (5 cases: elevated-below-floor, elevated-meets-floor, normal-load-inert, nil-tracker-inert, closed-apply-never-scales).
- Notes: Static gates green (build/vet/lint 0 issues); focused tests internal/tx + internal/ledger/openledger + internal/txq + internal/ledger/service all pass; heavy suites on CI. Merged origin/main (was 75 behind) to resolve conflicts: checkfee_loadfeetrack_test.go (both sides appended a distinct test fn — kept both) and this audit log (append collision — kept all blocks). verify skill N/A — internal/tx + internal/testing only, no JSON/wire surface.

## 2026-05-29 — PR #662 — test/issue-625-statecompare-version
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/662
- Phase 1: skipped, no protocol-bearing files. Diff is 2 new test files only — internal/statecompare/client_test.go + version/version_test.go (108 insertions, 0 deletions). Neither internal/statecompare/ nor version/ is in the protocol-bearing prefix set, so the rippled-conformance review does not apply; no PR review comment posted.
- Files cleanup-only (Phase 0 skipped Phase 1):
  - internal/statecompare/client_test.go
  - version/version_test.go
- Cleanup commit: dc4ddc67 — removed one restated-next-line comment ("// key is never set in this subtest's environment.") that merely paraphrased the "unset returns default" subtest name. Kept all 3 load-bearing "why" comments: version_test.go's no-ldflags/default-literal rationale (guards a build masquerading as a release), client_test.go's empty==unset + t.Setenv-restores rationale, and the nil-*sql.DB early-return safety note on ValidateRange.
- Notes: Branch 7 commits behind origin/main at finalize (< 50 threshold → no rebase). Worked in existing worktree go-xrpl-worktrees/issue-625 (clean, at origin HEAD b8441b30). Local build (exit 0) + vet (0 diagnostics) + lint (0 issues) all green after cleanup; tests delegated to CI per finalize policy. verify skill N/A — test-only diff, no internal/rpc/handlers/ or internal/peermanagement/ wire surface.
## 2026-05-29 — PR #657 — fix/issue-617-dead-lsfamm-constant
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/657
- Review comment: skipped per author decision (gh pr comment was blocked by the auto-mode permission classifier; author opted not to post). Review captured in finalize chat + this entry.
- Files reviewed (Phase 1):
  - internal/rpc/handlers/account_info.go — 0 findings, 0 blocking. Removed dead lsfAMM=0x02000000: never fed the account_flags map (account_info.go:122-138); zero repo refs remain (only lsfAMMNode substring matches, a distinct real flag). rippled defines no lsfAMM (LedgerFormats.h); bit 0x02000000 = lsfTshCollect (LedgerFormats.h:138) / lsfLowDeepFreeze (167). RPC-side counterpart to PR #509's production removal. account_flags output matches AccountInfo.cpp:88-151, byte-unchanged by this PR.
  - internal/testing/amm/amm_payment_test.go — 0 findings, 0 blocking. Stale comment tightened; TestAMMFlags asserts exact lsfDisableMaster|lsfDefaultRipple|lsfDepositAuth per createPseudoAccount (View.cpp:~1129); comment now accurate.
- Files cleanup-only (Phase 0 skipped Phase 1): none — both protocol-bearing, Phase 1 ran.
- Cleanup commit: none — Phase 2 no-op (only PR-rewritten comment is a load-bearing rippled-citing docstring; nothing to strip).
- Notes: Branch was 19->32 behind origin/main; rebased onto origin/main (now behind=0) to clear a pre-existing, unrelated stale lint failure (internal/testing/payment/fund_new_account_test.go:84 unconvert — already fixed on main). Post-rebase build/vet/lint all green (0 issues). Pre-existing out-of-scope gap noted for a future gap-audit: account_flags omits allowTrustLineLocking (AccountInfo.cpp:113,148).
## 2026-05-29 — PR #656 — fix/issue-616-secp256k1-loop-bound
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/656
- Review comment: https://github.com/LeJamon/go-xrpl/pull/656#issuecomment-4577760332
- Files reviewed (Phase 1):
  - crypto/secp256k1/secp256k1.go — 0 findings, 0 blocking (1 Nit, out of scope). Caps the family-seed scalar-derivation retry loop at 128 (was 0…2^32-1), matching rippled SecretKey.cpp:103 (deriveDeterministicRootKey `seq != 128`) and :162 (Generator::calculateTweak `subseq != 128`). Go loop counter `i` maps to rippled's *retry* counter, not the account index; all three call sites (secp256k1.go:130 root, :139 + :277 tweak) route through the cap. Validity predicate `key>0 && key<order` (secp256k1.go:112) ≡ secp256k1_ec_seckey_verify (SecretKey.cpp:109,:168). Success-path output byte-identical (existing KATs secp256k1_test.go:34-57 still valid). This is a conformance FIX: the prior 2^32 bound could derive a key rippled rejects for a (astronomically improbable) seed failing 128 candidates. Nit: exhaustion uses panic where rippled Throw<runtime_error> (SecretKey.cpp:116,:175) — pre-existing, unreachable (~2^-128), idiomatic Go, out of diff scope.
- Wire-shape verify pass: N/A — no files under internal/rpc/handlers/ or internal/peermanagement/; pure crypto key derivation, no JSON/wire surface.
- Files cleanup-only (Phase 0 skipped Phase 1): none — Phase 1 ran (crypto/ is protocol-bearing).
- Cleanup commit: none — Phase 2 was a no-op. The single PR-rewritten comment (secp256k1.go:117-118, panic-unreachability rationale) is load-bearing panic-safety "why"; pre-existing comments (deriveScalar docstring, :94, :113) are out of scope and not stale. No restated-next-line/banner/temporal/name-paraphrase cruft.
- Nit resolved IN-BRANCH (commit bb70c172, at user request — superseding the original "out of scope / not actionable" call): deriveScalar now returns (*big.Int, error) with a new ErrScalarDerivation sentinel instead of panicking, propagated through DeriveKeypair (both call sites) and DerivePublicKeyFromPublicGenerator. This mirrors rippled's recoverable Throw<runtime_error> (SecretKey.cpp:116,:175) rather than crashing the process. No public-API signature change (both enclosing funcs already returned error); output byte-identical for valid seeds. Re-verified: build/vet/lint green, crypto/secp256k1 + codec/addresscodec package tests pass.
- Notes: Branch was 16 commits behind origin/main, which surfaced a stale `unconvert` lint failure in internal/testing/payment/fund_new_account_test.go:84 — already fixed on main by 0588f338, unrelated to this PR. Per user decision, rebased onto origin/main (conflict-free, disjoint files) and force-pushed c6f6ebf3→1078d9b2; branch now 0 behind. Post-rebase static gates all green: build (exit 0) + vet (exit 0) + lint (0 issues). Tests delegated to CI per finalize policy. Zero code edits in either phase — diff remains the 4-line crypto change CI validates.
## 2026-05-31 — PR #676 — fix/issue-673-discovery-stop-double-close
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/676
- Review comment: https://github.com/LeJamon/go-xrpl/pull/676#issuecomment-4586457897
- Files reviewed (Phase 1):
  - internal/peermanagement/discovery.go — 0 findings, 0 blocking. Guards `Discovery.Stop()` with `sync.Once` so the redundant `close(d.closeCh)` (discovery.go:561) can't double-close → fixes issue #673 `close of closed channel` panic. `wg.Wait()` inside the Once cannot deadlock: maintenanceLoop (discovery.go:748-757) selects on BOTH ctx.Done() and closeCh with `defer d.wg.Done()`, so a ctx-cancelled goroutine has already returned. closeCh is created in NewDiscovery (discovery.go:519), not Start, so the un-started Stop path closes a non-nil channel. Conformance posture: no divergence — rippled OverlayImpl::stop() (OverlayImpl.cpp:561-569) likewise blocks until children quiesce; sync.Once is the Go-idiomatic guard for re-entry + the language-specific double-close panic C++ has no analog for.
  - internal/peermanagement/overlay.go — 0 findings, 0 blocking. Wraps the entire `Overlay.Stop()` body in `sync.Once` with ordering/locking preserved verbatim (cancel ctx → close listener under listenerMu → discovery.Stop() → close peers under peersMu → peerWG.Wait() → resourceManager.Stop()). All nil-guards (cancel/listener/resourceManager) intact; return contract unchanged (always nil). `go vet` copylocks clean → Once not copied (struct used by pointer). Idempotency is independent at both layers, so the real double-stop path (adaptor/startup.go:212 error-path + deferred stop) is panic-free.
  - internal/peermanagement/shutdown_test.go — 0 findings, 0 blocking (new, +42). TestDiscoveryStopIdempotent (Start→Stop→Stop) and TestOverlayStopIdempotent (Stop→Stop on un-started overlay) reproduce issue #673 and pin the fix deterministically.
- Wire-shape verify pass: N/A — diff is under internal/peermanagement/ so the Step-3.4 path trigger fires, but its purpose (JSON field-type drift) does not apply: this change emits NOTHING to any RPC/protobuf/JSON wire surface — purely Stop() lifecycle plumbing. No response shape to observe; double-shutdown behavior pinned by the two new unit tests. Matches prior internal/peermanagement/ finalize precedent (PR #588-era inbound-endpoints entry).
- Files cleanup-only (Phase 0 skipped Phase 1): none — all three protocol-bearing (internal/peermanagement/), Phase 1 ran.
- Cleanup commit: none — Phase 2 no-op. Every PR-introduced comment is load-bearing: the Stop() docstrings on discovery.go/overlay.go document the idempotency/panic-safety invariant (the #673 fix rationale) on exported identifiers; the test comments cite issue #673. The only restated-next-line comments (overlay.go `// Close listener` / `// Stop discovery` / `// Close all peers`) are PRE-EXISTING (base lines 739/747/750, only re-indented when wrapped in stopOnce.Do) and not stale → out of scope. Nothing to strip.
- Notes: Branch 0 commits behind origin/main at finalize (origin/main = merge-base 278e67bd; no rebase needed). Worked in existing worktree .claude/worktrees/fix+issue-673-discovery-stop-double-close, clean tree throughout. Static gates green before and after both phases: go vet ./internal/peermanagement/... (rc=0, incl copylocks), just build (rc=0), just lint (0 issues). Tests delegated to CI per finalize policy. Zero code edits in either phase — diff remains the original +84/-34 (3 files) CI validates.

## 2026-06-06 — PR #788 — fix/issue-787-stream-hash-uppercase
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/788
- Review comment: https://github.com/LeJamon/go-xrpl/pull/788#issuecomment-4639599918
- Phase 0: `internal/cli/` is NOT on the mechanical protocol-bearing prefix list, but the diff is the byte content of WebSocket subscription streams (ledgerClosed / transaction / validations / manifests + the subscribe ledger response), which must match rippled byte-for-byte incl. hex case (goXRPL side of issue #787 / confluence hash-case divergence). Reviewed as protocol-bearing on substance → Phase 1 ran.
- Files reviewed (Phase 1):
  - internal/cli/server.go — 0 findings, 0 blocking. New unexported `upperHex` (= `strings.ToUpper(hex.EncodeToString)`, :1289) replaces plain `hex.EncodeToString` at all 9 stream-emit sites (:812/823/838 ledgerClosed+transaction; :1273 subscribe; :1401/1404/1410/1447/1451 validations; :1562 manifests). Uppercase is the correct rippled encoding for every converted field: `strHex`→`boost::algorithm::hex` is uppercase (rippled strHex.h:39), `to_string(base_uint)` delegates to strHex (base_uint.h:630-633). Per-field parity confirmed vs NetworkOPs.cpp: pubLedger :3124, transJson :3295/:3336, pubValidation :2417/:2418/:2422/:2449/:2432, pubManifest :2249 (manifest blob is `strHex` = hex, NOT base64/base58 — base58 master_key/signing_key siblings :2239/:2242 correctly left alone), subLedger :4179. Completeness: every other stream-surface hash/sig/blob already uppercase (peer_status overlay.go:1532; book_changes book_changes.go:77; manifest sigs via binarycodec Blob blob.go:44; tx/meta JSON via binarycodec.Decode). 3 remaining `hex.EncodeToString` in server.go (:1321/1334/1522) are decode-INPUTS to binarycodec.Decode (case-insensitive) — correctly lowercase.
  - internal/cli/server_helpers_test.go — 0 findings, 0 blocking (+50). TestUpperHex asserts DEADBEEF + nil→""; TestBuildValidationEvent_UppercaseHexFields asserts uppercase for ledger_hash/signature/data/validated_hash/amendments using bytes containing A-F digits.
- Adjacent finding (M1 — Minor, PRE-EXISTING) — FIXED in this branch at user request (commit a1a74823): validation `cookie` field was emitted as base-16 hex (`strconv.FormatUint(v.Cookie, 16)`, server.go:1424) but rippled emits base-10 decimal (`std::to_string(*cookie)`, NetworkOPs.cpp:2429). BASE mismatch, not a case issue; was not in #787's original diff. Fix: `FormatUint(v.Cookie, 10)` + new TestBuildValidationEvent_CookieDecimal. (Adjacent observation, NOT fixed: goXRPL's buildValidationEvent does not emit `server_version`, which rippled emits decimal at NetworkOPs.cpp:2426 — a missing-field gap, separate class; left for a follow-up.)
- Wire-shape verify pass: NOT driven live. Step-3.4 path trigger (internal/rpc/handlers/ or internal/peermanagement/) did not fire — diff is under internal/cli/. The risk verify catches (JSON field-type drift) does not apply: every converted field is already a `string`, the change is a pure case transform, and the new unit tests assert uppercase directly. Prior finalize precedent (this log, PR-era line ~248) recorded that driving the server for a WS wire-probe hit an unrelated startup panic path; not re-attempted.
- Files cleanup-only (Phase 0 skipped Phase 1): none — Phase 1 ran (both files in scope).
- Cleanup commit: none — Phase 2 no-op. All 3 PR-introduced comments are load-bearing rippled-parity rationale + issue refs (the `upperHex` docstring documents the byte-for-byte case-agreement requirement that is the helper's entire reason to exist; the two test comments cite #787 and explain the A-F test-byte choice). Kept per project convention (cites/why protected) and consistent with the prior WS-subscription finalize precedent (this log, "parity story carried by those comments survives merge"). Nothing to strip.
- Notes: Branch 0 commits behind origin/main at finalize (origin/main = merge-base e0515731; no rebase needed), 1 code commit ahead. Worked in existing worktree goXRPL-worktrees/issue-787, clean tree throughout. Static gates green: just build (rc=0), just vet (rc=0), just lint (0 issues). Tests delegated to CI per finalize policy. Phase 1/2 made zero code edits; the audit-entry commit (d3313af8) adds only this log. POST-FINALIZE: user requested the M1 cookie fix → commit a1a74823 (server.go decimal + new test); targeted test run (TestBuildValidationEvent_CookieDecimal PASS) + build/vet/lint re-verified green.

## 2026-06-10 — PR #833 — fix/issue-829-notenabled-gating
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/833
- Review comment: https://github.com/LeJamon/go-xrpl/pull/833#issuecomment-4668232932
- Files reviewed (Phase 1):
  - internal/rpc/handlers/helpers.go — 0 findings (RequireTxTables: gate-before-params + RequireLedgerService subsumption verified)
  - internal/rpc/handlers/tx.go — 1 Minor PRE-EXISTING not fixed (M1: no rpcINVALID_PARAMS when both transaction+ctid supplied, rippled Tx.cpp:295-298 rejects as ambiguous; Go tx.go:38 silently prefers transaction), 0 Blocking. Gate placement matches Tx.cpp:288; lookupByCTID gate removal safe (sole caller after gate)
  - internal/rpc/handlers/account_tx.go — 0 findings (gate precedes apiVersion≥2 binary/forward type checks, matching AccountTx.cpp:406)
  - internal/rpc/handlers/tx_history.go — 0 findings (gate precedes start check, matching TxHistory.cpp:41; v1-only API-version gate precedes handler in both impls)
  - internal/ledger/service/tx_query.go — 1 Nit (N1: UseTxTables takes s.mu.RLock while sibling relationalDB readers are lock-free; field is single-assignment at construction, both safe), 0 Blocking
  - internal/rpc/ledger_adapter.go — 0 findings (TxTablesProvider impl + compile-time assert)
  - internal/rpc/types/services.go — 0 findings (interface addition; notEnabled=12 token/message matches ErrorCodes.cpp:90)
  - internal/rpc/tx_tables_gate_test.go — 0 findings (exceeds rippled's test bar: rippled has no RPC-level useTxTables test, only SHAMapStore can_delete)
- Wire-shape verify pass: DRIVEN LIVE (handlers path trigger fired). Standalone node with unopenable database_path (SQLite open fails → no tx tables): tx/account_tx/tx_history all return {"error":"notEnabled","error_code":12,"error_message":"Not enabled in configuration."} for malformed AND missing params; working SQLite → gate passes, invalidParams/actMalformed as before. Gotcha re-confirmed: first probe ran a STALE ../tmp/main and showed pre-change behaviour — explicit `go build` from the worktree required before trusting probes (see memory verify-binary-staleness-gotcha).
- Gate-site completeness: rippled has exactly 4 RPC useTxTables gates — the 3 ported + Subscribe.cpp:206 (account_history_tx_stream, not implemented in goXRPL → no gap). No gRPC tx-history surface in goXRPL; 2.6.2 Tx.cpp has no gRPC variant.
- Files cleanup-only (Phase 0 skipped Phase 1): none — Phase 1 ran (all files protocol-bearing)
- Cleanup commit: cf791839 (services.go: dropped implementation-inventory sentence from TxTablesProvider doc; ledger_adapter.go: UseTxTables doc reduced to interface marker; +3/-6). Kept: 3 call-site ordering comments (notEnabled-precedes-validation invariant + rippled cites), RequireTxTables/UseTxTables service docs, test parity-pin comments.
- Notes: Branch 0 commits behind origin/main at finalize (no rebase needed). Worked in existing worktree goXRPL-worktrees/issue-829, clean tree throughout. Static gates green before and after both phases: just build, just vet, just lint (0 issues). Tests delegated to CI per finalize policy. Phase 1 made zero code edits (M1 pre-existing, left as PR-comment finding). POST-FINALIZE: user requested M1+N1 fixes → tx.go rejects transaction+ctid with invalidParams "Invalid parameters." (Tx.cpp:295-298 parity) + new TestTxMethodErrorValidation/Ambiguous case; tx_query.go UseTxTables drops the RLock (single-assignment field, matches sibling readers). Targeted tests + internal/ledger/service suite + build/vet/lint re-verified green.

## 2026-06-10 — PR #870 — fix/issue-850-lookupledger-shape
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/870
- Review comment: https://github.com/LeJamon/go-xrpl/pull/870#issuecomment-4674388722
- Scope: central `fillLedgerFields` helper mirroring rippled `RPC::lookupLedger` (open → `ledger_current_index` only; closed → `ledger_hash`+`ledger_index`; `validated` always) swept across 9 handlers + open-vs-closed determination fix (`resolveLedgerSelector`: ledger_hash → closed, default → current) + validated→current default correction for deposit_authorized/noripple_check. Phase 0 → Phase 1 (protocol-bearing: internal/rpc/handlers/).
- Files reviewed (Phase 1):
  - internal/rpc/handlers/helpers.go — 0 blocking. `fillLedgerFields` matches RPCHelpers.cpp:632-642 exactly (open `!ledger->open()` else branch; `validated` unconditional = isValidated(*ledger)). `resolveLedgerSelector`/`isOpenLedgerSelector` selector→open-or-closed branch is consistent with the service's open-only-for-"current"/"" resolution (account_query.go:55-77); rippled's numeric-index-==-current-seq open edge (RPCHelpers.cpp:503-507) has no Go analog (history is closed-only). `ledger_index`/`ledger_current_index` = JSON number (info.seq), no api_version stringification in lookupLedger → composable with PR #865's api_version default flip (orthogonal).
  - internal/rpc/handlers/{account_info,account_channels,account_lines,account_objects,account_offers,book_offers,gateway_balances}.go — 0 findings (mechanical swap to fillLedgerFields; shapes verified).
  - internal/rpc/handlers/deposit_authorized.go, noripple_check.go — 0 findings. validated→current default correction verified: both rippled handlers call RPC::lookupLedger with no override (DepositAuthorized.cpp:74, NoRippleCheck.cpp:102) → defaults to "current" (RPCHelpers.cpp:614-615,389).
  - internal/rpc/{account_info,book_offers,deposit_authorized,account_offers,account_channels,account_lines,account_objects,gateway_balances}_test.go — 0 findings; new shape tests assert exact field presence/absence + number types, exceed pre-PR coverage.
  - M1 (Minor, PRE-EXISTING, not fixed, does not gate): ledger_hash not threaded to the service for these 9 handlers (signature takes string selector only) → a ledger_hash query resolves to "validated" (latest validated), not the specific historical ledger; and resolveLedgerSelector lets ledger_index win over ledger_hash whereas rippled ledgerFromRequest:376-385 lets hash win. Pre-existing (account_info had this inline on main); PR fixes shape only. Follow-up if these services gain by-hash lookup.
- Wire-shape verify pass: NOT driven live. book_offers handler is under the Step-3.4 trigger prefix, but the risk verify catches (JSON field-type drift) does not apply — every emitted field is a plain JSON string/number and the new unit tests assert exact field presence/absence + number types. Prior finalize precedent (this log, PR #788/#833) records driving the server hits an unrelated startup panic path. Static read + unit tests establish wire shape.
- Files cleanup-only (Phase 0 skipped Phase 1): none — Phase 1 ran (all protocol-bearing).
- Cleanup commit: none — Phase 2 no-op. Every PR-introduced comment is load-bearing rippled-lookupLedger parity rationale: the fillLedgerFields/resolveLedgerSelector/isOpenLedgerSelector docstrings document the open-vs-closed fill semantics + selector enumeration (the whole reason the helpers exist); the handler comments document the validated→current default correction; the test comments are the lookupLedger response-shape contract. The one borderline-narration comment (`// Verify ledger_current_index`, account_offers_test.go:498) is a PRE-EXISTING `// Verify X` block comment the PR merely kept accurate (index→current_index) — not stale, stripping it in isolation would create sibling inconsistency + require forbidden drive-by edits. Nothing to strip.
- Notes: Branch 3 commits behind origin/main at finalize (well under 50, no rebase needed), 1 code commit ahead (ba8dd8ef). Worked in existing worktree go-xrpl-worktrees/issue-850, clean tree throughout. Static gates green: just build (rc=0), just vet (rc=0), just lint (0 issues). Tests delegated to CI per finalize policy. Both phases made zero code edits — diff remains the original +258/-75 (18 files) CI validates.
## 2026-06-10 — PR #893 — fix/issue-857-invariant-predicates
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/893
- Review comment: https://github.com/LeJamon/go-xrpl/pull/893#issuecomment-4674538435
- Files reviewed (Phase 1):
  - internal/tx/invariants/offers.go — 0 blocking. NoBadOffers clause-for-clause vs InvariantCheck.cpp:229-245 (zero passes / negative either leg / both-native), XRP sign via bit 62 cPositive, IOU sign via reused state.ParseIOUAmountBinary + Signum(); NoZeroEscrow umbrella Name correct for the Escrow+MPTIssuance+MPToken class (InvariantCheck.cpp:267-339).
  - internal/tx/invariants/binary_helpers.go — 0 blocking, 1 Minor (M1: cross-branch). Full VL-length AccountID decode (2-byte extended form). See M1.
  - internal/tx/invariants/frozen.go — 0 findings. Same-issuer skip removal proof re-verified (TrustSet preflight trustset.go:109 + preclaim :204 temDST_IS_SRC; all RippleState producers use CompareAccountIDsForLine over distinct accounts). enforce hoisted for the Phase-1 parse hard-fails.
  - internal/tx/invariants/trustlines.go — 0 findings (parse hard-fail, correct Names).
  - internal/tx/invariants/clawback.go — 0 findings (parse hard-fail, ValidClawback).
  - internal/tx/invariants/amm.go — 0 findings. 1e-11 tolerance kept w/ documented reconstruction-vs-stored drift justification (matches prior #509 audit); exact-eq rejected per issue.
  - internal/tx/invariants/nftoken.go — 0 findings (parse hard-fail via helpers).
  - internal/tx/invariants/permissioned.go — 0 findings (parse hard-fail; non-overlapping hunks vs #885).
  - internal/tx/invariants/predicate_residuals_test.go — 0 findings (rippled-cited, exercises zero/neg-XRP/neg-IOU/XRP-XRP/parse-fail/same-issuer/ULP/hard-fail family).
- M1 (Minor, cross-branch merge-order, NOT a code defect): PR #885 (fix/issue-842-sle-field-sweep, OPEN, SHA 85fd3601) ALSO fixes skipFieldBytes case 8 AccountID off the same main base, but single-byte-only (no >192 extended form). Behaviorally equivalent FOR AccountID (always 20 bytes → prefix 0x14 ≤ 192), but the two edits touch the SAME 2 lines → guaranteed textual conflict on whichever merges second; resolve by keeping #893's fuller version (strict superset). #885's skipOuterField wrapper then dispatches into #893's complete handling (better, no regression). permissioned.go touched by both but in non-overlapping hunks → auto-merges.
- Files cleanup-only (Phase 0 skipped Phase 1): none — Phase 1 ran (all 9 files protocol-bearing under internal/tx/)
- Pre-Phase-1 commit: d4c383d2 (the PR's implementation commit; no review fixes needed — zero blockers)
- Cleanup commit: eb54268a — trimmed PR-introduced restatement docs (parseOfferForInvariant doc removed; offerForInvariant + nftCountParseViolation docs trimmed to the why; nftPageParseViolation doc removed). Kept: rippled cites, isBad predicate note, bit-level XRP/IOU wire-format comment, AMM tolerance justification, all test-intent comments. +5/-11.
- Notes: Branch 0 commits behind origin/main at finalize (no rebase). Static gates green before and after both phases: just build, just vet, just lint (0 issues). Tests delegated to CI per finalize policy. No wire-format/RPC files touched → no verify pass. Pre-existing section-divider banners left untouched (out of PR scope + consistent package convention).
## 2026-06-10 — PR #885 — fix/issue-842-sle-field-sweep
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/885
- Review comment: https://github.com/LeJamon/go-xrpl/pull/885#issuecomment-4674501146
- Phase 0: all changed files protocol-bearing (internal/ledger/state/, internal/tx/check/, internal/tx/invariants/) → Phase 1 ran.
- Files reviewed (Phase 1) — 0 blocking, APPROVE:
  - internal/ledger/state/offer_entry.go — 0 blocking, 1 Nit (N1). Hybrid offer AdditionalBooks serialized as a single-entry STArray of one sfBook inner object (sfBookDirectory UINT256 nth=16 + sfBookNode UINT64 nth=3), matching CreateOffer::applyHybrid (CreateOffer.cpp:562-571); inner-object shape stores exactly those two fields, no extra. sfBook OBJECT nth=36 / sfAdditionalBooks ARRAY nth=13 verified vs sfields.macro:121/179/364/380; soeOPTIONAL on ltOFFER (ledger_entries.macro:254). New STArray parse arm round-trips byte-for-byte; default→`return offer,nil` is a strict safety improvement over the old switch-`break`. N1: skipArray byte-scans for 0xF1 (false-positive risk on inner payloads) but is unreachable — AdditionalBooks is the only array field on ltOFFER.
  - internal/ledger/state/directory.go — 0 findings. sfIndexes always serialized even when empty (wire 0113 + VL 00); sfIndexes is soeREQUIRED on ltDIR_NODE (ledger_entries.macro:184) and dirRemove keepRoot writes back the emptied Vector256 (ApplyView.cpp:230-234,282-283). Prior omit-when-empty dropped a required field on keepRoot deletions → account_hash fork. Non-empty pages byte-identical; codec already encodes empty Vector256, no codec change.
  - internal/ledger/state/account_root.go — 0 findings. Adds FieldTypeObject(14)/FieldTypeArray(15) + objectEndMarker(0xE1)/arrayEndMarker(0xF1) consts consumed by the offer STArray parser.
  - internal/tx/check/helpers.go — 0 findings. serializeCheck copies tx SourceTag onto the Check SLE iff present — byte-for-byte CreateCheck.cpp:199-200. Canonical position correct (sfSourceTag UINT32 nth=3 → header 0x23); survives the ParseCheck→SerializeCheckFromData round-trip (HasSourceTag preserved).
  - internal/tx/invariants/binary_helpers.go — 0 findings. AccountID field-walker fix: was `return 20` (off-by-one, ignored the VL length prefix); now `1 + data[offset]` — AccountID inside an SLE body is VL-prefixed 0x14 (account_id.go:19-22), length-generic incl. zero-length default STAccount. Bounds-checked.
  - internal/tx/invariants/permissioned.go — 0 findings, 1 Nit (N2). badHybrids tightened to `DomainID==zero || abCount<1 || abCount>1`, matching rippled InvariantCheck.cpp:1658-1662 now that AdditionalBooks is on the wire (-1=absent → bad). New skipOuterField handles Amount width (8B XRP / 48B IOU by high bit) which generic skipFieldBytes returns (0,false) for — lets the walker traverse TakerPays/TakerGets/Account to reach the array. Field-type completeness verified for every type preceding sfAdditionalBooks in an Offer's canonical order. N2: present-but-zero DomainID / present-but-empty array would diverge from rippled's isFieldPresent/size>1, but neither is rippled-reachable (DomainID is a non-zero domain-keylet hash set only when present; applyHybrid always pushes exactly one entry) — equivalent for all reachable states.
  - Tests (issue842_test.go, source_tag_test.go, permissioned_hybrid_test.go) — byte-level pins for all three field gaps + the badHybrids matrix; not re-flagged.
- Wire-shape verify pass: N/A — no files under internal/rpc/handlers/ or internal/peermanagement/; pure SLE serialization, no JSON/protobuf wire surface. Byte-level assertions live in the new unit tests.
- Files cleanup-only (Phase 0 skipped Phase 1): none — Phase 1 ran (all files protocol-bearing).
- Cleanup commit: none — Phase 2 no-op. Every PR-introduced comment is load-bearing: SLE wire-format invariants (the sfIndexes-soeREQUIRED / keepRoot-fork rationale on directory.go, the AccountID VL-encoding note on binary_helpers.go), the badHybrids parity comment + its rippled cite, the skipOuterField "Amount-not-handled-by-generic" why, the hand-parser offset contracts (parseAdditionalBooks/parseInnerBook/skipArray), and the `// FieldName (nth=N)` field-code annotations that are the file's established convention (pre-existing lines 149-177). No restated-next-line cruft, banner, temporal "#842" trail, or name-only docstring to strip.
- Notes: Branch 0 commits behind origin/main at finalize (no rebase needed). Worked in existing worktree goXRPL-worktrees/issue-842, clean tree throughout. Static gates green: just build (rc=0), just vet (rc=0), just lint (0 issues). Tests delegated to CI per finalize policy. Phase 1 and Phase 2 made zero code edits; only this audit-log block was added (committed on-branch per accepted pattern). PR body reports branch-vs-clean-main conformance identical (1274 pass / 241 fail, zero regressions).
## 2026-06-10 — PR #838 — feat/issue-832-rpcsub-url-subscriptions
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/838
- Review comment: https://github.com/LeJamon/go-xrpl/pull/838#issuecomment-4672003904
- Files reviewed (Phase 1):
  - internal/rpc/rpcsub.go — 2 findings (M1 empty-host url rejected where rippled's parseUrl regex accepts it — Go stricter, admin-only surface, kept; N1 seq stamped at delivery not enqueue, so bounded-queue drops are gapless/undetectable), 0 blocking
  - internal/rpc/handlers/subscribe.go — 0 findings (gating order/codes match doSubscribe url branch; live-verified)
  - internal/rpc/handlers/unsubscribe.go — 0 findings (silent-success unknown url, Unsubscribe.cpp:51-53; live-verified)
  - internal/rpc/subscription/manager.go — 0 findings (HasStreamSubscriptions mirrors tryRemoveRpcSub's stream-maps-only scan, NetworkOPs.cpp:4404-4422; url state fully removed, no stale readers)
  - internal/rpc/types/types.go — 0 findings (HasURL member-presence = isMember semantics; URLCredentials deprecated-member precedence incl. clear-to-empty, Subscribe.cpp:56-69/:97-107)
  - internal/rpc/types/services.go — 0 findings (URLSubscriptionService interface + container wiring)
  - internal/rpc/websocket.go — 1 finding (M2 pre-existing, surfaced: buildSubscribeAck gates network_id on >0 and emits fee_ref unconditionally; rippled subLedger NetworkOPs.cpp:4182-4188 is the inverse — verbatim extraction, not a regression; follow-up candidate), 0 blocking
  - internal/rpc/rpcsub_test.go, internal/rpc/subscribe_test.go — 0 findings (test parity ⊇ Subscribe_test.cpp:561-644 url cases + delivery/credentials/removal coverage rippled lacks)
- Wire-shape verify pass: DRIVEN LIVE. Standalone node + capturing HTTP sink: subscribe ack (integer ledger fields), event delivery {"method":"event","params":{...,"seq":1},"id":1} with Basic auth (alice:secret → YWxpY2U6c2VjcmV0), seq 1→2 increment, error strings verbatim ("Only http and https is supported.", "Invalid parameters."), unsubscribe stops delivery, unknown-url unsubscribe silent success. Binary freshness strings-checked ("rpcsub:" literal) before probing.
- Files cleanup-only (Phase 0 skipped Phase 1): none — Phase 1 ran (all 9 files protocol-bearing)
- Cleanup commit: bfe1ddfb (subscribe_test.go: one temporal "no longer carries url state" cross-reference trimmed to a durable pointer; +2/-3). Everything else kept — PR comments are rippled-cited conformance rationale (queue-bound divergence note, member-presence semantics, credential reuse rules, registry placement).
- Notes: Branch 3 commits behind origin/main at finalize (no rebase needed). Existing worktree goXRPL-worktrees/issue-832, clean tree throughout. Static gates green before and after both phases: just build, just vet, just lint (0 issues). Tests delegated to CI per finalize policy. Phase 1 made zero code edits (M1 intentional improvement; M2 pre-existing extraction).
## 2026-06-12 — PR #907 — refactor/issue-867-shamap-dedup
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/907
- Review comment: https://github.com/LeJamon/go-xrpl/pull/907#issuecomment-4690987275
- Phase 0: protocol-bearing files present (shamap/, internal/ledger/, internal/consensus/) → Phase 1 ran. Prior shamap coverage consulted (PR #637 updateHashDeep entry) — used as context; full re-review still performed since this PR rewrites the whole package (+2009/−4190, 80 files).
- Files reviewed (Phase 1) — 4 parallel slices, 0 blocking, 4 Minor, 5 Nit:
  - shamap/{sync,compare,completeness,fetchpack,traverse}.go — 2 Minor (M1 useful/duplicate collapse vs SHAMapAddNode, benign; M2 diffInner descends equal-hash branches, pre-existing perf), 3 Nit. AddKnownNodeFromPrefix attach-by-NodeID verified peer-poison-resistant (hash re-checked vs live parent, SHAMapSync.cpp:610-627); both claimed lazy-load bugs verified real on main and fixed.
  - shamap/{node,inner_node,leaf,item,node_id,keypath,wire,flush,store,proof,invariants,nodestore_family}.go — 0 findings above informational. Wire/hash byte formats, compressed-inner encoding, hash prefixes, flush ordering (PR #637 guard preserved), snapshot race-fix lock story all conform.
  - shamap/{shamap,iterator,traverse}.go + README — 2 Minor (M3 walkBoundStack continuation fix correct vs SHAMap.cpp:595 but untested; M4 stale ForEach early-stop test comment), 1 Nit (boundBelow swallows missing-node errors, pre-existing). boundBelow merge preserves firstBelow/lastBelow exactly.
  - Consumers (internal/cli, internal/consensus/adaptor, internal/ledger/{genesis,inbound,openledger,service}, internal/replaytool, tests) — 0 findings. All dropped New() errors provably always-nil; no dangling symbols; consensus disputes (rcl/) untouched and never used the deleted channel API.
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: ff2a42fa — rewrote the stale TestSme_ForEachEarlyStop comment (described pre-refactor swallowed-stop behavior); all 275 PR-added comment lines classified keep-class (locking contracts, invariants, rippled cites); apparent restatements were pre-existing moved/re-cased lines, out of scope.
- Notes: Branch 19 commits behind origin/main at finalize (under threshold, no rebase). Static gates green before and after both phases: just build, just vet, just lint (0 issues). Tests delegated to CI per finalize policy. No files under internal/rpc/handlers/ or internal/peermanagement/ → no wire-shape verify pass.
- Follow-up fixes (891bcfdb, user-requested post-finalize): M1 (AddKnownNode* return added-vs-duplicate, SHAMapAddNode parity), M2 (diffInner hash-first skip, SHAMapDelta.cpp:207-209), M3 (bound-iterator Next() rewalks from current key per SHAMap.cpp:589-596 — writing the drain test exposed a REAL bug: the saved-stack continuation skipped the rest of the subtree boundBelow descended into; TestUpperBoundDrain fails on the pre-fix code), M4 (count==1), N1 (direct AddKnownNodeFromPrefix test incl. poison case), N4 (boundBelow error propagation). N2/N3/N5 no-action by design (valid-subset enumeration order, per-branch emit, coverage gain). Race-enabled tests green for shamap/inbound/adaptor; all 12 ledger/replaytool consumer packages green.
## 2026-06-10 — PR #884 — fix/issue-851-rpc-fidelity
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/884
- Review comment: https://github.com/LeJamon/go-xrpl/pull/884#issuecomment-4674471368
- Files reviewed (Phase 1):
  - internal/rpc/handlers/account_info.go — 0 findings, 0 blocking. queue_data field-for-field vs AccountInfo.cpp:193-283: empty/unwired → {txn_count:0} (else branch :280-281); non-empty txn_count+transactions[]+always auth_change_queued(bool)+max_spend_drops_total(string) with conditional sequence_count/ticket_count/lowest_/highest_ sequence+ticket (:264-278); per-tx seq XOR ticket(number), fee_level/fee/max_spend_drops(strings via FormatUint), conditional LastLedgerSequence, auth_change(bool). String-vs-number types match.
  - internal/rpc/handlers/ledger.go — 0 findings, 0 blocking. (a) queue_data top-level ARRAY, sibling of ledger, only-when-nonempty (addJson:352-353 / fillJsonQueue:286-317); per-entry fee_level/LastLedgerSequence?/fee/max_spend_drops/auth_change/account/retries_remaining/preflight_result/last_result?; v1 nests under tx, v2 flattens (:312-315). (b) owner_funds OfferCreate-only + expanded-non-binary-only + self-funded(issuer==account) skip + fhIGNORE_FREEZE(AccountFunds fhZeroIfFrozen=false) + getText() string, key owner_funds (LedgerToJson.cpp:206-224). (c) unlimited gate full||accounts → !ctx.Unlimited → rpcNO_PERMISSION (LedgerHandler.cpp:66-72); ctx.Unlimited=Admin||Identified (Role.cpp:124-128); token "noPermission" code 6 (ErrorCodes.cpp:100). (d) accountState array binary→{hash,tx_blob}/expanded→SLE-json/else→key (fillJsonState:260-282).
  - internal/txq/txq.go — 0 findings, 0 blocking. GetAccountTxs/GetAllTxs read-only accessors over Candidate state; candidateDetails maps AuthChange=Consequences.IsBlocker (≡ isBlocker()). HasLastResult=RetriesRemaining<RetriesAllowed(10) is a faithful proxy for rippled std::optional<TER> lastResult: --retriesRemaining and lastResult=result are lockstep (TxQ.cpp:570-571,1515-1516); goXRPL sets LastResult before the decrement (accept.go:80 vs :96-99), retry-penalty branch leaves 1<10 still set.
  - internal/rpc/types/services.go — 0 findings, 0 blocking. QueueAccountTxs/QueueAllTxs nil-safe hooks + QueuedTxInfo view mirroring TxQ::TxDetails (AccountInfo.cpp:218-261, LedgerToJson.cpp:292-316).
  - internal/ledger/service/service.go — 0 findings, 0 blocking. GetQueueAccountTxs/GetQueueAllTxs nil-guarded RLock accessors over s.txQueue.
  - internal/cli/server.go — 0 findings, 0 blocking. queuedTxInfos projection: MaxSpendDrops = PotentialSpend + Fee (≡ consequences.potentialSpend()+fee(), AccountInfo.cpp:252-253); SeqProxy.Value/IsTicket mapping correct.
- Wire-shape verify pass: Step-3.4 path trigger fired (internal/rpc/handlers/) but NOT driven live. Every queue_data/owner_funds field type is asserted directly by the 4 new test files (queue_data_test.go, ledger_owner_funds_test.go, ledger_test.go, account_info_test.go) with string-vs-number distinctions pinned; the change emits only FormatUint strings + JSON numbers/bools — no field-type drift surface beyond what the unit tests already cover. CI runs the suite.
- Files cleanup-only (Phase 0 skipped Phase 1): none — all 6 files reviewed (internal/rpc/, internal/ledger/, internal/txq/ protocol-bearing), Phase 1 ran.
- Cleanup commit: none — Phase 2 no-op. Every PR-introduced comment is load-bearing: rippled cites (AccountInfo.cpp/LedgerToJson.cpp/LedgerHandler.cpp/Role.cpp/ErrorCodes.cpp) on the queue/owner_funds/gate docstrings + service-hook nil-safety docs, OR a non-obvious why (queuedTxInfos body-flattened-only-for-ledger, parseLedgerAmount codec-reuse parity, HasLastResult optional-gate invariant, self-funded-skip). No restated-next-line/banner/temporal/name-paraphrase cruft to strip. Consistent with prior rippled-citing RPC finalize precedent (PR #788/#676).
- Notes: Branch 3 commits behind origin/main at finalize (well under 50, no rebase). Worked in existing worktree goXRPL-worktrees/issue-851, clean tree throughout. Static gates green: just build (rc=0), just vet (rc=0), just lint (0 issues). Tests delegated to CI per finalize policy. Phase 1 and Phase 2 made zero code edits — the diff remains the original +917/-44 (10 files) CI validates; this audit-log commit is the only branch addition.
