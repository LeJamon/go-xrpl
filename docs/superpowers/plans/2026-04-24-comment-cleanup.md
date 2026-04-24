# Comment Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Subtract low-value comments (godoc restatements, const echoes, obvious what-comments, section separators, stale TODOs, commented-out code) across `goXRPL/` while preserving every comment that ties Go code to rippled, amendments, or protocol invariants.

**Architecture:** Pattern-based candidate detection per package group, followed by human review, applied via `Edit`, gated by `go build ./...` + `go test` + `goimports -l`, committed per package group. Each commit is independently revertable. No code reordering, no refactoring, no new comments.

**Tech Stack:** Go 1.24, `goimports`, `git`, ripgrep (`rg` preferred; falls back to `grep -rnE`).

**Spec:** `docs/superpowers/specs/2026-04-24-comment-cleanup-design.md`

---

## Ground rules for every task

These apply to every task below. Re-read them if you are picking up mid-plan.

### Removable categories (candidates)

1. **Godoc restatement** — `// Foo does foo` above `func Foo()` that only echoes the identifier.
2. **Const/enum echo** — trailing comment that repeats the const name in another case (e.g., `TypePayment Type = 0 // ttPAYMENT`).
3. **Obvious inline what-comment** — `// Return the result` above `return result`, etc.
4. **Section header / separator** — `// ===== Foo =====`, `// ---- helpers ----`, long dashed dividers.
5. **Stale TODO or commented-out code** — TODOs verified done, or `// func oldThing() { ... }` blocks.

### Hard preservation list (never remove)

Before removing any candidate, check the comment against this list. If it matches, **keep it**:

- Contains `Reference:`, `Mirrors rippled`, `Matches rippled`, `See rippled`, or any rippled file path.
- Mentions an amendment name (grep hint: `amendment`, `feature`, `fix`, `Amendment`).
- Documents an invariant, preflight, preclaim, or TER code.
- Explains a crypto/DER/signature algorithm or byte layout.
- Describes a struct-tag or serialization format.
- Is in a `doc.go` file (skip these files entirely).
- Is in a file containing `// Code generated ... DO NOT EDIT.` (skip these files entirely).
- Documents params, return values, errors, side effects, or non-obvious semantics (even for category 1).
- Is a live TODO pointing at real gap (grep the referenced symbol; if it is unimplemented, keep the TODO).

### Per-task workflow (apply to every package group)

For each task below, perform these steps in order. Do not skip gates.

**W1. Baseline** — run `go build ./...` from repo root. Must pass before you start. If it does not, stop and investigate; do not attribute pre-existing breakage to this cleanup.

**W2. Enumerate candidates** — run each category grep listed in the task against the task's paths. Pipe to a local scratch file if useful; do not commit it.

**W3. Review candidates** — open each candidate file at the matched line with `Read`. Apply the hard preservation list. Discard any candidate that is preserved.

**W4. Apply edits** — use `Edit` (not `sed`, not `Write`). Remove only the comment line(s). Do not alter code lines. Do not reorder. Do not fix typos in neighbouring code.

**W5. Build gate** — `go build ./...` from repo root. Must pass.

**W6. Test gate** — `go test <task's test target>`. Must pass. If a previously-passing test fails, revert the last edit and re-review the candidate.

**W7. Format gate** — `goimports -l <modified files>`. Output must be empty.

**W8. Preservation audit** — run `git diff --unified=0 | rg -i 'reference:|amendment|mirrors rippled|matches rippled'`. Output must be empty. If not, you removed a preserved comment — restore it.

**W9. Commit** — use the commit message given in the task. Do not add unrelated files.

### Category grep patterns

Use these as the starting point for W2. `rg` is preferred; swap `rg` for `grep -rnE` if unavailable.

```bash
# Category 2 — const/enum echoes: trailing comments on declarations
rg -nP '^\s*\w+\s*(=|:=|[A-Z]\w*\s+=)\s+[^/]+//\s*\w+\s*$' <paths> --type=go

# Category 3 — obvious inline what-comments (heuristic start; must review)
rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip)\b[^.]{0,40}$' <paths> --type=go

# Category 4 — section headers and separators
rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,}|#{2,})' <paths> --type=go

# Category 5a — TODO/FIXME/XXX markers (review live vs stale)
rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b' <paths> --type=go

# Category 5b — commented-out code (review; catches godoc too)
rg -nP '^\s*//\s*(func |if |for |return |var |const |type |switch |go |defer )' <paths> --type=go

# Category 1 — godoc that echoes the identifier (manual scan, no clean grep)
# Read each *.go; for `// X …` immediately above `func X`, `type X`, `var X`, `const X`,
# judge: is there content beyond echoing `X`? If no, candidate.
```

### Commit message convention

```
chore(<pkg>): prune non-informative comments

Removes <N> low-value comments across <files>.
No rippled references, amendment gates, or invariant notes touched.
```

Fill in `<pkg>`, `<N>`, and `<files>` (keep `<files>` short — use the package name if many).

---

## File Map

This plan edits only comment lines. No code changes. No file creations. Files touched are bounded by the task paths below.

| Task | Paths (relative to `goXRPL/`) |
|------|-------------------------------|
| 0 | — (baseline only) |
| 1 | `codec/addresscodec/`, `codec/binarycodec/` |
| 2 | `crypto/`, `drops/`, `keylet/`, `protocol/` |
| 3 | `ledger/entry/`, `shamap/`, `storage/` |
| 4 | `amendment/`, `config/` |
| 5 | `internal/rpc/` |
| 6 | `internal/tx/<per-tx-type>/` (account, amm, batch, check, clawback, credential, delegate, depositpreauth, did, escrow, ledgerstatefix, mpt, nftoken, offer, oracle, paychan, payment, permissioneddomain, pseudo, signerlist, ticket, trustset, vault, xchain) |
| 7 | `internal/tx/` top-level (`engine.go`, `apply_state_table.go`, `signature.go`, `registry.go`, `flatten.go`, others) |
| 8 | `internal/consensus/`, `internal/txq/` |
| 9 | `internal/testing/` and its subpackages |
| 10 | `*_test.go` sweep (any test files not covered above) |
| 11 | Final full-tree verification |

---

## Task 0: Baseline verification

Before any edits, verify the tree is in a clean, passing state. Any later breakage must be attributable to this cleanup, not to pre-existing issues.

**Files:** none (verification only).

- [ ] **Step 1: Confirm clean working tree**

Run from `/Users/thomashussenet/Documents/project_goXRPL/goXRPL`:

```bash
git status --short
```

Expected: only the spec + plan files from earlier commits show as untracked or nothing. If unrelated modifications exist, stop and ask the user.

- [ ] **Step 2: Baseline build**

```bash
go build ./...
```

Expected: exits 0 with no output. If it fails, stop — this is not a cleanup problem.

- [ ] **Step 3: Baseline tests (smoke)**

```bash
go test ./codec/... ./crypto/... ./ledger/... ./amendment/... ./protocol/... ./keylet/... ./drops/... ./shamap/... ./storage/...
```

Expected: PASS. Known-failing tests may exist; record which suites fail at baseline so you can distinguish them from regressions later.

- [ ] **Step 4: Confirm tooling**

```bash
which goimports
command -v rg || command -v grep
```

Expected: `goimports` resolves; either `rg` or `grep` is available.

- [ ] **Step 5: Verify `rippled/` is not inside the module**

```bash
git ls-files | rg '^rippled/' | head -5
```

Expected: empty. If `rippled/` shows up in `git ls-files`, double-check before proceeding — this plan must not touch that directory.

No commit. Move to Task 1.

---

## Task 1: codec/ (addresscodec, binarycodec)

Leaf package group. Format specs are load-bearing — preserve any comment describing byte layouts, test vectors, or hash prefix constants.

**Files:** modify only comment lines in `codec/addresscodec/**/*.go` and `codec/binarycodec/**/*.go`. Skip `doc.go`, skip `testutil/mock_*.go`, skip any file containing `// Code generated`.

- [ ] **Step 1: W1 — build baseline**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: W2 — enumerate candidates**

From `goXRPL/`:
```bash
rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,})' codec/ --type=go
rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b' codec/ --type=go
rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip)\b[^.]{0,40}$' codec/ --type=go
rg -nP '^\s*\w[^=]*=\s*[^/]+//\s*\w+\s*$' codec/ --type=go
rg --files codec/ --type=go | rg -v '/doc\.go$|/testutil/mock_' | while read f; do
  rg -l 'Code generated .* DO NOT EDIT\.' "$f" >/dev/null && continue
  printf '%s\n' "$f"
done > /tmp/codec-files.txt
```

- [ ] **Step 3: W3 — review and prune the candidate list**

For each grep hit, `Read` the file at the hit line (±3 lines). Apply the hard preservation list. Format/byte-layout comments in `binarycodec/types/*.go` are overwhelmingly load-bearing — default to keep unless the comment is a pure header/separator or an identifier echo.

- [ ] **Step 4: W4 — apply edits**

Use `Edit` for each comment line to remove. Remove only the comment line. Do not change code.

- [ ] **Step 5: W5 — build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 6: W6 — test**

```bash
go test ./codec/...
```
Expected: PASS (modulo baseline known-fails from Task 0 Step 3).

- [ ] **Step 7: W7 — format**

```bash
goimports -l $(git diff --name-only | rg '\.go$')
```
Expected: empty output.

- [ ] **Step 8: W8 — preservation audit**

```bash
git diff --unified=0 | rg -i 'reference:|mirrors rippled|matches rippled|see rippled|amendment|invariant|preflight|preclaim|TER'
```
Expected: empty. If any line appears, a preserved comment was removed — restore it and re-run the audit.

- [ ] **Step 9: W9 — commit**

```bash
git add -u codec/
git commit -m "$(cat <<'EOF'
chore(codec): prune non-informative comments

Removes low-value comments (headers, echoes, stale markers) across
addresscodec/ and binarycodec/. No rippled references, amendment gates,
format specs, or invariant notes touched.
EOF
)"
```

---

## Task 2: crypto/, drops/, keylet/, protocol/

Foundation packages. Crypto algorithm commentary is load-bearing — preserve anything explaining DER, secp256k1, ed25519, RFC references, or byte layouts.

**Files:** modify only comment lines in `crypto/**/*.go`, `drops/**/*.go`, `keylet/**/*.go`, `protocol/**/*.go`. Skip `doc.go`, generated files, `testutil/mock_*.go`.

- [ ] **Step 1: W1 — build baseline**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: W2 — enumerate candidates**

```bash
rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,})' crypto/ drops/ keylet/ protocol/ --type=go
rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b' crypto/ drops/ keylet/ protocol/ --type=go
rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip)\b[^.]{0,40}$' crypto/ drops/ keylet/ protocol/ --type=go
rg -nP '^\s*\w[^=]*=\s*[^/]+//\s*\w+\s*$' crypto/ drops/ keylet/ protocol/ --type=go
```

- [ ] **Step 3: W3 — review and prune**

For each hit, `Read` and apply preservation list. Default-keep on anything referencing RFC numbers, DER, canonicality, or rippled signature behavior.

- [ ] **Step 4: W4 — apply edits**

Use `Edit`. Comment lines only.

- [ ] **Step 5: W5 — build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 6: W6 — test**

```bash
go test ./crypto/... ./drops/... ./keylet/... ./protocol/...
```
Expected: PASS.

- [ ] **Step 7: W7 — format**

```bash
goimports -l $(git diff --name-only | rg '\.go$')
```
Expected: empty.

- [ ] **Step 8: W8 — preservation audit**

```bash
git diff --unified=0 | rg -i 'reference:|mirrors rippled|matches rippled|see rippled|amendment|invariant|RFC|DER|canonical'
```
Expected: empty. Any hit → restore and re-audit.

- [ ] **Step 9: W9 — commit**

```bash
git add -u crypto/ drops/ keylet/ protocol/
git commit -m "$(cat <<'EOF'
chore(crypto,drops,keylet,protocol): prune non-informative comments

Removes low-value comments (headers, echoes, stale markers) across the
foundation packages. No rippled references, RFC citations, DER/crypto
algorithm notes, or canonicality invariants touched.
EOF
)"
```

---

## Task 3: ledger/entry/, shamap/, storage/

Ledger object definitions and state storage. SLE (Serialized Ledger Entry) field comments are often load-bearing — they document on-wire layout and amendment-gated fields.

**Files:** `ledger/entry/**/*.go`, `shamap/**/*.go`, `storage/**/*.go`. Skip `doc.go`, generated files, `testutil/mock_*.go`.

- [ ] **Step 1: W1 — build baseline**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: W2 — enumerate candidates**

```bash
rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,})' ledger/ shamap/ storage/ --type=go
rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b' ledger/ shamap/ storage/ --type=go
rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip)\b[^.]{0,40}$' ledger/ shamap/ storage/ --type=go
rg -nP '^\s*\w[^=]*=\s*[^/]+//\s*\w+\s*$' ledger/ shamap/ storage/ --type=go
```

- [ ] **Step 3: W3 — review and prune**

Default-keep on anything referencing SLE fields, amendment gates (e.g., `fixMPT`, `featureClawback`), or `ltXxx` ledger-type codes.

- [ ] **Step 4: W4 — apply edits**

Use `Edit`.

- [ ] **Step 5: W5 — build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 6: W6 — test**

```bash
go test ./ledger/... ./shamap/... ./storage/...
```
Expected: PASS.

- [ ] **Step 7: W7 — format**

```bash
goimports -l $(git diff --name-only | rg '\.go$')
```
Expected: empty.

- [ ] **Step 8: W8 — preservation audit**

```bash
git diff --unified=0 | rg -i 'reference:|mirrors rippled|matches rippled|see rippled|amendment|invariant|SLE|ledger entry|lt[A-Z][a-z]|fix[A-Z]|feature[A-Z]'
```
Expected: empty.

- [ ] **Step 9: W9 — commit**

```bash
git add -u ledger/ shamap/ storage/
git commit -m "$(cat <<'EOF'
chore(ledger,shamap,storage): prune non-informative comments

Removes low-value comments (headers, echoes, stale markers) across
ledger entries, SHAMap, and storage backends. No rippled references,
SLE field specs, amendment gates, or invariant notes touched.
EOF
)"
```

---

## Task 4: amendment/, config/

Amendment registry and configuration. Every amendment-name comment is load-bearing by definition of this cleanup — when in doubt, keep.

**Files:** `amendment/**/*.go`, `config/**/*.go`. Skip `doc.go`, generated files.

- [ ] **Step 1: W1 — build baseline**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: W2 — enumerate candidates**

```bash
rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,})' amendment/ config/ --type=go
rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b' amendment/ config/ --type=go
rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip)\b[^.]{0,40}$' amendment/ config/ --type=go
rg -nP '^\s*\w[^=]*=\s*[^/]+//\s*\w+\s*$' amendment/ config/ --type=go
```

- [ ] **Step 3: W3 — review and prune**

In `amendment/registry.go` specifically, trailing comments on amendment-constant lines that restate the amendment name *are still load-bearing* (they are the human-readable amendment label). Keep them. Only remove where the comment adds pure noise unrelated to the amendment.

- [ ] **Step 4: W4 — apply edits**

Use `Edit`.

- [ ] **Step 5: W5 — build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 6: W6 — test**

```bash
go test ./amendment/... ./config/...
```
Expected: PASS.

- [ ] **Step 7: W7 — format**

```bash
goimports -l $(git diff --name-only | rg '\.go$')
```
Expected: empty.

- [ ] **Step 8: W8 — preservation audit**

```bash
git diff --unified=0 | rg -i 'reference:|mirrors rippled|matches rippled|see rippled|amendment|fix[A-Z]|feature[A-Z]'
```
Expected: empty.

- [ ] **Step 9: W9 — commit**

```bash
git add -u amendment/ config/
git commit -m "$(cat <<'EOF'
chore(amendment,config): prune non-informative comments

Removes low-value comments (headers, echoes, stale markers). Amendment
names and their human-readable labels preserved; no rippled references
or feature-gate notes touched.
EOF
)"
```

---

## Task 5: internal/rpc/

Largest surface area in the tree. Many methods are skeletons with TODO markers — these TODOs are live (they describe real unimplemented work) and must be preserved.

**Files:** `internal/rpc/**/*.go` excluding `*_test.go`. Skip `doc.go`, generated files.

- [ ] **Step 1: W1 — build baseline**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: W2 — enumerate candidates**

```bash
rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,})' internal/rpc/ --type=go -g '!*_test.go'
rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b' internal/rpc/ --type=go -g '!*_test.go' | head -100
rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip)\b[^.]{0,40}$' internal/rpc/ --type=go -g '!*_test.go'
rg -nP '^\s*\w[^=]*=\s*[^/]+//\s*\w+\s*$' internal/rpc/ --type=go -g '!*_test.go'
```

- [ ] **Step 3: W3 — review and prune**

TODOs in this package are mostly live ("implement X using Y store"). Default-keep. Remove only TODOs that reference work already done elsewhere (grep the referenced symbol; if it exists and is used, the TODO is stale).

- [ ] **Step 4: W4 — apply edits**

Use `Edit`.

- [ ] **Step 5: W5 — build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 6: W6 — test**

```bash
go test ./internal/rpc/...
```
Expected: PASS.

- [ ] **Step 7: W7 — format**

```bash
goimports -l $(git diff --name-only | rg '\.go$')
```
Expected: empty.

- [ ] **Step 8: W8 — preservation audit**

```bash
git diff --unified=0 | rg -i 'reference:|mirrors rippled|matches rippled|see rippled|amendment|invariant|TODO'
```
Expected: empty. A `TODO` line in the diff's *removed* side must be justified by a verified-done check.

- [ ] **Step 9: W9 — commit**

```bash
git add -u internal/rpc/
git commit -m "$(cat <<'EOF'
chore(rpc): prune non-informative comments

Removes low-value comments (headers, echoes, stale markers) across
internal/rpc/. Live TODOs describing unimplemented work preserved;
no rippled references or protocol notes touched.
EOF
)"
```

---

## Task 6: internal/tx/ per-transaction-type subpackages

Twenty-four transaction-type subpackages. Every per-tx preflight/preclaim/apply flow carries load-bearing rippled and invariant references — default to keep.

**Paths (do one sub-group per commit, not all at once):**
- 6a: `account/`, `amm/`, `batch/`, `check/`
- 6b: `clawback/`, `credential/`, `delegate/`, `depositpreauth/`
- 6c: `did/`, `escrow/`, `ledgerstatefix/`, `mpt/`
- 6d: `nftoken/`, `offer/`, `oracle/`, `paychan/`
- 6e: `payment/`, `permissioneddomain/`, `pseudo/`, `signerlist/`
- 6f: `ticket/`, `trustset/`, `vault/`, `xchain/`

For each sub-group (6a through 6f), **run Steps 1–9 end-to-end, producing one commit per sub-group**. Do not batch sub-groups into a single commit. Six commits total for Task 6.

- [ ] **Step 1: W1 — build baseline**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: W2 — enumerate candidates for this sub-group**

Replace `<subpkgs>` with the space-separated paths of the current sub-group (e.g., `internal/tx/account/ internal/tx/amm/ internal/tx/batch/ internal/tx/check/` for 6a):

```bash
SUBPKGS="<subpkgs>"
rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,})' $SUBPKGS --type=go -g '!*_test.go'
rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b' $SUBPKGS --type=go -g '!*_test.go'
rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip)\b[^.]{0,40}$' $SUBPKGS --type=go -g '!*_test.go'
rg -nP '^\s*\w[^=]*=\s*[^/]+//\s*\w+\s*$' $SUBPKGS --type=go -g '!*_test.go'
```

- [ ] **Step 3: W3 — review and prune**

These packages have the densest rippled cross-references in the tree. Every `// Reference:` and every comment near a `Preflight`, `Preclaim`, or `Apply` body is load-bearing by default. Only touch stylistic separators, truly obvious what-comments, and echoes.

- [ ] **Step 4: W4 — apply edits**

Use `Edit`.

- [ ] **Step 5: W5 — build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 6: W6 — test**

```bash
go test $(echo $SUBPKGS | sed 's#\([^ ]*\)#./\1...#g')
```
Replace `$SUBPKGS` with the literal sub-group path list if the shell does not expand it. Expected: PASS.

- [ ] **Step 7: W7 — format**

```bash
goimports -l $(git diff --name-only | rg '\.go$')
```
Expected: empty.

- [ ] **Step 8: W8 — preservation audit**

```bash
git diff --unified=0 | rg -i 'reference:|mirrors rippled|matches rippled|see rippled|amendment|invariant|preflight|preclaim|TER|fix[A-Z]|feature[A-Z]'
```
Expected: empty.

- [ ] **Step 9: W9 — commit**

```bash
git add -u internal/tx/
git commit -m "$(cat <<'EOF'
chore(tx/<sub-group-label>): prune non-informative comments

Removes low-value comments across <listed sub-packages>. No rippled
references, amendment gates, invariant notes, preflight/preclaim
commentary, or TER code references touched.
EOF
)"
```

Replace `<sub-group-label>` with e.g. `account,amm,batch,check` and list the sub-packages explicitly. Six commits total for Task 6.

---

## Task 7: internal/tx/ top-level files

The highest-risk files in the tree. `engine.go` is 2,471 lines and heavy on invariants; `apply_state_table.go` is the ledger-state mutator; `signature.go` is signing/DER. Default to preserve — this task is expected to remove the fewest comments per line of code.

**Files:** `internal/tx/*.go` (non-`_test.go`, non-`doc.go`). This includes: `apply_context.go`, `apply_state_table.go`, `asset.go`, `block_processor.go`, `engine.go`, `field_metadata.go`, `flags.go`, `flatten.go`, `invariants_adapter.go`, `owner_count.go`, `parse.go`, `registry.go`, `result.go`, `serialize.go`, `signature.go`, `threading.go`, `transaction.go`, `types.go`, `utils.go`, `validate.go`.

- [ ] **Step 1: W1 — build baseline**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: W2 — enumerate candidates**

```bash
rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,})' internal/tx/*.go
rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b' internal/tx/*.go
rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip)\b[^.]{0,40}$' internal/tx/*.go
rg -nP '^\s*\w[^=]*=\s*[^/]+//\s*\w+\s*$' internal/tx/*.go
```

- [ ] **Step 3: W3 — review and prune — extra caution**

`engine.go`, `apply_state_table.go`, and `signature.go`: read every candidate in context (`Read` with a ±10-line window). These files interleave protocol-critical invariants with non-obvious invariants unique to the Go port. When in doubt, keep.

Candidates most likely to be legitimate removals here:
- Pure separator lines (`// ====`) at section boundaries.
- `types.go` const echoes (e.g., `TypePayment Type = 0 // ttPAYMENT` style).

- [ ] **Step 4: W4 — apply edits**

Use `Edit`. One file at a time, in the order `types.go` → `flags.go` → `result.go` → `utils.go` → remaining files (lowest risk first).

- [ ] **Step 5: W5 — build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 6: W6 — test**

```bash
go test ./internal/tx/... ./internal/testing/...
```
Expected: PASS. This pulls in conformance/invariants tests that exercise `engine.go` and `apply_state_table.go`.

- [ ] **Step 7: W7 — format**

```bash
goimports -l $(git diff --name-only | rg '\.go$')
```
Expected: empty.

- [ ] **Step 8: W8 — preservation audit**

```bash
git diff --unified=0 | rg -i 'reference:|mirrors rippled|matches rippled|see rippled|amendment|invariant|preflight|preclaim|TER|DER|canonical|fix[A-Z]|feature[A-Z]'
```
Expected: empty.

- [ ] **Step 9: W9 — commit**

```bash
git add -u internal/tx/
git commit -m "$(cat <<'EOF'
chore(tx): prune non-informative comments at tx package top level

Removes low-value comments (headers, echoes, stale markers) from
engine.go, apply_state_table.go, signature.go, types.go and other
top-level tx files. No rippled references, invariants, amendment
gates, or DER/canonicality notes touched.
EOF
)"
```

---

## Task 8: internal/consensus/, internal/txq/

Consensus protocol (csf/, rcl/) and transaction queue. Consensus commentary frequently cites the XRPL whitepaper or rippled's `Consensus.cpp` — preserve these by default.

**Files:** `internal/consensus/**/*.go`, `internal/txq/**/*.go`, non-test, non-`doc.go`.

- [ ] **Step 1: W1 — build baseline**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: W2 — enumerate candidates**

```bash
rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,})' internal/consensus/ internal/txq/ --type=go -g '!*_test.go'
rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b' internal/consensus/ internal/txq/ --type=go -g '!*_test.go'
rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip)\b[^.]{0,40}$' internal/consensus/ internal/txq/ --type=go -g '!*_test.go'
rg -nP '^\s*\w[^=]*=\s*[^/]+//\s*\w+\s*$' internal/consensus/ internal/txq/ --type=go -g '!*_test.go'
```

- [ ] **Step 3: W3 — review and prune**

Preserve anything referencing the consensus protocol, Byzantine/quorum/UNL/manifest terminology, or a `Consensus.cpp`/`RCLConsensus.cpp` reference.

- [ ] **Step 4: W4 — apply edits**

Use `Edit`.

- [ ] **Step 5: W5 — build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 6: W6 — test**

```bash
go test ./internal/consensus/... ./internal/txq/...
```
Expected: PASS.

- [ ] **Step 7: W7 — format**

```bash
goimports -l $(git diff --name-only | rg '\.go$')
```
Expected: empty.

- [ ] **Step 8: W8 — preservation audit**

```bash
git diff --unified=0 | rg -i 'reference:|mirrors rippled|matches rippled|see rippled|amendment|invariant|consensus|quorum|UNL|validator|manifest'
```
Expected: empty.

- [ ] **Step 9: W9 — commit**

```bash
git add -u internal/consensus/ internal/txq/
git commit -m "$(cat <<'EOF'
chore(consensus,txq): prune non-informative comments

Removes low-value comments (headers, echoes, stale markers) from
consensus and txq. Consensus-protocol terminology, validator/UNL
references, and rippled cross-references preserved.
EOF
)"
```

---

## Task 9: internal/testing/ (test framework and helpers)

Test framework, assertions, env, conformance runner, and per-feature test suites. These files are production code for the test framework and ad-hoc comments for individual tests.

**Files:** `internal/testing/**/*.go` (both test and non-test files in this tree). Skip `doc.go`, generated files.

- [ ] **Step 1: W1 — build baseline**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: W2 — enumerate candidates**

```bash
rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,})' internal/testing/ --type=go
rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b' internal/testing/ --type=go
rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip|Verify|Assert|Expect)\b[^.]{0,40}$' internal/testing/ --type=go
rg -nP '^\s*\w[^=]*=\s*[^/]+//\s*\w+\s*$' internal/testing/ --type=go
```

- [ ] **Step 3: W3 — review and prune**

Test cases occasionally have `// This tests the scenario where …` comments describing the test intent — those are valuable and not candidates. Candidates here are headers inside long test files and echoes like `setUp := …  // setup`.

- [ ] **Step 4: W4 — apply edits**

Use `Edit`.

- [ ] **Step 5: W5 — build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 6: W6 — test**

```bash
go test ./internal/testing/...
```
Expected: PASS.

- [ ] **Step 7: W7 — format**

```bash
goimports -l $(git diff --name-only | rg '\.go$')
```
Expected: empty.

- [ ] **Step 8: W8 — preservation audit**

```bash
git diff --unified=0 | rg -i 'reference:|mirrors rippled|matches rippled|see rippled|amendment|invariant|scenario|regression'
```
Expected: empty.

- [ ] **Step 9: W9 — commit**

```bash
git add -u internal/testing/
git commit -m "$(cat <<'EOF'
chore(testing): prune non-informative comments

Removes low-value comments (headers, echoes, stale markers) from the
test framework and per-feature test suites. Scenario/regression-intent
comments preserved; no rippled references or invariant notes touched.
EOF
)"
```

---

## Task 10: `*_test.go` sweep for anything not yet covered

Any `*_test.go` files outside the packages already swept (in particular top-level tests that live alongside production code, e.g., `internal/tx/*_test.go`, `crypto/*_test.go`).

**Files:** all remaining `*_test.go` files. Enumerate explicitly in Step 2 so nothing is double-committed.

- [ ] **Step 1: W1 — build baseline**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: W2 — enumerate remaining test files**

```bash
git ls-files '*_test.go' > /tmp/all-tests.txt
git log --name-only --pretty=format: | rg '_test\.go$' | sort -u > /tmp/touched-tests.txt
comm -23 <(sort -u /tmp/all-tests.txt) /tmp/touched-tests.txt > /tmp/remaining-tests.txt
wc -l /tmp/remaining-tests.txt
```

(The intent is a list of `*_test.go` files under `goXRPL/` that have not yet been modified by an earlier task in this plan. If the shell does not produce a usable list, enumerate manually: for each directory not listed in Tasks 1–9, `find <dir> -name '*_test.go'`.)

Then run the category greps limited to the files in `/tmp/remaining-tests.txt`:

```bash
xargs -a /tmp/remaining-tests.txt rg -nP '^\s*//\s*(={3,}|-{3,}|\*{3,})'
xargs -a /tmp/remaining-tests.txt rg -nP '^\s*//\s*(TODO|FIXME|XXX)\b'
xargs -a /tmp/remaining-tests.txt rg -nP '^\s*//\s*(Return|Returns|Loop|Iterate|Increment|Decrement|Set|Get|Check|Call|Create|Initialize|Add|Remove|Delete|Update|Print|Log|Skip|Verify|Assert|Expect)\b[^.]{0,40}$'
```

- [ ] **Step 3: W3 — review and prune**

Scenario-intent comments stay. Otherwise apply the standard rules.

- [ ] **Step 4: W4 — apply edits**

Use `Edit`. If the remaining list spans many directories, split into per-directory commits.

- [ ] **Step 5: W5 — build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 6: W6 — test**

```bash
go test ./...
```
Expected: PASS (modulo baseline known-fails).

- [ ] **Step 7: W7 — format**

```bash
goimports -l $(git diff --name-only | rg '\.go$')
```
Expected: empty.

- [ ] **Step 8: W8 — preservation audit**

```bash
git diff --unified=0 | rg -i 'reference:|mirrors rippled|matches rippled|see rippled|amendment|invariant|scenario|regression'
```
Expected: empty.

- [ ] **Step 9: W9 — commit**

```bash
git add -u
git commit -m "$(cat <<'EOF'
chore(tests): prune non-informative comments in remaining test files

Tree-wide *_test.go sweep for files not covered by earlier commits.
Scenario/regression-intent comments preserved.
EOF
)"
```

---

## Task 11: Final verification

After all package-group commits land, verify the tree is healthy end-to-end and run the conformance summary to confirm no regression in protocol conformance.

- [ ] **Step 1: Full build**

```bash
go build ./...
```
Expected: PASS.

- [ ] **Step 2: Full test**

```bash
go test ./...
```
Expected: PASS (modulo the baseline known-fails from Task 0 Step 3).

- [ ] **Step 3: Conformance summary**

```bash
./scripts/conformance-summary.sh
```
Expected: pass/fail counts match baseline. If any previously-passing suite now fails, the likely cause is a comment removal that accidentally took a code line with it (or the `Edit` tool matched an unexpected string). `git bisect` against the 10 commits to localize.

- [ ] **Step 4: Count the damage**

```bash
FIRST=$(git log --grep='prune non-informative' --reverse --pretty=format:'%H' | head -1)
git log --grep='prune non-informative' --pretty=format:'%h %s'
git diff --stat ${FIRST}^..HEAD -- '*.go'
```

Report total lines removed, files touched, commit count. Add this as the final line of the plan's review section (below).

- [ ] **Step 5: Final audit across all commits**

```bash
FIRST=$(git log --grep='prune non-informative' --reverse --pretty=format:'%H' | head -1)
git log ${FIRST}^..HEAD --grep='prune non-informative' --patch | rg -i 'reference:|mirrors rippled|matches rippled|see rippled'
```
Expected: empty. Any hit here means a preserved comment slipped through and needs restoration.

---

## Review

- Total commits: 16 (plus the two spec/plan doc commits on main)
- Files touched: 209
- Lines added: 28 (gofmt re-alignments after trailing-comment removal, plus 2 oracle godoc rewrites that preserve the rippled cross-reference on the new line)
- Lines removed: 1638
- Net: -1610 lines
- Packages affected: codec, crypto, drops, keylet, protocol, ledger/entry, shamap, storage, amendment, config, internal/rpc, all 24 internal/tx/ subpackages, internal/tx/ top-level, internal/consensus, internal/testing, internal/txq, tree-wide *_test.go
- Preservation audit final check: PASS. Net rippled-reference content unchanged; the two oracle godoc edits move "matches rippled's DeleteOracle::preflight()" / "matches rippled's SetOracle::preflight()" onto the godoc summary line rather than a separate trailing line.
- Regressions: zero. Package-level fail list on this branch is identical to baseline main at `173ebf9` (notably `codec/binarycodec/types TestSerializeXrpAmount` panic and the tx-subpackage failures already present on main).

### Commits (newest → oldest)

1. `7a9ace0` chore(tests): prune non-informative comments in *_test.go files
2. `7c84258` chore(txq): prune non-informative comments
3. `fe697a0` style(tx): remove residual godoc-restatement comments from all tx subpackages
4. `60e6010` chore(testing): prune non-informative comments in testing helpers
5. `1b257dc` chore(consensus): prune non-informative comments in adaptor package
6. `6948c62` chore(tx): prune non-informative comments in top-level tx package
7. `1d9c809` chore(tx/payment,permissioneddomain,pseudo,signerlist,ticket,trustset,vault,xchain): prune non-informative comments
8. `879a12d` chore(tx/nftoken,offer,oracle,paychan): prune non-informative comments
9. `eb8257b` chore(tx/did,escrow,ledgerstatefix,mpt): prune non-informative comments
10. `1c5b74b` chore(tx/clawback,credential,delegate,depositpreauth): prune non-informative comments
11. `74d421c` chore(tx/account,amm,batch,check): prune non-informative comments
12. `a379ad4` chore(rpc): prune non-informative comments
13. `eb375af` chore(amendment,config): prune non-informative comments
14. `f325500` chore(ledger,shamap,storage): prune non-informative comments
15. `373d266` chore(crypto,drops,keylet,protocol): prune non-informative comments
16. `7f4ef8b` chore(codec): prune non-informative comments

No commits were reverted. One indentation bug introduced mid-run by a subagent in `internal/txq/apply.go` was caught and corrected before the txq commit landed.
