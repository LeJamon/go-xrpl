# Conformance audit log

Append-only record of `finalizing-goxrpl-branch` runs. Each block records what
was reviewed against which `rippled/` SHA so subsequent finalizations can do
incremental reviews instead of re-reading rippled from scratch.

## 2026-05-22 — PR #486 — fix/issue-482-amm-pool-balances (review + same-day fix)
- Rippled SHA at review: 1e89286a92
- PR URL: https://github.com/LeJamon/go-xrpl/pull/486
- Review comment: https://github.com/LeJamon/go-xrpl/pull/486#issuecomment-4518346498
- Files reviewed (Phase 1):
  - internal/rpc/handlers/amm_info.go — 1 Blocking, 5 Minor, 1 Nit
  - internal/rpc/handlers/amm_info_test.go — covered as test-coverage notes inside the amm_info findings
  - internal/rpc/ledger_adapter.go — 0 direct findings; Minor #4 flags `validated` derivation as needing verification of Service.getLedgerForQuery
  - internal/ledger/service/ledger_query.go — 0 findings on the new GetLedgerForQuery export wrapper (did not audit the validated-bit derivation end-to-end)
  - internal/rpc/types/errors.go — 0 findings (RpcISSUE_MALFORMED = 93 matches rippled)
  - internal/rpc/types/services.go — 0 findings (interface widening; matched by 9 mock additions)
  - keylet/keylet.go — 0 findings (currency-primary, issuer-secondary sort matches rippled Issue::operator<=>; IOU+IOU keylet bug fix)
  - internal/rpc/{account_channels,account_currencies,account_info,account_lines,account_nfts,deposit_authorized,gateway_balances,nft_offers,noripple_check,missing_methods}_test.go — mechanical mock interface additions
- Files cleanup-only (Phase 0 skipped Phase 1): none
- Cleanup commit: deferred — fix commit lands first, AI-comment cleanup pass to follow in a separate finalize run.
- Fix commit: Blocker + all five Minors + Nit resolved in the same branch. Highlights:
  - Blocker: dropped handler-local `currencyToBytes` in favor of `state.GetCurrencyBytes`; same swap applied in `ledger_entry.go`.
  - Minor: hoisted `GetLedgerForQuery` above the v<3 combo check; reorder is now ledger lookup → v<3 combo → field parse → v>=3 combo → SLE read.
  - Minor: asset / asset2 typed as `json.RawMessage`; new `parseUserIssue` silently defaults non-object values to XRP issue (matches rippled's `getIssue` lambda and `testInvalidAmmField`).
  - Minor: Service.getLedgerForQuery validated-bit derivation traced (numeric → ledger.IsValidated, closed-alias → equality check, validated-alias → true); `validatedReader` only pins upward so it cannot misreport `false` as `true`. No change.
  - Minor: both `lp_token` branches now emit `{currency, issuer, value}` with explicit string-typed value via `lpTokenValueFromSLE`.
  - Minor: `view.Read(keylet.Keylet{Key: ammKey})` replaced with `view.Read(keylet.AMMByID(ammKey))`.
  - Nit: dropped `ammKeyResolved` in favor of `!hasAMMAccount`.
  - Regression coverage: TestKeyletAMM_XRPPair_MatchesCanonical (guards against the ASCII-XRP keylet regression), TestParseUserIssue_{ValidObject,NonObjectFallsThroughToXRP,MalformedObject}, TestLPTokenValueFromSLE; updated TestAMMInfoMethod expectations to match new precedence.
- Notes: Branch was 57 commits behind main on entry; main merged locally and pushed (merge commit 7fb5f5b) before review. No conflicts.

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
