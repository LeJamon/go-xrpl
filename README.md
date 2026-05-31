# go-xrpl

[![Go Report Card](https://goreportcard.com/badge/github.com/LeJamon/go-xrpl)](https://goreportcard.com/report/github.com/LeJamon/go-xrpl)

An idiomatic Go implementation of an [XRP Ledger](https://xrpl.org/) node.

go-xrpl is not a line-by-line port of [rippled](https://github.com/XRPLF/rippled) (the C++ reference implementation). It is a native Go implementation that follows Go conventions and concurrency patterns while maintaining full protocol compatibility with the XRP Ledger network. rippled serves as the de facto specification — there is no formal XRPL spec — so behavioral parity with rippled is the correctness bar.

> **Status: actively developed, building in public.** Core transaction processing, ledger state management, and RPC are functional. See [Current Status](#current-status) for details.

## Getting Started

### Prerequisites

- Go 1.24+
- PostgreSQL (optional, for relational storage)

### Build

```bash
go build -o ./tmp/main ./cmd/xrpld
```

### Run

```bash
# Start the node
./tmp/main

# Or with hot reload during development
cd cmd/xrpld && air
```

The server exposes:
- `http://localhost:8080/` — JSON-RPC 2.0
- `ws://localhost:8080/ws` — WebSocket subscriptions
- `http://localhost:8080/health` — Health check

### Test

```bash
# All tests
go test ./...

# Specific transaction type
go test ./internal/tx/offer/...

# Specific test suite
go test ./internal/testing/amm/...

# Single test
go test ./internal/testing/offer/... -run TestOfferCreateValidation

# Conformance summary
./scripts/conformance-summary.sh
./scripts/conformance-summary.sh --failing
```

## Building

`goxrpl` uses CGO for two subsystems — **OpenSSL** (peer TLS handshake) and
**libsecp256k1** (hot-path ECDSA verification, with a pure-Go fallback under
`CGO_ENABLED=0`). Install the development headers, then build:

```bash
# macOS
brew install openssl@3 secp256k1 pkg-config
export PKG_CONFIG_PATH="$(brew --prefix openssl@3)/lib/pkgconfig:$(brew --prefix secp256k1)/lib/pkgconfig"
# Debian/Ubuntu: sudo apt install -y libssl-dev libsecp256k1-dev pkg-config

go build ./cmd/xrpld        # or: just build
```

See **[docs/operating.md](docs/operating.md)** for static/Alpine builds, the
`CGO_ENABLED=0` path, the full `xrpld.toml` configuration reference, and running a
node.

## Architecture

goXRPL is organized into importable public packages (codec, crypto, keylet,
shamap, ledger entries, storage, …) and internal subsystems (the transaction
engine, ledger lifecycle, consensus, RPC, peer networking). Every transaction
flows through the same four-stage pipeline — **Validate → Preflight → Preclaim →
Apply** — orchestrated by `internal/tx/engine.go`; types self-register via
`init()` + `tx.Register()`.

See **[docs/architecture.md](docs/architecture.md)** for the full package map,
the pipeline in detail, the ledger close flow, the consensus split (`rcl` real
vs `csf` simulation), and storage layering. The per-package API reference is on
[pkg.go.dev](https://pkg.go.dev/github.com/LeJamon/go-xrpl).

## Current Status

### What works

The client currently targets **standalone mode** (single-node, no network peers), with **rippled v2.6.2** as the first release target.

- **24 transaction families (66 transaction types)** — Full pipeline (validate through apply) with behavioral parity to rippled
- **70+ RPC methods** — JSON-RPC 2.0 and WebSocket interfaces
- **Ledger state** — SHAMap-backed state tree with Pebble storage
- **Pathfinding** — DFS-based path discovery matching rippled's algorithm
- **Codec** — Full binary serialization/deserialization
- **Cryptography** — ED25519 and secp256k1 signing/verification
- **34 test suites** — Conformance tests validating behavior against rippled

### What's in progress

- **Consensus** — CSF and RCL implementations exist but are not yet tested
- Peer-to-peer networking
- Full ledger sync / history
- WebSocket `path_find` subscriptions
- Admin authentication

## Design Decisions

**Why Go?** Go's concurrency model (goroutines, channels) is a natural fit for a blockchain node that juggles peer connections, transaction processing, consensus rounds, and RPC serving concurrently. The language's simplicity and strong standard library reduce the surface area for bugs in critical financial infrastructure.

**Why not a direct port?** rippled's C++ idioms (templates, RAII, complex inheritance hierarchies) don't translate well to Go. Instead, go-xrpl uses Go interfaces, composition, and table-driven designs while preserving the same protocol semantics. The result is more readable and maintainable while remaining behaviorally equivalent.

**rippled as spec.** Every transaction type, ledger entry, and edge case is validated against rippled's behavior. The local `rippled/` source tree is the reference for any ambiguity.

## Documentation

| Doc | For |
|-----|-----|
| [pkg.go.dev/github.com/LeJamon/go-xrpl](https://pkg.go.dev/github.com/LeJamon/go-xrpl) | Library consumers — full API reference |
| [docs/architecture.md](docs/architecture.md) | How the node is structured and how transactions flow |
| [docs/operating.md](docs/operating.md) | Node operators — building, running, and the `xrpld.toml` reference |
| [docs/conformance.md](docs/conformance.md) | How rippled-parity is verified and the conformance suite |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Contributors — the implement-against-rippled workflow |

The [docs/](docs/) directory also carries generated catalogs (RPC methods,
supported transactions, amendments, conformance status).

## Contributing

Contributions are welcome — most work means porting a piece of rippled behavior
into idiomatic Go while preserving exact protocol semantics. See
**[CONTRIBUTING.md](CONTRIBUTING.md)** for the workflow, the rippled reference
locations, the test layout, and the build/test/lint commands. When in doubt about
expected behavior, rippled is the source of truth.

## License

ISC License — see [LICENSE](LICENSE) for details.
