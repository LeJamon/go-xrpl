# API stability

go-xrpl is two things: a node (`goxrpl`) and a set of Go packages other programs
can import. This page says what each of them promises.

## What is public

The importable API is every package outside `internal/`:

`amendment`, `codec`, `config`, `crypto`, `drops`, `keylet`, `ledger/entry`,
`log`, `protocol`, `shamap`, `storage`, `version`.

Everything under `internal/` (the transaction engine, ledger service, consensus,
peer networking, RPC) is private to this module. The Go toolchain refuses to
import it from outside, and it changes without notice. A lint rule keeps public
packages from depending on it.

`cmd/` and `scripts/` are programs and tooling, not API.

## Two version numbers

- **Module version** — the Go module's git tag (`vX.Y.Z`), which is what
  `go get` resolves. It describes the Go API of the public packages.
- **Software version** — `version.SemanticVersion`, the string the node
  advertises to peers and in `server_info`. It has its own release cycle and is
  not a Go module version.

The two are independent. The rippled release whose behaviour the node tracks is a
third thing again; see [conformance.md](conformance.md).

## Compatibility promise

The module is pre-1.0. While it is tagged `v0.y.z`:

- A **patch** release (`v0.y.Z`) does not remove or change exported identifiers
  in the public packages.
- A **minor** release (`v0.Y.0`) may break the public API. Breaking changes are
  listed in the release notes.
- Untagged commits on `main` carry no promise; pin a tag.

Protocol behaviour is outside this promise. Serialization, hashing and
validation follow the XRP Ledger protocol, and a public function changes its
output whenever the protocol requires it.

A `v1.0.0` tag will mean the standard Go module contract: no breaking change
without a new major version and module path.

## How changes are checked

CI runs `scripts/apidiff.sh` on every pull request and writes the result to the
job summary, split into compatible and incompatible changes. The job reports; it
does not block. Run it locally with:

```bash
just api-diff              # against origin/main
just api-diff v0.1.0       # against a tag
```

An incompatible change is acceptable before 1.0 when it is deliberate: say so in
the pull request so it reaches the release notes.
