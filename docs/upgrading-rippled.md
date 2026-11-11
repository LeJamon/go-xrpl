# Upgrading to a new rippled release

How to move go-xrpl from the rippled release it targets to the next one. The
per-release evidence logs ([release-3.4.1.md](release-3.4.1.md),
[v3.3.0.md](v3.3.0.md)) record what one upgrade found; this page is the
procedure. Work through the steps in order: each one ends with the test that
says it is complete, and later steps assume the earlier ones are green.

## The pin

[`internal/testutil/rippled/pin.env`](../internal/testutil/rippled/pin.env) is
the only place that names the targeted release: tag, private and public
commits, checkout directory, release package, oracle image tag, and the
version-named fixture locations. Go tests read it as `rippled.Pin`, CI through
`./.github/actions/oracle-pin`, and the scripts, generators and `justfile`
parse the file directly.

`TestPinnedIdentityIsNotDuplicated` fails if the pinned commits, the checkout
or corpus directory names, or (in code) the tag are hard-coded anywhere outside
its allow-list. `scripts/peer-interop/Dockerfile` cannot read the pin, so
`TestOracleDockerfileMatchesPin` compares its literals with it instead.

## Prerequisites

- Read access to `XRPLF/xrpld-private` for the release tag. The public
  `XRPLF/rippled` tag is enough for the inventory tests only; the fixture
  generators reject a checkout whose `origin` is not the private repository.
- The CGO toolchain from [operating.md](operating.md#build-requirements), `just`,
  `jq`, Python 3, Docker (interop), and `protoc` 34.1 with
  `protoc-gen-go@v1.36.11` if the peer protocol changed.
- For re-recording the conformance corpus: a C++ toolchain able to build
  rippled (Conan, CMake, Ninja). Replay needs none of it.

Check out the new release next to the repository, clean and at the tag. `NEW`
is the new tag, `OLD` the current `ORACLE_TAG`:

```sh
git clone --branch "$NEW" git@github.com:XRPLF/xrpld-private.git ../rippled-worktrees/v$NEW-oracle
git -C ../rippled-worktrees/v$NEW-oracle rev-parse HEAD      # ORACLE_COMMIT
git ls-remote https://github.com/XRPLF/rippled.git "refs/tags/$NEW^{}"   # ORACLE_PUBLIC_COMMIT
git -C ../rippled-worktrees/v$NEW-oracle diff --name-only "$OLD" "$NEW"
```

The last command is the work list. Give every file a disposition in a new
`docs/release-<NEW>.md`, modelled on the "Complete upstream release delta"
table in release-3.4.1.md.

## 1. Bump the pin

Manual. Edit `pin.env`:

- `ORACLE_TAG`, `ORACLE_COMMIT`, `ORACLE_PUBLIC_COMMIT`, `ORACLE_CHECKOUT`,
  `ORACLE_IMAGE`, `ORACLE_PACKAGE_URL`.
- `ORACLE_PRIOR_*`: the release you are leaving. Tests fall back to its
  checkout only for the hash-pinned inventory files (step 2).
- `ORACLE_PACKAGE_SHA256`, `ORACLE_BINARY_SHA256`: step 9, once the package is
  published.
- `ORACLE_CORPUS`, `ORACLE_FINAL_OUTCOMES`, `ORACLE_AMENDMENT_REGISTRY`,
  `ORACLE_DEFINITIONS_*`: updated by the steps that regenerate those artefacts.
  New artefacts go in new version-named paths; do not rename the old ones in
  the same change.

Complete when `go test ./internal/testutil/rippled -run 'TestPin'` passes.
Expect most other oracle tests to fail until the steps below are done; that is
the remaining work list.

## 2. Protocol inventory sources

Manual. `sourceHashes` in `internal/testutil/rippled/source.go` pins the SHA-256
of the seven oracle files the inventory tests parse. Recompute each one that
changed:

```sh
shasum -a 256 ../rippled-worktrees/v$NEW-oracle/include/xrpl/protocol/detail/sfields.macro
```

Complete when `go test ./internal/testutil/rippled` passes.

## 3. Definitions

`codec/binarycodec/definitions/definitions.json` is edited by hand; there is no
generator. Diff these oracle files against the previous tag and mirror every
change (new fields, types, transaction and ledger-entry type codes, results):

- `include/xrpl/protocol/SField.h`, `include/xrpl/protocol/detail/sfields.macro`
- `include/xrpl/protocol/detail/transactions.macro`, `ledger_entries.macro`
- `include/xrpl/protocol/TER.h`

Then update the expected counts in
`codec/binarycodec/definitions/final_inventory_test.go` and, for new result
codes, `internal/tx/ter/result.go`.

Complete when these pass:

```sh
just test-pkg './codec/binarycodec/definitions -run TestFinalDefinitionsMatchRippled'
just test-pkg './internal/tx/ter -run TestResultDefinitionsLockstep'
```

## 4. `server_definitions`

The response tables in `internal/rpc/handlers/server_definitions_tables.go` are
manual: diff the formats and flags in `src/libxrpl/protocol/TxFormats.cpp`,
`LedgerFormats.cpp`, and `include/xrpl/protocol/TxFlags.h` / `LedgerFormats.h`.
The expected hashes are generated from the private checkout:

```sh
python3 internal/rpc/testdata/server_definitions_final_oracle.py \
  "$(cd ../rippled-worktrees/v$NEW-oracle && pwd)" \
  --output internal/rpc/testdata/server_definitions_final_hashes.json
```

Copy the printed `FULL_DOCUMENT` length and hash into
`ORACLE_DEFINITIONS_BYTES` and `ORACLE_DEFINITIONS_SHA512_HALF`. If the release
changed how the document is built, the generator needs the same change.

Complete when `just test-pkg './internal/rpc -run ServerDefinitions'` passes;
the per-section hashes in the fixture show which table is wrong.

## 5. Amendments

Generate the oracle's registry snapshot, then point `ORACLE_AMENDMENT_REGISTRY`
at the new file:

```sh
python3 amendment/testdata/generate_private3_4_1_registry.py \
  ../rippled-worktrees/v$NEW-oracle --output amendment/testdata/private${NEW}_registry.json
```

Then, manually, in `amendment/registry.go`: add each new `XRPL_FEATURE` /
`XRPL_FIX` with the oracle's `Supported` and `VoteBehavior`, and move each
`XRPL_RETIRE_*` entry to `registerRetired`. Retiring an amendment also means
deleting the pre-amendment code path it guarded; grep for its `Feature…`
constant. Update `privateV341FeatureSHA256` in `amendment/final_oracle_test.go`
to the fixture's `oracle.sha256`, and `privateV341GoOnlyFeatures` if the set of
Go-only registrations changed.

Complete when `just test-pkg ./amendment/...` passes
(`TestPrivateV341FeatureRegistry`, `TestPrivateV341DefaultVotingAcrossProfiles`).
`go run ./scripts/amendment-watch` optionally compares the registry with a live
network.

## 6. Transactions, ledger entries, regeneration

Manual, guided by the release delta. Follow
[CONTRIBUTING.md](../CONTRIBUTING.md#the-implement-against-rippled-loop) for
each new or changed transactor: `protocol/transaction_type.go`, a package under
`internal/tx/<type>/` registered through `internal/tx/all`, and mirrored tests
under `internal/testing/<feature>/`. Ledger entries are declared in
`ledger/entry/schema` and generated:

```sh
go generate ./ledger/entry/... ./internal/peermanagement/proto
```

The second path regenerates `xrpl.pb.go` and is only needed when the oracle's
`include/xrpl/proto/xrpl.proto` changed. Mirror that diff by hand into
`internal/peermanagement/proto/xrpl.proto` first; the Go file is not a verbatim
copy.

Complete when the CI `generate` job's check is clean and the drift tests pass:

```sh
git diff --exit-code -- ledger/entry internal/peermanagement/proto/xrpl.pb.go
just test-pkg './ledger/entry/...'
```

`TestSpecCoversRippledMacro`, `TestSerializationStylesMatchRippledTag` and
`TestLedgerSpecificFlags` are the oracle comparisons.

## 7. Fixtures and the conformance corpus

Requires the private checkout and a rippled build. Commit the recorder sources
in `scripts/oracle/` first (the manifest binds them to a commit); update
`oracle_tag` / `oracle_commit` in `strict-corpus-config.json` and the matching
constants in `strict_recorder.cpp`. Then record into a new directory:

```sh
python3 scripts/oracle/record-v4.py --clean-build --record \
  --oracle ../rippled-worktrees/v$NEW-oracle \
  --build-root /tmp/rippled-$NEW-recorder \
  --fixture-dir internal/testing/conformance/testdata/rippled-$NEW-v4
```

Write `manifest.json` with `python3 scripts/recorded-corpus-manifest.py`
(`--help` lists the required recorder commit, binary, build identity, config,
recorder sources and coverage limits), add a README modelled on the current
corpus's, and set `ORACLE_CORPUS`. The amendment profile matrix
(`pinnedProfile` in `internal/testing/conformance/pinned_corpus.go`) is
release-specific: revise it if the release's gating amendments differ.

Smaller recorded fixtures assert the pinned commit and must be re-recorded
against the new release. Each directory documents or carries its recorder:
`ORACLE_FINAL_OUTCOMES`, `internal/testing/batch/testdata/wrapper-oracle/`,
`internal/tx/payment/testdata/`, `internal/tx/vault/testdata/send_assets_oracle/`.
`internal/testing/enginefuzz` freezes a named amendment profile and its
fingerprint; rename and re-fingerprint it for the new release.

Finally refresh the coverage inventory:

```sh
python3 scripts/engine-coverage/generate.py --oracle ../rippled-worktrees/v$NEW-oracle
```

Complete when `python3 scripts/engine-coverage/generate.py --oracle … --check`,
`python3 -B scripts/engine-coverage/generate_test.py`,
`python3 -B scripts/oracle/record-v4_test.py` and
`go test ./internal/testing/conformance ./internal/testing/batch/...` pass.

## 8. Conformance triage

```sh
just conformance              # runs ORACLE_CORPUS from the pin
just conformance --failing
just conformance --list-fail
```

A failure is either a Go bug (fix it against the oracle source) or a recording
problem (fix the recorder and re-record; never edit fixture inputs or
expectations). [conformance.md](conformance.md) describes what a pass proves.
Complete when the summary reports zero failed, skipped and excluded cases, as
the CI `conformance-final` job asserts.

## 9. Interop images

Manual, once the release package is published:

```sh
curl -fsSLO "$(sed -n 's/^ORACLE_PACKAGE_URL=//p' internal/testutil/rippled/pin.env)"
shasum -a 256 xrpld_*_amd64.deb                     # ORACLE_PACKAGE_SHA256
```

Put the same URL and checksum in `scripts/peer-interop/Dockerfile`. For the
binary hash, extract the package the way the Dockerfile does (`dpkg-deb -x`,
on Linux or in an `ubuntu:22.04` container) and take `sha256sum` of
`usr/bin/xrpld`; set it in the Dockerfile and `ORACLE_BINARY_SHA256`.

`just test-docker` builds the image, verifies binary, version line and commit
through `scripts/acceptance/oracle-image.sh`, and runs the live peer tests. The
`consensus-smoke` CI matrix and the default in
`internal/peermanagement/handshake_interop_test.go` name older public releases
on purpose (backward interop); add the release you are leaving there if it
should stay covered, and its producer name to `required_producers` in
`scripts/acceptance/final-evidence.sh` and the lists in
`internal/testing/conformance/acceptance_evidence_test.go`.

Complete when `go test ./internal/testutil/rippled` (Dockerfile matches the
pin) and `just test-docker` pass.

## 10. Generated docs

```sh
just docs-check      # fails if the registry-derived catalogs are stale
just docs-gen        # also reruns the corpus for docs/conformance-status.md
```

Commit the regenerated catalogs. Prose that names the release (`README.md`,
`CONTRIBUTING.md`, `CLAUDE.md`, `docs/conformance.md`,
`docs/engine-coverage.md`) is updated by hand; the guard does not cover prose.

## 11. Release evidence

Finish `docs/release-<NEW>.md`: oracle identity, the release-delta table, the
definitions and amendment results from steps 3–5, and the closure checklist.
The three CI job names that mention the release and the `branches:` trigger in
`.github/workflows/ci.yml` are literals; rename them here.

Before pushing, run `just build-all`, `just test`, `just vet` and
`golangci-lint run --config .golangci.yml ./...`. Complete when the
`Release candidate evidence` job is green on the pushed commit. It aggregates
every producer through `scripts/acceptance/final-evidence.sh aggregate` and
rejects mixed oracle identities, so a pin that disagrees with any recorded
artefact fails there.
