# Contributing to go-xrpl

go-xrpl is a native Go implementation of an XRP Ledger node. Contributions are
welcome — most work means porting a piece of [rippled](https://github.com/XRPLF/rippled)
behavior into idiomatic Go while preserving exact protocol semantics.

Before diving in, skim [docs/architecture.md](docs/architecture.md) for how the
node fits together and [docs/conformance.md](docs/conformance.md) for how parity is
verified.

## The core principle: rippled is the spec

There is no formal XRP Ledger specification. rippled's observable behavior *is*
the spec, so the correctness bar for any change is **"does this behave the way
rippled behaves?"** — same field ordering, same TER result codes, same state
mutations, same edge cases. When in doubt about expected behavior, check rippled.

For the source inventory tests in `just test`, use the public
[`XRPLF/rippled`](https://github.com/XRPLF/rippled) tag `3.4.1`, whose peeled
commit is `00e640751460c137636fdb6dae6b4280de355d81`. No private-repository
access is required. From the go-xrpl repository root:

```bash
git clone --depth 1 --branch 3.4.1 --single-branch https://github.com/XRPLF/rippled.git ../rippled-worktrees/v3.4.1-oracle
just test
```

The tests discover `rippled-worktrees/v3.4.1-oracle/` in the repository or an
ancestor directory. To use a checkout elsewhere, run
`GOXRPL_ORACLE_DIR=/absolute/path/to/rippled just test`. Keep the checkout clean,
including untracked files; no rippled build is needed for these tests.

Each inventory input must match its pinned SHA-256 from private 3.4.1 commit
`d147fccf54a500fce586522f28d6044c37fd8d29`; the tests log that content
equivalence. The private checkout and the automatically discovered public
3.4.0 fallback remain supported. This equivalence applies only to the pinned
inventory files, not to all release behavior. The private commit remains the
provenance of the recorded behavioral fixtures.

Use the local checkout for source inspection:

| What you need | Where it lives in `rippled-worktrees/v3.4.1-oracle/` |
|---------------|------------------------------|
| Transaction logic | `src/libxrpl/tx/transactors/` |
| Transaction headers | `include/xrpl/tx/transactors/` |
| Ledger implementation and helpers | `src/libxrpl/ledger/` |
| Protocol definitions | `src/libxrpl/protocol/` and `include/xrpl/protocol/` |
| Upstream unit tests | `src/test/app/` (e.g. `AccountSet_test.cpp`, `Offer_test.cpp`) |

## The implement-against-rippled loop

When implementing or fixing a feature:

1. **Find the rippled implementation** of the transaction type, ledger entry, or
   RPC method in the tables above, and read its **unit tests** in
   `src/test/app/` — those tests enumerate the behavior you must match.
2. **Implement the Go equivalent**, following Go idioms (interfaces, composition,
   table-driven code) rather than transliterating C++. Match rippled's validation
   logic and error codes; use prefixed error messages that match rippled's TER
   codes (e.g. `temBAD_OFFER: ...`).
3. **Mirror the tests** in Go under `internal/testing/<feature>/` — one directory
   per feature (e.g. `internal/testing/offer/`, `internal/testing/amm/`).
   Transaction types live under `internal/tx/<type>/`. Each declares one
   `tx.Descriptor` (constructor, delegation, privileges, queue consequences,
   Batch and reserve-sponsor eligibility) in its package's `Register()` via
   `tx.Register()`; `internal/tx/all.RegisterAll()` calls every package's
   `Register()` explicitly at startup. A new type also needs its code and name
   in `protocol/transaction_type.go`, its field template in
   `internal/tx/template.go`, and its wire definition in `definitions.json`.
4. **Run the build, tests, and lint** (below) until green, plus the conformance
   summary for the area you touched.

Cite the rippled source you followed in a code comment where the behavior is
subtle — it lets reviewers check the port against the original.

## Build, test, lint

The [`justfile`](justfile) is the toolchain entrypoint; all recipes run from the
repository root. See [docs/operating.md](docs/operating.md#build-requirements) for
the CGO/OpenSSL prerequisites.

```bash
just build            # build the goxrpl binary (CGO + OpenSSL)
just build-all        # compile every package

just test             # all tests
just test-tx          # transaction-engine tests (./internal/tx/...)
just test-integration # conformance/integration suites (./internal/testing/...)
just test-core        # ledger / txq / rpc / consensus / peermanagement
just test-libs        # codec / crypto / shamap / storage / ...
just test-pkg ./internal/tx/offer/...                  # one package
just test-pkg './internal/tx/payment/... -run TestX'   # one test (quote the args)

just vet              # go vet
just lint             # golangci-lint at the CI-pinned version (auto-installs)
just fmt              # gofmt -w the module

just conformance              # rippled-parity summary (see docs/conformance.md)
just conformance --failing    # only suites with failures
```

A change should build, pass the relevant tests, and pass `just lint` (which now
enforces doc comments on exported symbols in the public packages) before you open
a PR.

## Submitting changes

1. Branch from `main`, make focused commits.
2. Use [Conventional Commits](https://www.conventionalcommits.org/) prefixes
   (`feat:`, `fix:`, `docs:`, `test:`, `chore:`, `refactor:`).
3. **Do not mention yourself or any tooling in commit messages** — keep them about
   the change.
4. Open a PR against `main` describing what changed and which rippled behavior it
   matches. If the change touches protocol behavior, note the rippled source and
   the conformance suite that covers it.

## Documentation

Exported symbols in public packages must carry doc comments (enforced by
`just lint`). When adding a public package or symbol, document it; when adding an
RPC method, transaction type, or amendment, the generated catalogs
(`just docs-gen`) will pick it up. Prose guides live in [docs/](docs/).
