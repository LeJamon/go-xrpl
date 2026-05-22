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
