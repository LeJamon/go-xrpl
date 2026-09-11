# 0007 — Native libsecp256k1 for all secp256k1 operations

## Status

Accepted. Supersedes the secp256k1 fallback decision in ADR 0005.

## Context

The daemon already required CGO for peer TLS and used libsecp256k1 for signature
verification. Key derivation, signing, and public-key operations still used two
separate Go secp256k1 implementations. Keeping those paths split meant that
production behavior and performance depended on which operation was called, and
allowed a CGO-disabled build to use a different secp256k1 implementation.

The confidential MPT backend also uses the XRPLF native package, whose locked
dependency graph contains libsecp256k1. Mixing that package with a separately
resolved system library could put two native secp256k1 copies in one process.

## Decision

Route every secp256k1 operation through the project C shim and native
libsecp256k1. The shim owns native context setup and exposes byte-oriented
operations to Go; protocol hashing, encoding, and orchestration remain in Go.
The secp256k1 package is CGO-only and has no pure-Go fallback.

The optional `mptcrypto` build uses the locked Conan graph and resolves its
libsecp256k1 dependency to the same native package as the project shim.

Portable no-CGO checks may continue for leaves whose production and test
dependency graph avoids addresscodec: Ed25519, SHA-512Half, RFC 1751, drops,
protocol, amendments, SHAMap, and the key-value/node stores. Binarycodec,
relational storage, keylet, and ledger packages reach the native shim
transitively.

## Consequences

- The daemon needs OpenSSL for peer TLS and libsecp256k1 for secp256k1
  operations; `pkg-config` locates both. Secp256k1 package consumers require
  CGO, libsecp256k1, and `pkg-config`.
- `CGO_ENABLED=0` cannot build the daemon or packages that use secp256k1.
- CI tests native secp256k1 on both supported operating systems, including race
  and cgo pointer checks, while retaining no-CGO coverage for portable leaves.
