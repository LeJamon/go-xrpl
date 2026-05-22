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
