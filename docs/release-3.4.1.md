# Private rippled 3.4.1 release evidence

Release sign-off is **open**. This inventory is preparation for issue
[#2017](https://github.com/LeJamon/go-xrpl/issues/2017), not a full-node parity
claim. The final gate must run on the merged `v3.4.1` candidate after its
dependencies land, and the published evidence must name that exact remote SHA.

## Oracle identity

| Property | Value |
| --- | --- |
| Repository | `XRPLF/xrpld-private` |
| Tag | `3.4.1` |
| Peeled commit | `d147fccf54a500fce586522f28d6044c37fd8d29` |
| Read-only local checkout | `rippled-worktrees/v3.4.1-oracle` |
| Release-delta base | `4a4fded2eba11427c48ce3f24d9c1aea5e7a9d17` (`3.4.0`) |
| Go integration baseline inspected | `02f4f17c7d1c1b5676b112ddfa669655bcff4415` |

The local oracle worktree belongs to `xrpld-private/.git`. Before regenerating
evidence, verify its `HEAD`, peeled tag and clean tracked/untracked status.
Neither a binary version string nor a requested container tag establishes its
build provenance. Preserve the binary/image digest, source identity, build
commands and flags, and configuration alongside executed results.

The Go product version is independent: `version.SemanticVersion` remains
`3.4.0`, and `version.Version` is the build identifier supplied through
`-ldflags`. Neither is a claim that every rippled behavior has been verified.

## Complete upstream release delta

The following table accounts for every file returned by
`git diff --name-only 4a4fded2eba11427c48ce3f24d9c1aea5e7a9d17 d147fccf54a500fce586522f28d6044c37fd8d29`
in the private oracle repository.

| Repository path | Disposition |
| --- | --- |
| `include/xrpl/basics/MathUtilities.h` | Checked signed addition/subtraction introduced; runtime consumers in this delta use addition, covered by #2013 / PR #2020 (merged). |
| `include/xrpl/tx/paths/detail/Steps.h` | Checked path aggregation; #2013, `internal/tx/payment/amount.go`. |
| `include/xrpl/tx/paths/detail/StrandFlow.h` | Overflow becomes `tecPATH_DRY`; #2013, `internal/tx/payment/flow.go`. |
| `src/libxrpl/tx/paths/BookStep.cpp` | Checked book totals; #2013, `internal/tx/payment/step_book.go`. |
| `include/xrpl/protocol/detail/features.macro` | `fixBatchV1_2` registration; #2011 / PR #2018, merged. |
| `src/libxrpl/tx/transactors/system/Batch.cpp` | Amendment-gated inner wrapper validation; #2011. |
| `src/test/app/Batch_test.cpp` | Wrapper regression coverage; signed oracle fixtures in `internal/testing/batch/testdata/wrapper-oracle`. |
| `include/xrpl/tx/invariants/InvariantCheck.h` | Wide XRP accumulator; #2012 / PR #2019, merged; Go uses `math/big.Int`. |
| `src/libxrpl/tx/invariants/InvariantCheck.cpp` | Comment-only companion change; no separate behavior. |
| `src/libxrpl/ledger/helpers/TokenHelpers.cpp` | Aggregate XRP/MPT transfer checks; #2014 / PR #2021, merged. |
| `src/libxrpl/protocol/BuildInfo.cpp` | rippled release string only; no Go product-version change. |
| `.github/workflows/reusable-package.yml` | Nexus upload endpoint; no protocol effect and no Go build-system port required. |

The arithmetic and wrapper fixtures prove their recorded cases. Whole-engine
submission, close, replay and bounded generation evidence belongs to #2016;
corpus recording and strict comparison belong to #2015 / PR #2022. None of
these bounded results replaces a final integrated release run.

## Definitions and source inventories

The source-derived server-definitions document is byte-identical between the
two pinned oracle commits: **79,944 bytes**, SHA-512-half
`1EA05B0FC11101F7C500BD0DAC794A8BC746A7FBA6250B75489603EB820E0FF5`.
All ten generated sections match, including transaction/ledger formats and
flags: zero generated definitions/format/flag content changes. The fixture and
generator now identify the private release; these pin updates are shared with
#2015. The verified package's runtime `--definitions` output independently
reproduces the same length and hash. `features.macro` is a separate changed
amendment source.

`amendment/testdata/private3.4.1_registry.json` independently records all 108
oracle registrations (50 active and 58 retired), including IDs, support and
default votes. Its generator requires the clean private checkout and pins
`features.macro` SHA-256
`1708df2ea0150a3105f13f7f39c42c798ac5addae9833c073fe6df9e9e17208b`.
`TestPrivateV341FeatureRegistry` checks every registration and the explicit
Go-only entries under both capability profiles.

The seven source inputs in
[`internal/testutil/rippled/source.go`](../internal/testutil/rippled/source.go)
are also byte-identical. Definitions, ledger flags and schema/style tests now
check their pinned 3.4.1 SHA-256 values. They prefer the clean private checkout;
`GOXRPL_ORACLE_DIR` can select its absolute path explicitly. A selected wrong
or dirty checkout fails instead of falling back. Public CI may read the exact
3.4.0 commit only for these seven hash-verified inputs, logging the limited
content equivalence. This does not authorize behavioral or live interoperability
claims from a 3.4.0 binary. In particular, `features.macro` changed and is not
part of this equivalence list.

Run the inventory checks from the Go repository:

```sh
just test-pkg './amendment ./internal/testutil/rippled ./codec/binarycodec/definitions ./ledger/entry/... -race -count=1'
```

## Active pin inventory

| Surface | Required disposition |
| --- | --- |
| `.github/workflows/ci.yml`, `nightly.yml` | Source checkouts retain the explicit content-equivalence check above. Final peer/consensus jobs and the nightly soak use the checksum-pinned private 3.4.1 binary. |
| `codec/binarycodec/definitions/final_inventory_test.go` | Private 3.4.1 file hashes and clean pinned source selection. |
| `ledger/entry/flags_test.go`, schema drift/style tests | Same verified 3.4.1 source-input contract. |
| `internal/rpc/server_definitions_test.go`, fixture and generator | Generated content unchanged; private provenance verified against source and the package runtime. |
| `scripts/acceptance/final-evidence.sh` | Private identities are pinned, and historical producers are rejected as final evidence. The required `conformance-final` job executes the committed signed v4 corpus and records reconciled counts. |
| `scripts/peer-interop/Dockerfile` | Pin the official 3.4.1 package and extracted binary SHA-256; require an amd64 image. `scripts/acceptance/oracle-image.sh` checks binary/version/commit and records the immutable image ID used by live tests. |
| `justfile` | `test-docker` builds/verifies the pinned private 3.4.1 image and runs peer/manifest, ledger-node and validator-list interop against its immutable ID. `conformance` and `fuzz-differential` use the strict signed 3.4.1 corpus. |
| `CLAUDE.md`, `CONTRIBUTING.md` | Private pinned source is the working reference. |
| `README.md`, `docs/conformance.md` | State the release target, committed corpus contract and bounded evidence. |
| `version/version.go` | Independent Go software version; preserve it. |
| Historical fixtures, release records, market/native-backend notes | Keep original producer identities; inherited evidence needs an explicit current check before contributing to sign-off. |

The published package is
`https://packages.xrplf.org/repository/deb-stable/pool/x/xrpld/xrpld_3.4.1-1_amd64.deb`,
SHA-256 `cae8ce3b9bc9451b19975c890714ba789d2004987cdfff9cbd55522c612c6f26`.
Its `/usr/bin/xrpld` SHA-256 is
`fb8430dfdbee9d8016818dab22881e48a125598beebf9b5b8c90ca7fdba1faaa`.
The live verifier also requires the exact release string and private source
commit at runtime. It records these identities and resolves the image tag to
an immutable ID before any interop test. The package is amd64 even on an arm64
host, so its Docker build explicitly selects `linux/amd64`.

## Supported build profiles

Both daemon profiles require CGO, OpenSSL and native libsecp256k1. The default
build lacks confidential MPT cryptography and reports `ConfidentialTransfer`
unsupported. The `mptcrypto` build tag with CGO links the locked `mpt-crypto` 1.0.2
backend and reports support only when that backend is available. The amendment
defaults to a no vote in both profiles. The production Docker build selects the
native profile and checks the reported capability before completing.

Compared with private 3.4.1, shared amendment IDs and vote metadata match. Go
also retains unsupported `InvariantsV1_1` and the three obsolete NFT feature registrations
`NonFungibleTokensV1`, `fixNFTokenDirV1`, and `fixNFTokenNegOffer` for
compatibility. The default build's unsupported `ConfidentialTransfer` is an
explicit capability limit, not an alternate accepted execution mode. Enabling
an unsupported or unknown amendment must block proposals and validations;
unsupported majority-only entries must not block or receive support votes.

Relevant regression coverage lives in `amendment`,
`internal/consensus/adaptor`, `internal/consensus/rcl`, and `internal/rpc`.
Run it under both default and `mptcrypto` profiles, together with native crypto
and MPT transaction/integration tests. A successful native unit run does not
replace testing the actual shipped daemon/image.

## Final closure checklist

- [ ] Integrate #2015 and #2016 into `v3.4.1`; recheck all sibling and
  historical blockers against that refreshed candidate.
- [ ] Run peer/manifest interop and RPC/submission/consensus smoke against a
  binary or image proven to come from the pinned private oracle.
- [ ] Build the actual release artifacts and exercise each advertised native
  capability profile at the proposal/validation boundary.
- [ ] Collect current required build, vet/lint, transaction/integration/core/
  library tests, targeted race tests and a non-skipped strict differential run.
- [ ] Preserve commands, environment, flags, oracle/artifact identities,
  executed counts, exclusions, seeds and mismatch counts, all bound to the
  exact tested Go SHA. Document unverified surfaces explicitly.
- [ ] After the final merge, refresh the remote candidate identity and verify
  the published evidence names that SHA before closing #2017 or parent #2010.
