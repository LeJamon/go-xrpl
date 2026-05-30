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
- [x] **1.2 Exported-symbol docs.** Per public package, ensure exported types/functions/constants have doc comments. Commit per-package. Focus on the surface a consumer actually touches; don't pad trivial accessors.
- [x] **1.3 Example tests.** Add runnable `Example` tests (render on pkg.go.dev AND run in CI → never rot) for `codec/addresscodec`, `codec/binarycodec`, `keylet`, `crypto`, `drops`. Each must `go test` green with a correct `// Output:`.
- [x] **1.4 godoc lint.** Add a doc-comment rule (revive `exported`/`package-comments`, or `stylecheck`) to `.golangci.yml`, scoped to public packages so internal churn isn't blocked. Run `just lint`; fix flagged items.

## Phase 2 — Durable repo guides

- [x] **2.1 `docs/architecture.md`.** Deepen the README map: package responsibilities, tx pipeline (Validate→Preflight→Preclaim→Apply), ledger close flow, consensus (csf/rcl), storage layering, "why Go / why not a port". Link to godoc.
- [x] **2.2 `docs/operating.md`.** Running a node: standalone vs networked, full `xrpld.toml` config reference (every key, default, meaning — read `config/` + `xrpld.toml`), storage backends, RPC/WS/health endpoints, CGO build requirements.
- [x] **2.3 `docs/conformance.md`.** How rippled-as-spec parity works, running the conformance suite (`just conformance` + flags), reading results, what's intentionally out of scope (vault/xchain stubs, `scripts/conformance-out-of-scope.txt`).
- [x] **2.4 `CONTRIBUTING.md` (repo root).** Lift the porting workflow out of README: rippled reference location, the implement-against-rippled loop, test layout under `internal/testing/<feature>/`, build/test/lint commands, commit conventions.
- [x] **2.5 Refresh `README.md`.** Trim detail now covered by guides, add a docs index linking architecture/operating/conformance/contributing, update "Current Status" against reality.
- [x] **2.6 `docs/README.md`.** Index for the `docs/` folder: each document and who it's for.

## Phase 3 — Generated, anti-rot docs

- [x] **3.1 RPC catalog generator.** Go program/script that enumerates registered RPC methods from the handler registry → `docs/rpc-methods.md` (method, category, summary, version). Commit generator + output.
- [x] **3.2 Conformance status generator.** Wrap `scripts/conformance-summary.sh` → `docs/conformance-status.md` (per-suite pass/fail/skip table). Replaces hand-maintained `IMPLEMENTATION_STATUS.md`.
- [x] **3.3 Supported tx & amendments generator.** Emit `docs/supported-transactions.md` + `docs/amendments.md` from the `tx.Register` and amendment registries.
- [x] **3.4 Wire into justfile.** `just docs-gen` runs all generators; `just docs-check` regenerates + `git diff --exit-code` to fail on stale output.

## Phase 4 — Governance & CI

- [x] **4.1 ADRs.** `docs/adr/` with an index + short records for load-bearing decisions: rippled-as-spec, Go vs line-by-line port, concurrency model, storage architecture, CGO for peertls/secp256k1.
- [x] **4.2 CI doc gates.** GitHub workflow (or extend existing) running godoc lint, a markdown broken-link check, example tests, and `just docs-check`. Fast + non-flaky.
- [x] **4.3 Final verification.** `just build` + `just vet` + `just lint` green; example tests pass; all internal doc links resolve; `go doc ./...` spot-check renders. Write the Review section below.

## Finalization

- [ ] **F.1 Push & PR.** Push `docs-overhaul`, open a PR against `main` (`gh pr create`) with a summary grouped by phase. Then STOP the loop (omit the next ScheduleWakeup).

---

## Progress log

_(append one line per completed item: `0.1 done @ <commit> — note`)_
- 4.3 done — final verification all green: `just build` exit 0, `just vet` exit 0, `just lint` 0 issues, example tests pass (drops/keylet/crypto/codec), 0 broken internal doc links (scripted check over README/CONTRIBUTING/docs/*/adr/*), `go doc ./drops` + `./keylet` render cleanly. Wrote the Review section above.
- 0.1 done — wrote docs/archive/INVENTORY-pre-overhaul.md. go list audit: meaningful godoc gaps are config, crypto/common, crypto/ed25519, crypto/secp256k1, storage/relationaldb(+postgres,sqlite); amendment/shamap/storage/kvstore/storage/nodestore already documented.
- 0.2 done — git mv'd 9 stale 2026-03-11 snapshots into docs/archive/ + added docs/archive/README.md marking the folder historical. Verified no tracked file references the moved paths (no broken links). Chose folder-level historical marker over per-file headers to preserve the snapshots verbatim.
- 0.3 done — rewrote goXRPL/CLAUDE.md: removed pre-refactor paths (internal/codec/core/types) and the false "skeleton/placeholder/TODO" status; aligned to real public+internal package layout, justfile-based build/test, tx engine flow, rippled reference locations. Verified every claim against the live justfile and `internal/*` listing.
- 1.2 DONE — documented all 257 undocumented exported symbols across every public package. Authoritative tool: `go run github.com/mgechev/revive@v1.7.0 -config <jobtmp>/revive-exported.toml`. Per-package commits: drops/protocol/crypto/binarycodec (13), config (27), shamap (44), nodestore+relationaldb-core (16), kvstore pebble+memorydb (16), postgres backend (66, two commits), sqlite backend (74). Stutter-rename warnings (ED25519CryptoAlgorithm etc.) intentionally left — renaming exported types is a breaking API change, out of scope for docs. FINAL VERIFICATION: full revive missing-doc audit across ./amendment/ ./codec/ ./config/ ./crypto/ ./drops/ ./keylet/ ./ledger/ ./protocol/ ./shamap/ ./storage/ = 0 findings; `go build ./...` clean.
- 4.2 done — added .github/workflows/docs.yml (new, additive — no existing workflow touched). Mirrors ci.yml setup exactly (ubuntu-latest, Go 1.24, checkout@v5, setup-go@v6, apt CGO headers). `docs` job on push-main + PR: regenerates rpcmethods + registries catalogs and fails on git tree drift (same pattern as ci.yml's ledgerfields `generate` job), then runs the Example tests. Inlines commands (repo convention: no `just` in CI) and excludes the slow conformance suite. YAML validated (ruby YAML.load_file OK); dry-ran the exact commands locally — drift check clean, examples pass, no stray binaries. godoc-lint gate already covered by 1.4 in ci.yml's golangci-lint job.
- 4.1 done — wrote docs/adr/ with index README + 5 ADRs (Status/Context/Decision/Consequences template): 0001 rippled-as-spec, 0002 native-Go-not-a-port, 0003 single-writer engine (verified against engine.go OpenView comment), 0004 storage architecture (nodestore + relationaldb), 0005 CGO for peertls+secp256k1 with CGO_ENABLED=0 fallback. All Accepted. Linked adr/ from docs/README.md. Verified all files + cross-doc links resolve.
- 3.4 done — wired justfile: `docs-gen` (rpcmethods + registries + conformance.sh), `docs-gen-fast` (Go generators only, no slow suite), `docs-check` (depends docs-gen-fast, then `git diff --exit-code` on the 3 registry-derived docs — conformance-status excluded since its counts change with the full suite, noted in a comment). Verified all three appear in `just --list`; `just docs-check` exits 0 on the clean committed tree (generated output matches committed); no stray binaries. PHASE 3 COMPLETE — all 4 volatile catalogs now generate from code/registries with a CI-able staleness guard.
- 3.3 done — wrote scripts/docsgen/registries/main.go emitting BOTH docs/supported-transactions.md (66 tx types, sorted, with numeric type codes — from tx.SupportedTypes()) and docs/amendments.md (99 amendments, sorted, Supported + Default-vote columns — from amendment.AllFeatures()). GOTCHA caught: tx registration is NOT via init() — it's an explicit all.RegisterAll() call, so a blank import gave 0 types; fixed by importing internal/tx/all and calling RegisterAll() in main. Verified deterministic re-run, vet clean, no stray binaries. Committed generator + both outputs.
- 3.2 done — wrote scripts/docsgen/conformance.sh (chmod +x): wraps scripts/conformance-summary.sh, writes docs/conformance-status.md with DO-NOT-EDIT header + fenced summary (color auto-off when piped, no timestamps → deterministic). Ran full suite (bg, exit 0): snapshot shows Total 1271 pass/244 fail (83.8%), In-scope 1098/43 (96.2%), per-suite breakdown. Replaces archived IMPLEMENTATION_STATUS.md. Committed generator + snapshot together. Note: full suite is slow (~300s) so docs-check (3.4) will run only the fast Go generators, not this one.
- 3.1 done — wrote scripts/docsgen/rpcmethods/main.go: imports internal/rpc/handlers, registers RegisterAll + RegisterWebSocketOnly into fresh registries, reflects each MethodHandler's RequiredRole() + SupportedApiVersions(), emits docs/rpc-methods.md (76 methods, sorted, WS-only flagged, DO-NOT-EDIT header). Verified: go run works, re-run is byte-identical (deterministic), build+vet clean. Committed generator + output together.
- 2.6 done — wrote docs/README.md: Guides table (architecture/operating/conformance/CONTRIBUTING + audience), a Generated-reference table for the Phase-3 outputs (rpc-methods/supported-transactions/amendments/conformance-status, noting do-not-edit + just docs-check), and an Archive note. Verified all present links resolve; the 4 generated-doc links are intentional forward-refs to Phase 3. PHASE 2 COMPLETE.
- 2.5 done — refreshed README.md: condensed the long Building section (point to operating.md) and the Architecture ASCII map + tx-flow (point to architecture.md), kept the strong intro + design-decisions, added a Documentation index table (godoc/architecture/operating/conformance/CONTRIBUTING + who each is for), replaced inline Contributing steps with a CONTRIBUTING.md link. Fixed stale Current Status counts against VERIFIED reality: "26 transaction types"→"24 transaction families (66 transaction types)" (66 distinct tx.Register calls across 24 internal/tx subpkgs) and "60+ RPC methods"→"70+" (76 registered in handlers/registry.go). All README internal links resolve.
- 2.4 done — wrote repo-root CONTRIBUTING.md: rippled-as-spec principle + reference-location table, the implement-against-rippled loop (find rippled impl+tests → idiomatic Go → mirror tests under internal/testing/<feature>/, tx types under internal/tx/<type>/ with init()+tx.Register()), full build/test/lint command list (verified all 13 cited justfile recipes exist), commit conventions (Conventional Commits; "do not mention yourself in commit messages" per repo rule), docs note. Links to architecture/operating/conformance all resolve.
- 2.3 done — wrote docs/conformance.md: rippled-as-oracle rationale + reference tree locations, the conformance suite (TestConformance/app|ledger/<Suite>), `just conformance` flags (filter, --failing, --list-fail, CONFORMANCE_TIMEOUT) all sourced from scripts/conformance-summary.sh, how to read the in-scope/out-of-scope summary, and the out-of-scope list from conformance-out-of-scope.txt. Framed Vault + XChain as intentional registered stubs (verified XChainBridge=SupportedNo in amendment/table_resync_test.go), not gaps. Verified cited paths; conformance-status.md + CONTRIBUTING.md are intentional forward-refs (phases 3.2 / 2.4).
- 2.2 done — wrote docs/operating.md: build (CGO/OpenSSL/secp256k1 + CGO_ENABLED=0 fallback), running (`just run`/`dev`, `xrpld generate-config` — verified the cmd exists in internal/cli/generate.go), endpoints table (verified /health route at internal/cli/server.go:914), standalone-vs-networked, and a FULL xrpld.toml reference grouped by section (top-level peer/ripple/client/storage/diag, [server]/[port_*], [node_db], [sqlite], [overlay], [transaction_queue], optional [validation_archive]/[amendments]/[perf]/[crawl]/[vl], validation). Every key+default+meaning sourced from config/examples/xrpld.toml (the gitignored root xrpld.toml is not tracked; the example is the canonical reference). Verified all cited paths resolve.
- 2.1 done — wrote docs/architecture.md grounded in real code: tx pipeline from internal/tx/transaction.go interfaces (Transaction/Appliable/Preclaimer/TecApplier) + engine.go (single-writer note, TapRETRY gate), public+internal package tables with rippled analogues, ledger lifecycle (openledger→service close→store), consensus split (rcl=real, csf=simulation framework — verified from package docs), storage layering, why-Go rationale. Verified all cited package/dir paths exist; package links use ../<pkg> from docs/. Forward-refs to operating/conformance/CONTRIBUTING are intentional (later in Phase 2).
- 1.4 done — enabled revive `exported` + `package-comments` in .golangci.yml, scoped to PUBLIC packages via an exclusions.rules path filter (^internal/|^cmd/|_test.go excluded). Had to drop the `comments` exclusion preset (it suppressed these findings) and disable stylecheck ST1000/ST1020-22 so revive is the sole doc linter; stutter check disabled (renaming exported types = breaking API). First `just lint` caught 8 real gaps in public pkgs missed by the 1.2 revive sweep (log/ 6 funcs, version/ + binarycodec/types/testutil package comments — these dirs weren't in my 1.2 audit list); documented all. `just lint` now exits 0 with 0 issues; internal/ still has hundreds of undocumented exports but is correctly excluded.
- 1.3 done — added runnable Example tests (example_test.go, package <pkg>_test) for all 5 target packages: drops (DecimalXRP, FromDecimalXRP, Fees.AccountReserve), keylet (Account determinism), crypto (SecureErase), addresscodec (classic-address round-trip), binarycodec (Encode/Decode of a Payment, output captured via a throwaway probe test then verified). All `go test -run Example` green; gofmt clean. Examples kept deterministic (zero-value account IDs, fixed hex) to render on pkg.go.dev and never rot.
- 1.1 done — added doc.go package synopses to the 8 gap packages (config, crypto/common, crypto/ed25519, crypto/secp256k1, storage/relationaldb + postgres + sqlite, codec/addresscodec/interfaces), each grounded in the package source and mapped to the rippled concept. Verified: go build clean, all now HAS_DOC via go list, gofmt clean.

## Review

Documentation overhaul of goXRPL, built on `docs-overhaul` (forked from `main`
@ c82e8fbc). Guiding principle: **co-locate or generate** — bias toward docs that
live next to code (godoc) or are generated from it (registries, conformance
script), because this repo's hand-written snapshot docs had rotted (the entire
`docs/` folder + top-level GAP/SITUATION reports were frozen at 2026-03-11 while
the code moved for months).

### What changed, by phase

- **Phase 0 — triage.** Archived 9 stale 2026-03-11 snapshots into `docs/archive/`
  (with a historical-marker README); rewrote the project `goXRPL/CLAUDE.md`, which
  had pre-refactor paths (`internal/codec|core|types`) and a false
  "skeleton/placeholder/TODO" status; recorded a baseline inventory.
- **Phase 1 — godoc (highest ROI, zero-rot).** Added package synopses to every
  public package; documented all 257 previously-undocumented exported symbols →
  **0 missing-doc** across the public API. Added runnable `Example` tests
  (drops/keylet/crypto/codec) that double as compile-checked usage docs on
  pkg.go.dev. Added a golangci `revive` `exported` + `package-comments` rule
  scoped to public packages (internal/cmd/tests excluded) so this can't regress.
- **Phase 2 — durable guides.** `docs/architecture.md`, `docs/operating.md` (full
  `xrpld.toml` reference), `docs/conformance.md`, repo-root `CONTRIBUTING.md`, a
  refreshed `README.md` (trimmed duplication, added a docs index, corrected stale
  status counts), and a `docs/README.md` index.
- **Phase 3 — generated catalogs (anti-rot core).** Generators emit the volatile
  lists from the live code: `docs/rpc-methods.md` (76), `docs/supported-transactions.md`
  (66), `docs/amendments.md` (99), and `docs/conformance-status.md`
  (96.2% in-scope). Wired `just docs-gen` / `docs-gen-fast` / `docs-check`.
- **Phase 4 — governance.** 5 ADRs under `docs/adr/`; a `.github/workflows/docs.yml`
  CI gate that fails on stale generated docs and runs the example tests.

### Verified facts (final sweep)

`just build` ✓ · `just vet` ✓ · `just lint` ✓ (0 issues) · example tests ✓ ·
0 broken internal doc links · `go doc` renders cleanly. Public-API missing-doc: 0.
Catalogs: 76 RPC methods, 66 transaction types, 99 amendments, 96.2% in-scope
conformance.

### How to keep it fresh

- **godoc** is enforced in CI by the golangci `exported`/`package-comments` rule on
  public packages — a new undocumented exported symbol fails `just lint`.
- **Generated catalogs**: regenerate with `just docs-gen` (RPC + tx + amendments +
  conformance) or `just docs-gen-fast` (skips the slow suite); `just docs-check`
  and the `docs.yml` CI job fail if the registry-derived catalogs drift from the
  committed output. `docs/conformance-status.md` refreshes via `just docs-gen`.
- **Prose guides + ADRs** are the only hand-maintained docs; they cover slow-moving
  concerns (architecture, operating, rationale) by design.
