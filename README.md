# go-xrpl

[![Go Report Card](https://goreportcard.com/badge/github.com/LeJamon/goXRPLd)](https://goreportcard.com/report/github.com/LeJamon/goXRPLd)

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

`goxrpl` uses CGO to call OpenSSL for the peer-to-peer TLS handshake — required to compute the
session-signature shared value matching rippled's `SSL_get_finished` / `SSL_get_peer_finished`
flow. You need OpenSSL development headers installed on the build host.

### macOS

```bash
brew install openssl@3 pkg-config
export PKG_CONFIG_PATH="$(brew --prefix openssl@3)/lib/pkgconfig"
go build ./cmd/xrpld
```

### Ubuntu / Debian

```bash
sudo apt install -y libssl-dev pkg-config
go build ./cmd/xrpld
```

### Alpine (or static-linked Linux build)

```bash
apk add --no-cache gcc musl-dev pkgconf openssl-dev openssl-libs-static
CGO_ENABLED=1 go build -ldflags="-linkmode external -extldflags '-static'" ./cmd/xrpld
```

### CGO-disabled builds

`CGO_ENABLED=0 go build ./cmd/xrpld` is supported. The resulting binary cannot
connect to or accept peers (peertls returns `ErrSessionSigUnsupported`), but RPC,
WebSocket, tx, codec, and all other subsystems work unchanged. Useful for contributors
without an OpenSSL toolchain.

### Running interop tests

A docker-based interop test against a real rippled instance lives at
`internal/peermanagement/peertls/tls_interop_test.go`. It is gated by a build tag and
an env var so CI never runs it:

```bash
PEERTLS_DOCKER_INTEROP=1 go test -tags 'docker' \
    ./internal/peermanagement/peertls/ \
    -run TestHandshake_Interop_RippledDocker
```

## Architecture

```
cmd/xrpld/             CLI entry point (Cobra)

── Public packages ──────────────────────────────
amendment/             Amendment/feature registry
codec/                 Binary & address encoding
  addresscodec/          Address encode/decode
  binarycodec/           XRPL binary serialization
config/                Configuration
crypto/                ED25519, secp256k1, SHA-512 Half
drops/                 XRP amount utilities
keylet/                Ledger object key derivation
ledger/entry/          Serializable Ledger Entries (40+ types)
protocol/              Protocol constants
shamap/                SHAMap (SHA-512 tree) for state hashing
storage/               Persistence layer
  kvstore/               KV interface (memory, Pebble)
  nodestore/             Blockchain state storage
  relationaldb/          PostgreSQL

── Internal packages ────────────────────────────
internal/tx/           Transaction engine & processing
  engine.go              Validate → Preflight → Preclaim → Apply
  account/  amm/  batch/  check/  clawback/  credential/
  delegate/  depositpreauth/  did/  escrow/  ledgerstatefix/
  mpt/  nftoken/  offer/  oracle/  paychan/  payment/
  permissioneddomain/  pseudo/  signerlist/  ticket/
  trustset/  vault/  xchain/
  invariants/            Transaction invariant checks
internal/ledger/       Ledger management
  genesis/  header/  manager/  service/  state/  store/
internal/consensus/    Consensus protocol
  csf/                   Consensus Simulation Framework
  rcl/                   Ripple Consensus Ledger
internal/txq/          Transaction queue
internal/rpc/          JSON-RPC server (60+ methods)
  handlers/              Per-method handler implementations
internal/grpc/         gRPC server
internal/peermanagement/  Peer networking
internal/testing/      Test suites (one directory per feature)
```

### Transaction Flow

Every transaction follows the same pipeline:

1. **Validate** — Structural validation (well-formed fields, valid types)
2. **Preflight** — Context-free checks (flags, field constraints)
3. **Preclaim** — Ledger-aware checks (account exists, sufficient balance)
4. **Apply** — Execute against ledger state

Transaction types self-register via `init()` + `tx.Register()` in their respective subpackages.

## Current Status

### What works

The client currently targets **standalone mode** (single-node, no network peers), with **rippled v2.6.2** as the first release target.

- **26 transaction types** — Full pipeline (validate through apply) with behavioral parity to rippled
- **60+ RPC methods** — JSON-RPC 2.0 and WebSocket interfaces
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

## Contributing

Contributions are welcome. The general workflow:

1. Pick a transaction type, RPC method, or test gap
2. Check the corresponding rippled implementation in `rippled/src/xrpld/app/tx/detail/`
3. Implement or fix the Go equivalent, matching rippled's behavior
4. Add or update tests in `internal/testing/<feature>/`
5. Run `go test ./...` and the conformance summary

When in doubt about expected behavior, rippled is the source of truth.

## License

ISC License — see [LICENSE](LICENSE) for details.
