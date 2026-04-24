# Comment Cleanup — Design

**Date:** 2026-04-24
**Scope:** `goXRPL/` tree only. `rippled/` is read-only reference and is not touched.

## Problem

The codebase has accumulated low-value comments — godoc that restates the identifier, trailing comments on consts that echo the name, inline what-comments that narrate the next line, stylistic section separators, and commented-out code. A subset of TODOs point at work that is already done. These add visual weight without improving comprehension and, in places, make the comment-to-code ratio hide the actual load-bearing commentary.

At the same time, the codebase has a large volume of genuinely load-bearing commentary — `// Reference: rippled …` cross-references in ~1,700+ files, amendment-gate explanations, invariant-check notes, and crypto/DER alignment notes. These are critical because the project treats rippled as the protocol source of truth (per `CLAUDE.md`). The cleanup must be subtractive, targeted, and conservative around these categories.

## Goals

- Remove the five categories of low-value comments defined below.
- Preserve every comment that helps a reader validate Go behavior against the rippled C++ reference or against XRPL protocol invariants.
- Ship in small per-package commits so any regression (a removed comment turning out to be load-bearing) is cheap to revert.

## Non-goals

- No reformatting, reordering, or refactoring of code.
- No adding new comments — this is a subtraction-only pass.
- No changes to `rippled/`, `docs/`, `scripts/`, or `tasks/`.
- No mass rewriting of borderline godoc — if a godoc is 50% useful, it stays.
- No style unification of surviving comments (no capitalization/punctuation normalization).

## Removable categories

1. **Godoc restatements** — godoc above an exported symbol that only echoes the identifier (`// Foo does foo` above `func Foo()`). Kept when it documents parameters, return values, error conditions, side effects, or non-obvious semantics.
2. **Const/enum echoes** — trailing comments that only repeat the const name in a different case (e.g., `TypePayment Type = 0 // ttPAYMENT`). Dropped when the comment adds no information beyond the identifier.
3. **Obvious inline what-comments** — `// Return the result` above `return result`, `// Loop over X` above `for _, x := range X`, `// Increment counter` above `counter++`. Dropped.
4. **Section headers and separators** — `// ===== Foo =====`, `// ---- helpers ----`, long dashed dividers. Dropped. Go's package and function structure is the boundary.
5. **Stale TODOs and commented-out code** — TODOs verified done (grep the referenced feature/symbol) or pointing at no actionable work; commented-out function bodies or blocks. Dropped. Live TODOs pointing at real gaps are kept.

## Hard preservation list (never touched)

- `// Reference: rippled …` and any variant (`// Mirrors rippled's …`, `// Matches rippled …`, `// See rippled/src/…`).
- Amendment-gate comments — anything mentioning an amendment name or `fix*` / `feature*` flag.
- Invariant-check, preflight, and preclaim explanation comments.
- Crypto / DER / signature algorithm alignment notes.
- Struct-tag and serialization format specs.
- Package-level `doc.go` files — entire files skipped.
- Generated files — any file containing `// Code generated …  DO NOT EDIT.` (includes `mock_*.go`).
- `rippled/` directory.
- Live TODOs pointing at real gaps.

**Heuristic for "is this load-bearing?":** if removing the comment would make a protocol-compliance mistake harder to catch, keep it.

## Execution workflow

Pattern-based detection per package, followed by human review, applied via the `Edit` tool, gated by build and tests, committed per package group.

### Package order

Low-risk leaf packages first, core consensus/tx last:

1. `codec/` (addresscodec, binarycodec)
2. `crypto/` + `drops/` + `keylet/` + `protocol/`
3. `ledger/entry/` + `shamap/` + `storage/`
4. `amendment/` + `config/`
5. `internal/rpc/`
6. `internal/tx/` subpackages (per tx type: payment, offer, escrow, amm, …)
7. `internal/tx/engine.go` + `apply_state_table.go` + `signature.go` (highest-risk, last of production)
8. `internal/consensus/` + `internal/txq/`
9. `internal/testing/` (test helpers)
10. `*_test.go` files across the tree (pattern pass)

### Per-package-group loop

1. Run category-specific greps to produce a candidate list.
2. Review candidates. Drop from the candidate list anything that:
   - is on the hard preservation list, or
   - references rippled, an amendment, an invariant, a TER code, or a protocol constant, or
   - documents a non-obvious constraint that the identifier alone does not convey.
3. Apply edits.
4. `go build ./...` from repo root.
5. `go test ./<package>/...` for the touched group.
6. `goimports -l` on modified files must be clean.
7. Commit with message `chore(<pkg>): prune non-informative comments`.

## Verification gates

Every commit must satisfy all of:

- `go build ./...` clean.
- `go test ./<package>/...` passes for the touched group.
- `goimports -l` clean on modified files.
- No edits to files on the hard-preservation list (grep check before commit).
- No `// Reference:` comments removed (grep `git diff` for the pattern — must be empty).

If any gate fails: stop, investigate, fix or revert that file. Do not skip gates.

## Rollback plan

Per-package commits enable per-package reverts. If a downstream reader later flags a removed comment as load-bearing, `git revert <commit>` on just that package restores it without undoing other groups.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| A removed comment was actually load-bearing context for rippled alignment. | Hard-preserve `// Reference:` and amendment comments; per-package commits allow targeted revert. |
| Pattern matching sweeps a comment that is ambiguous. | Every candidate is reviewed before edit; borderline cases stay. |
| Build or test regression from an accidental code edit. | Per-commit `go build` and `go test` gates. |
| Godoc of an exported symbol removed, breaking docs consumers. | Only restatement-style godoc (echoing the identifier) is eligible; substantive godoc stays. |
| Inconsistent judgment across packages during a long pass. | Written rules above; package groups are small enough that one reviewer holds the rules in context for each commit. |

## Out of scope for this spec

Future passes that could follow, but are explicitly not part of this work:

- Rewriting or normalizing surviving godoc to a consistent style.
- Upgrading `// Reference:` comments to include line numbers or permalink to a specific rippled commit.
- Pruning `rippled/` — read-only, never touched.
- Documentation rewrites under `docs/`.
