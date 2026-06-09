# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`go-xrpl` (module `github.com/LeJamon/go-xrpl`) is an idiomatic Go implementation of
an XRP Ledger (XRPL) node. It is **not** a line-by-line port of the C++ reference
implementation, [rippled](https://github.com/XRPLF/rippled); it is a native Go
implementation that follows Go conventions and concurrency patterns while
maintaining behavioral parity with the XRPL protocol.

### Implementation Philosophy

- **Rippled is the source of truth.** There is no formal XRPL specification, so the
  C++ rippled implementation (kept locally at `../rippled`, read-only) is the de
  facto spec. Before changing protocol behavior, check the corresponding rippled
  behavior first. Match rippled's validation logic and error codes (TER codes).
- **Idiomatic Go.** Prefer Go interfaces, composition, and table-driven designs over
  transliterated C++ idioms (templates, RAII, deep inheritance).
- **Simplicity first.** Make every change as small as possible; impact minimal code.
- **Production quality.** No simplified versions, no temporary fixes — find root
  causes.

## Build and Test Commands

A `justfile` consolidates the toolchain (CGO + OpenSSL env vars, test groupings
matching CI, conformance harness). Install `just` with `brew install just`. **All
`just` recipes run from the repository root.**

Two subsystems use CGO and link OpenSSL / libsecp256k1 via `pkg-config`. On macOS:
`brew install openssl@3 secp256k1 pkg-config`; on Debian/Ubuntu: `libssl-dev
libsecp256k1-dev`. The justfile auto-resolves Homebrew's openssl@3 path.
`CGO_ENABLED=0` builds work but cannot peer (peertls returns
`ErrSessionSigUnsupported`) and use the slower pure-Go secp256k1 verify.

```bash
just                 # discover recipes
just build           # CGO + OpenSSL → ../tmp/main
just build-all       # full module compile
just build-nocgo     # verify the !cgo peertls stub still builds

just test            # everything
just test-integration   # ./internal/testing/...
just test-tx            # ./internal/tx/...
just test-core          # ledger / txq / rpc / consensus / peermanagement
just test-libs          # codec / crypto / shamap / storage / ...
just test-pkg ./internal/tx/offer/...                  # one package
just test-pkg './internal/tx/payment/... -run TestX'   # one test (quote args)

just vet
just lint            # auto-installs golangci-lint at the CI-pinned version
just fmt
just tidy

just conformance               # full suite with per-suite breakdown
just conformance TxQ           # filter by suite name
just conformance --failing     # only suites with failures

just run             # plain `go run ./cmd/xrpld`
just dev             # hot reload (needs `air`)
```

### Raw commands (no `just`)

```bash
go build -o ./tmp/main ./cmd/xrpld        # build
go test ./...                              # all tests
go test ./internal/tx/offer/...            # one package
./scripts/conformance-summary.sh           # conformance summary
```

The server exposes JSON-RPC at `http://localhost:8080/`, WebSocket subscriptions at
`ws://localhost:8080/ws`, and a health check at `http://localhost:8080/health`.

## Architecture

### Public packages (exported, usable by external consumers)

- `amendment/` — Amendment/feature registry and rules
- `codec/` — Encoding/decoding subsystem
  - `addresscodec/` — Address encoding/decoding
  - `binarycodec/` — Binary codec for XRPL data types
- `config/` — Configuration system
- `crypto/` — Cryptographic operations (secp256k1, ed25519); `common/` has SHA512-Half
- `drops/` — XRP amount utilities
- `keylet/` — Ledger object key derivation
- `ledger/entry/` — Serializable Ledger Entries (SLE) for all object types (40+ types)
- `protocol/` — Protocol constants
- `shamap/` — SHA-512 tree map for ledger state hashing
- `storage/` — Persistence layer: `kvstore/` (memorydb, pebble), `nodestore/`
  (blockchain state), `relationaldb/` (PostgreSQL/SQLite)

### Internal packages

- `cmd/xrpld/` — CLI entry point (Cobra)
- `internal/cli/` — CLI commands (server, rpc, compare) and root command wiring
- `internal/replaytool/` — offline `replay`/`replay-range` developer commands
  (mainnet/fixture replay and state-divergence reporting; distinct from the
  node's production inbound-ledger replay)
- `internal/tx/` — Transaction engine, types, and processing
  - One subpackage per tx type: `account/`, `amm/`, `batch/`, `check/`, `clawback/`,
    `credential/`, `delegate/`, `depositpreauth/`, `did/`, `escrow/`,
    `ledgerstatefix/`, `mpt/`, `nftoken/`, `offer/`, `oracle/`, `paychan/`,
    `payment/`, `permissioneddomain/`, `pseudo/`, `signerlist/`, `ticket/`,
    `trustset/`, `vault/`, `xchain/`
  - `all/` — registry that imports all tx subpackages
- `internal/ledger/` — Ledger management (`genesis/`, `header/`, `manager/`,
  `service/`, `state/`, `store/`)
- `internal/consensus/` — Consensus protocol (`csf/` simulation framework, `rcl/`)
- `internal/txq/` — Transaction queue
- `internal/rpc/` — JSON-RPC server with 60+ methods (`handlers/`, `types/`) and
  WebSocket subscriptions
- `internal/grpc/` — gRPC server
- `internal/peermanagement/` — Peer networking (peertls handshake is CGO/OpenSSL)
- `internal/testing/` — Test framework and conformance suites (one dir per feature)
- `internal/statecompare/` — State comparison utilities

### Transaction engine flow

Transactions flow through `Validate()` → `Preflight()` → `Preclaim()` → `Apply()`:

1. **Validate** — structural validation (well-formed fields, valid types)
2. **Preflight** — context-free checks (flags, field constraints)
3. **Preclaim** — ledger-aware checks (account exists, sufficient balance)
4. **Apply** — execute against ledger state

The engine in `internal/tx/engine.go` orchestrates validation and applies
transactions to ledger state. Transaction types self-register via `init()` +
`tx.Register()` in their subpackages.

## Rippled reference locations

Use the local `rippled/` tree (do not fetch from the web):

- Transaction implementations: `rippled/src/xrpld/app/tx/detail/`
- Transaction headers: `rippled/src/xrpld/app/tx/`
- Ledger objects: `rippled/src/xrpld/ledger/detail/`
- Protocol definitions: `rippled/src/libxrpl/protocol/`
- Tests: `rippled/src/test/app/` (e.g. `AccountSet_test.cpp`, `Offer_test.cpp`)

When implementing or fixing a feature: look up the rippled implementation and its
unit tests, then implement matching Go tests under
`internal/testing/<feature>/`.

## Implementation patterns

- Serialize: Go struct → JSON map → `binarycodec.Encode()` → hex → bytes.
- UInt64 fields in the binary codec use HEX strings, **except** `sMD_BaseTen`
  fields (`MPTAmount`, `OutstandingAmount`, `MaximumAmount`, `LockedAmount`) which
  are DECIMAL — see `definitions.IsBaseTenUInt64FieldName`.
- Use prefixed error messages matching rippled's TER codes (e.g.
  `temBAD_OFFER: ...`).

## Comments

Only add comments when strictly required. Do not comment code to explain trivial
things.
