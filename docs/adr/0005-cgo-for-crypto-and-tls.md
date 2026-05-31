# 0005 — CGO for peer TLS and secp256k1, with a pure-Go fallback

## Status

Accepted.

## Context

Two subsystems have requirements the pure-Go ecosystem does not fully meet:

- The XRPL peer handshake derives a session-signature shared value from the TLS
  finished messages (`SSL_get_finished` / `SSL_get_peer_finished`). Go's
  `crypto/tls` does not expose those, so matching rippled's handshake requires
  OpenSSL.
- secp256k1 ECDSA verification is on the consensus hot path; a C library
  (libsecp256k1) is materially faster than the pure-Go implementation.

But requiring a C toolchain for every build would shut out contributors who only
want to work on RPC, codec, or transaction logic.

## Decision

Use CGO for those two subsystems — OpenSSL for `peertls`, libsecp256k1 for
hot-path verification — while keeping a pure-Go fallback under `CGO_ENABLED=0`.
The fallback builds a fully functional node minus peering: `peertls` returns
`ErrSessionSigUnsupported`, signature verification uses the slower pure-Go path
(~6× per verify), and RPC, WebSocket, transactions, codec, and storage work
unchanged.

## Consequences

- Production/networked builds need the OpenSSL and libsecp256k1 development
  headers (see [../operating.md](../operating.md#build-requirements)).
- Contributors without a CGO toolchain can still build, test, and run a
  non-peering node with `CGO_ENABLED=0`.
- The build matrix must cover both paths; `just build-nocgo` guards the fallback.
