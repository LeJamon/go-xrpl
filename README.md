# go-xrpl

Go implementation of the [XRP Ledger](https://xrpl.org/) protocol.

[![API Reference](https://pkg.go.dev/badge/github.com/LeJamon/go-xrpl.svg)](https://pkg.go.dev/github.com/LeJamon/go-xrpl)
[![Go Report Card](https://goreportcard.com/badge/github.com/LeJamon/go-xrpl)](https://goreportcard.com/report/github.com/LeJamon/go-xrpl)
[![CI](https://github.com/LeJamon/go-xrpl/actions/workflows/ci.yml/badge.svg)](https://github.com/LeJamon/go-xrpl/actions/workflows/ci.yml)

go-xrpl is an XRPL client written in Go. It provides the `goxrpl` node daemon,
an importable set of protocol libraries, and developer tools for working with
ledger data. The client can participate in the peer network, process
transactions, maintain ledger state, take part in consensus, and expose
JSON-RPC, WebSocket, and optional gRPC services.

The release branch targets private **rippled v3.4.1**. [Release sign-off](docs/release-3.4.1.md)
remains open until the merged candidate passes every required evidence gate.
go-xrpl has its own architecture and software version policy.

> [!IMPORTANT]
> go-xrpl is under active development. Review the
> [operator guide](docs/operating.md) and current generated capability catalogs
> before deploying it in production.

## Building from source

Building `goxrpl` requires Go 1.24 or later, a C compiler, OpenSSL 3,
libsecp256k1, and `pkg-config`. The examples use
[`just`](https://just.systems/) as the task runner, with a raw Go command shown
for environments where it is not installed.

On macOS:

```shell
brew install openssl@3 secp256k1 pkg-config just
export PKG_CONFIG_PATH="$(brew --prefix openssl@3)/lib/pkgconfig:$(brew --prefix secp256k1)/lib/pkgconfig"
```

On Debian or Ubuntu:

```shell
sudo apt install -y build-essential libssl-dev libsecp256k1-dev pkg-config
```

Build the node from the repository root:

```shell
just build

# Without just
go build -o ../tmp/goxrpl ./cmd/goxrpl
```

The binary is written to `../tmp/goxrpl`. To compile every package without
producing the node binary, run `just build-all`.

The daemon requires CGO. Its peer TLS handshake uses the OpenSSL C shim, and all
secp256k1 operations — key derivation, signing, public-key operations, and
verification — use the libsecp256k1 C shim. There is no supported pure-Go
secp256k1 backend. `CGO_ENABLED=0` remains useful only for leaves whose
production and test dependency graph avoids addresscodec, including Ed25519,
SHA-512Half, RFC 1751, drops, protocol, amendments, SHAMap, the key-value/node
stores, and binarycodec definitions, serdes, and decimal internals. Binarycodec,
relational storage, keylet, and ledger packages reach the native shim
transitively.

## Executable

The repository builds one executable, `goxrpl`, with the following primary
commands:

| Command | Description |
|---------|-------------|
| `goxrpl server` | Run an XRPL node. This is also the default command. |
| `goxrpl generate-config` | Generate a complete configuration for Mainnet, Testnet, or Devnet. |
| `goxrpl rpc` | Send JSON-RPC requests to a running node. |
| `goxrpl replay` | Replay ledger fixtures for deterministic state comparison. |
| `goxrpl replay-range` | Replay a range of ledgers. |
| `goxrpl compare` | Compare two ledger-state dumps. |
| `goxrpl version` | Print version and build information. |

Run `../tmp/goxrpl --help` or `../tmp/goxrpl <command> --help` for the complete
command-line reference.

## Running a node

Generate a configuration first:

```shell
../tmp/goxrpl generate-config --network main --output goxrpl.toml
```

Start a networked node and acquire its initial ledger from peers:

```shell
../tmp/goxrpl server --conf goxrpl.toml --net
```

For a local standalone node instead:

```shell
../tmp/goxrpl server --conf goxrpl.toml --standalone --start
```

The configuration controls peer discovery, validation, storage, logging, and
the listening addresses for JSON-RPC, WebSocket, gRPC, and peer traffic. See the
[operator guide](docs/operating.md) for startup modes, endpoint security,
storage backends, validator configuration, and the full configuration reference.

Accurate host time is required for peer connections. Run a synchronized system
time service before joining a network.

## Go packages

go-xrpl also exposes reusable Go packages for applications and tooling:

- `amendment` — amendment registry and rules
- `codec` — XRPL address and binary encoding
- `crypto` — Ed25519, secp256k1, and protocol hashing
- `drops` — XRP amount handling
- `keylet` — ledger key derivation
- `ledger/entry` — serialized ledger entry types
- `protocol` — protocol constants and primitives
- `shamap` — authenticated ledger state maps
- `storage` — node and relational storage backends

Internal node services include transaction execution, the transaction queue,
ledger acquisition and lifecycle, consensus, peer networking, JSON-RPC,
WebSocket subscriptions, and gRPC. API documentation for public packages is
available on [pkg.go.dev](https://pkg.go.dev/github.com/LeJamon/go-xrpl).

## Testing

The `justfile` groups local checks in the same way as CI:

```shell
just test             # all Go tests
just test-integration # transaction conformance suites
just test-tx          # transaction engine and handlers
just test-core        # ledger, consensus, RPC, queue, and peer subsystems
just test-libs        # public libraries and storage
just vet
just lint
```

For historical 3.4.0 corpus diagnostics:

```shell
just conformance --corpus /path/to/rippled-3.4.0-v3
just conformance --corpus /path/to/rippled-3.4.0-v3 --failing
```

These legacy commands do not establish private 3.4.1 parity. The required signed
3.4.1 corpus and strict replay are tracked in [#2015](https://github.com/LeJamon/go-xrpl/issues/2015);
see the [release evidence matrix](docs/release-3.4.1.md) for remaining gates.

## Documentation

| Document | Audience |
|----------|----------|
| [Architecture](docs/architecture.md) | Client design, transaction flow, ledger lifecycle, consensus, and storage |
| [Operating a node](docs/operating.md) | Build, configuration, startup, networking, validation, and security |
| [Conformance](docs/conformance.md) | Protocol compatibility methodology and test suite |
| [RPC methods](docs/rpc-methods.md) | Implemented RPC API catalog |
| [Transactions](docs/supported-transactions.md) | Supported transaction types |
| [Amendments](docs/amendments.md) | Amendment support and voting defaults |
| [Ledger entries](docs/ledger-entries.md) | Supported ledger object types |
| [Contributing](CONTRIBUTING.md) | Development workflow and project conventions |

## Contributing

Contributions are welcome. Changes to protocol-visible behavior must preserve
XRPL compatibility, while implementation choices should remain idiomatic Go.
Read [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow, test
layout, and review requirements.

## License

go-xrpl is licensed under the [ISC License](LICENSE).
