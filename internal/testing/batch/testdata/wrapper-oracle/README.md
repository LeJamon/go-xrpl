# Batch wrapper oracle fixtures

These 22 cases come from the eleven-wrapper regression in private rippled 3.4.1,
commit `d147fccf54a500fce586522f28d6044c37fd8d29` in `XRPLF/xrpld-private`.
Each wrapper is exercised with `fixBatchV1_2` disabled and enabled, while
`BatchV1_1` is enabled.

`recorder.patch` instruments only the existing C++ test. It enables signature
verification, constructs and signs each Batch once, submits its binary blob
through the actual submit RPC, and records the response and the parent and
resulting closed ledgers. The original result and delivery assertions still run.
No production C++ source is changed.

Each JSON file contains the signed transaction, raw submit response, serialized
ledger headers, effective amendment rules, fees, every state entry, and every
transaction and metadata blob. The C++ test environment supplies amendment
presets through configuration, so the effective rules are recorded explicitly
and supplied to Go without changing either ledger snapshot.
The Go test reconstructs and verifies both snapshots, submits the same signed
bytes with signature verification enabled, and compares the result, charged
fee, complete state, metadata, transaction roots, and ledger hashes. Rejected
cases must leave the open view unchanged. Close time and close flags are the
recorded consensus inputs.

To regenerate, apply `git apply --unidiff-zero recorder.patch` in a disposable
copy of the exact revision,
build `xrpld` with tests enabled and its locked dependencies, then run:

```sh
GOXRPL_BATCH_FIXTURE_DIR=/absolute/path/to/fixtures xrpld --unittest=Batch
```

The unit test must exit successfully and produce all 22 JSON files. Copy those
files here and run:

```sh
go test -race -count=1 ./internal/testing/batch -run '^TestBatchWrapperOracleFixtures$'
```

`GOXRPL_BATCH_ORACLE_DIR` can select a freshly exported directory instead of this
committed fixture directory. Without the override, these fixtures are mandatory
in the normal test suite.

## Recorded run

- Source: private rippled tag `3.4.1`, commit `d147fccf54a500fce586522f28d6044c37fd8d29`.
- Host/toolchain: macOS arm64, Apple Clang 17.0.0, Conan 2.18.1, CMake 4.1.2, Ninja 1.13.0; Release build, tests enabled.
- Binary SHA-256: `f05b910157c5e3a416ee723332ce2fbd83cf7e3c02d4a61af09060871e3d6113`.
- Conan lock SHA-256: `9d5e382cce56445d65694ed3add13b0bccea58ff9d4e2aad65ac3c9c54089bc5`.
- C++ result: 506 assertions, zero failures; 22 fixture files.
- Go result: all 22 cases passed with the race detector, including exact serialized ledger, state, and transaction/metadata comparisons.

The executable reports `xrpld version 3.4.1`. It was built from an archive of the
pinned commit, so the version output has no embedded Git hash. Only the test
recorder differs from that archive. The explicit `xrpld` target built
successfully; the default all-target build encountered an unrelated benchmark's
use of `std::ranges::iota`, unavailable in this Apple standard library.
