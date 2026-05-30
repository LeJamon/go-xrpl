# goXRPL Documentation Overhaul

**Worktree (work ONLY here):** `/Users/thomashussenet/Documents/project_goXRPL/goXRPL/.claude/worktrees/docs-overhaul`
**Branch:** `docs-overhaul` (forked from latest `origin/main` @ c82e8fbc)
**Remote:** `git@github.com:LeJamon/go-xrpl.git`
**Driver:** self-paced `/loop`. One checklist item per iteration → implement → verify → commit → tick the box → schedule next wake-up. Stop the loop when every box is ticked and the PR is open.

## Guiding principle — co-locate or generate

This repo's standalone prose docs rot fast: the entire `docs/` folder and the top-level GAP/SITUATION reports froze at **2026-03-11** while code moved for months after. Therefore:

- Prefer documentation that lives **next to code** (godoc) or is **generated from code** (registries, conformance script). It can't go stale.
- Reserve hand-written prose for things that change slowly: architecture, rationale, how-to-contribute.
- Delete or archive anything that is a frozen snapshot of fast-moving state.

## Audiences (priority order)

1. **Library consumers** → godoc on public packages (broadest reach, zero rot). Highest ROI.
2. **Node operators** → `docs/operating.md`, config reference.
3. **Contributors** → `CONTRIBUTING.md`, conformance docs.

## Rules for every iteration

- Work ONLY inside the worktree above. Always `cd` to it before git ops; echo `pwd` + branch first (Bash cwd persists across calls and other bg jobs share worktrees).
- **Rippled is the source of truth** — any doc describing protocol behavior must match `../../../../rippled/` (the local reference tree), not be invented.
- Production quality. No placeholder/TODO docs. Match the existing README's tone and depth.
- Verify before committing: relevant `just build` / `just test-*` / `just lint` must pass. Never commit a broken build.
- One logical change per commit. Conventional commit messages (`docs:`, `chore:`, `test:`). **Never mention Claude/AI in commits.**
- Prefer additive, safe changes. For risky changes (`.golangci.yml`, CI workflows) verify locally first; if a lint rule is too noisy to satisfy cleanly, scope it down rather than leaving lint red.
- NEVER use `git stash` (stash stack is shared across worktrees here — it will collide with other bg jobs).
- After finishing an item, tick its box in this file and include that in the same commit.

---

## Phase 0 — Triage & baseline

- [x] **0.1 Baseline inventory.** Create `docs/archive/INVENTORY-pre-overhaul.md`: list every existing doc (README, `docs/*`, top-level `*.md`, both `CLAUDE.md`), its last-modified date, and a verdict (keep / refresh / archive / delete). Audit godoc package-comment coverage across ALL public packages and record gaps here.
- [x] **0.2 Archive stale snapshots.** `git mv` the frozen 2026-03-11 docs into `docs/archive/` with a one-line header marking them historical: `docs/IMPLEMENTATION_STATUS.md`, `docs/offer_create_comparison.md`, `docs/offer_create_spec.md`, `docs/performance_bottlenecks.md`, `docs/PLAN_UNSKIP_PHASE2.md`, `docs/SKIPPED_TESTS.md`, and top-level `FEATURE_GAP_ANALYSIS.md`, `GAP_ANALYSIS_STANDALONE_NODE.md`, `SITUATION_REPORT.md`. (Confirm each still exists on this branch first.)
- [x] **0.3 Fix project `CLAUDE.md`.** `goXRPL/CLAUDE.md` references pre-refactor paths (`internal/codec`, `internal/core`, `internal/types`) and an outdated RPC list. Reconcile it with the real current structure (the repo-root `CLAUDE.md` is accurate — align to it).

## Phase 1 — godoc / API reference (highest ROI)

- [x] **1.1 Package doc gaps.** Add a package comment to every public package lacking one. Confirmed gaps (go list audit, see inventory): `config`, `crypto/common`, `crypto/ed25519`, `crypto/secp256k1`, `storage/relationaldb` (+ `postgres`, `sqlite`); minor: `codec/addresscodec/interfaces`. (NOT `amendment`/`shamap`/`storage/kvstore`/`storage/nodestore` — those already have synopses.) Re-audit and cover ALL public packages: `amendment`, `codec`/`addresscodec`/`binarycodec`, `config`, `crypto`/`common`, `drops`, `keylet`, `ledger/entry`, `protocol`, `shamap`, `storage` + subpkgs. Each comment: what the package does, its role in the node, and the rippled concept it maps to.
- [ ] **1.2 Exported-symbol docs.** Per public package, ensure exported types/functions/constants have doc comments. Commit per-package. Focus on the surface a consumer actually touches; don't pad trivial accessors.
- [ ] **1.3 Example tests.** Add runnable `Example` tests (render on pkg.go.dev AND run in CI → never rot) for `codec/addresscodec`, `codec/binarycodec`, `keylet`, `crypto`, `drops`. Each must `go test` green with a correct `// Output:`.
- [ ] **1.4 godoc lint.** Add a doc-comment rule (revive `exported`/`package-comments`, or `stylecheck`) to `.golangci.yml`, scoped to public packages so internal churn isn't blocked. Run `just lint`; fix flagged items.

## Phase 2 — Durable repo guides

- [ ] **2.1 `docs/architecture.md`.** Deepen the README map: package responsibilities, tx pipeline (Validate→Preflight→Preclaim→Apply), ledger close flow, consensus (csf/rcl), storage layering, "why Go / why not a port". Link to godoc.
- [ ] **2.2 `docs/operating.md`.** Running a node: standalone vs networked, full `xrpld.toml` config reference (every key, default, meaning — read `config/` + `xrpld.toml`), storage backends, RPC/WS/health endpoints, CGO build requirements.
- [ ] **2.3 `docs/conformance.md`.** How rippled-as-spec parity works, running the conformance suite (`just conformance` + flags), reading results, what's intentionally out of scope (vault/xchain stubs, `scripts/conformance-out-of-scope.txt`).
- [ ] **2.4 `CONTRIBUTING.md` (repo root).** Lift the porting workflow out of README: rippled reference location, the implement-against-rippled loop, test layout under `internal/testing/<feature>/`, build/test/lint commands, commit conventions.
- [ ] **2.5 Refresh `README.md`.** Trim detail now covered by guides, add a docs index linking architecture/operating/conformance/contributing, update "Current Status" against reality.
- [ ] **2.6 `docs/README.md`.** Index for the `docs/` folder: each document and who it's for.

## Phase 3 — Generated, anti-rot docs

- [ ] **3.1 RPC catalog generator.** Go program/script that enumerates registered RPC methods from the handler registry → `docs/rpc-methods.md` (method, category, summary, version). Commit generator + output.
- [ ] **3.2 Conformance status generator.** Wrap `scripts/conformance-summary.sh` → `docs/conformance-status.md` (per-suite pass/fail/skip table). Replaces hand-maintained `IMPLEMENTATION_STATUS.md`.
- [ ] **3.3 Supported tx & amendments generator.** Emit `docs/supported-transactions.md` + `docs/amendments.md` from the `tx.Register` and amendment registries.
- [ ] **3.4 Wire into justfile.** `just docs-gen` runs all generators; `just docs-check` regenerates + `git diff --exit-code` to fail on stale output.

## Phase 4 — Governance & CI

- [ ] **4.1 ADRs.** `docs/adr/` with an index + short records for load-bearing decisions: rippled-as-spec, Go vs line-by-line port, concurrency model, storage architecture, CGO for peertls/secp256k1.
- [ ] **4.2 CI doc gates.** GitHub workflow (or extend existing) running godoc lint, a markdown broken-link check, example tests, and `just docs-check`. Fast + non-flaky.
- [ ] **4.3 Final verification.** `just build` + `just vet` + `just lint` green; example tests pass; all internal doc links resolve; `go doc ./...` spot-check renders. Write the Review section below.

## Finalization

- [ ] **F.1 Push & PR.** Push `docs-overhaul`, open a PR against `main` (`gh pr create`) with a summary grouped by phase. Then STOP the loop (omit the next ScheduleWakeup).

---

## Progress log

_(append one line per completed item: `0.1 done @ <commit> — note`)_
- 0.1 done — wrote docs/archive/INVENTORY-pre-overhaul.md. go list audit: meaningful godoc gaps are config, crypto/common, crypto/ed25519, crypto/secp256k1, storage/relationaldb(+postgres,sqlite); amendment/shamap/storage/kvstore/storage/nodestore already documented.
- 0.2 done — git mv'd 9 stale 2026-03-11 snapshots into docs/archive/ + added docs/archive/README.md marking the folder historical. Verified no tracked file references the moved paths (no broken links). Chose folder-level historical marker over per-file headers to preserve the snapshots verbatim.
- 0.3 done — rewrote goXRPL/CLAUDE.md: removed pre-refactor paths (internal/codec/core/types) and the false "skeleton/placeholder/TODO" status; aligned to real public+internal package layout, justfile-based build/test, tx engine flow, rippled reference locations. Verified every claim against the live justfile and `internal/*` listing.
- 1.1 done — added doc.go package synopses to the 8 gap packages (config, crypto/common, crypto/ed25519, crypto/secp256k1, storage/relationaldb + postgres + sqlite, codec/addresscodec/interfaces), each grounded in the package source and mapped to the rippled concept. Verified: go build clean, all now HAS_DOC via go list, gofmt clean.

## Review (fill in at 4.3 / F.1)

_(to be completed)_
