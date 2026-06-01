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

The local `rippled/` tree is the working reference. Do not fetch from the web — use
the local copy:

| What you need | Where it lives in `rippled/` |
|---------------|------------------------------|
| Transaction logic | `src/xrpld/app/tx/detail/` |
| Transaction headers | `src/xrpld/app/tx/` |
| Ledger objects | `src/xrpld/ledger/detail/` |
| Protocol definitions | `src/libxrpl/protocol/` |
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
   Transaction types live under `internal/tx/<type>/` and self-register via
   `init()` + `tx.Register()`.
4. **Run the build, tests, and lint** (below) until green, plus the conformance
   summary for the area you touched.

Cite the rippled source you followed in a code comment where the behavior is
subtle — it lets reviewers check the port against the original.

## Build, test, lint

The [`justfile`](justfile) is the toolchain entrypoint; all recipes run from the
repository root. See [docs/operating.md](docs/operating.md#build-requirements) for
the CGO/OpenSSL prerequisites.

```bash
just build            # build the xrpld binary (CGO + OpenSSL)
just build-all        # compile every package
just build-nocgo      # verify the CGO_ENABLED=0 path still builds

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
