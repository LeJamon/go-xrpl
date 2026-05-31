# Documentation inventory — pre-overhaul baseline

Snapshot taken at the start of the documentation overhaul (branch `docs-overhaul`,
forked from `main` @ `c82e8fbc`). This records the state the overhaul started from
so the archived material has context. It is itself a historical document — do not
keep it up to date.

## Prose documents

| Path | Last commit | Verdict | Notes |
|------|-------------|---------|-------|
| `README.md` | 2026-05-18 | **keep / refresh** | Strong front door. Trim detail that moves into `docs/` guides; add a docs index. |
| `CLAUDE.md` (repo root) | current | keep | Accurate structure; the authoritative map. |
| `goXRPL/CLAUDE.md` (project) | 2025-07-23 | **refresh** | Stale: references pre-refactor paths (`internal/codec`, `internal/core`, `internal/types`) and an outdated RPC list. Item 0.3. |
| `FEATURE_GAP_ANALYSIS.md` | 2026-03-11 | **archive** | Frozen snapshot of fast-moving completion state. |
| `GAP_ANALYSIS_STANDALONE_NODE.md` | 2026-03-11 | **archive** | Frozen snapshot. |
| `SITUATION_REPORT.md` | 2026-03-11 | **archive** | Frozen snapshot. |
| `docs/IMPLEMENTATION_STATUS.md` | 2026-03-11 | **archive** | Replaced by generated `docs/conformance-status.md` (item 3.2). |
| `docs/offer_create_comparison.md` | 2026-03-11 | **archive** | Point-in-time porting note. |
| `docs/offer_create_spec.md` | 2026-03-11 | **archive** | Point-in-time porting note. |
| `docs/performance_bottlenecks.md` | 2026-03-11 | **archive** | Point-in-time analysis. |
| `docs/PLAN_UNSKIP_PHASE2.md` | 2026-03-11 | **archive** | Completed plan doc. |
| `docs/SKIPPED_TESTS.md` | 2026-03-11 | **archive** | Snapshot; live skip list lives in the tests. |

Verdict legend: **keep** (current, leave as-is), **refresh** (rewrite in place this
overhaul), **archive** (move under `docs/archive/`, mark historical).

## godoc package-comment coverage (public packages)

Authoritative read via `go list -f '{{.Doc}}'` over the public package trees.
`HAS_DOC` = a package synopsis is present; `NO_DOC` = missing.

| Package | godoc | Action (item 1.1) |
|---------|-------|-------------------|
| `amendment` | HAS_DOC | ok |
| `codec/addresscodec` | HAS_DOC | ok |
| `codec/addresscodec/interfaces` | NO_DOC | low priority (internal-facing interfaces subpkg) |
| `codec/binarycodec` (+ `definitions`, `serdes`, `types`) | HAS_DOC | ok |
| `config` | NO_DOC | **add** |
| `crypto` | HAS_DOC | ok |
| `crypto/common` | NO_DOC | **add** |
| `crypto/ed25519` | NO_DOC | **add** |
| `crypto/secp256k1` | NO_DOC | **add** |
| `crypto/secp256k1/shim` | HAS_DOC | ok |
| `crypto/rfc1751` | HAS_DOC | ok |
| `drops` | HAS_DOC | ok |
| `keylet` | HAS_DOC | ok |
| `ledger/entry` | HAS_DOC | ok |
| `protocol` | HAS_DOC | ok |
| `shamap` | HAS_DOC | ok |
| `storage/kvstore` (+ `memorydb`, `pebble`) | HAS_DOC | ok |
| `storage/nodestore` | HAS_DOC | ok |
| `storage/relationaldb` | NO_DOC | **add** |
| `storage/relationaldb/postgres` | NO_DOC | **add** |
| `storage/relationaldb/sqlite` | NO_DOC | **add** |

**Summary:** coverage is better than first assumed. The meaningful public-package
gaps are: `config`, `crypto/common`, `crypto/ed25519`, `crypto/secp256k1`,
`storage/relationaldb` (+ `postgres`, `sqlite`). One minor interfaces subpackage
(`codec/addresscodec/interfaces`) is low priority. Core packages that were
initially suspected (`amendment`, `shamap`, `storage/kvstore`, `storage/nodestore`)
already have valid package synopses — no action needed.

## Overhaul targets (see `tasks/doc-overhaul.md` for the live checklist)

- Phase 0 — triage (this inventory, archive stale docs, fix project CLAUDE.md)
- Phase 1 — godoc completeness + example tests + doc lint
- Phase 2 — durable guides (architecture, operating, conformance, CONTRIBUTING)
- Phase 3 — generated docs (RPC catalog, conformance status, supported tx/amendments)
- Phase 4 — ADRs + CI doc gates
