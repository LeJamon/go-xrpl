# 0006 — CGO required for the goxrpl daemon

## Status

Accepted. Supersedes [ADR 0005](0005-cgo-for-crypto-and-tls.md) for daemon
builds.

## Context

The XRPL peer handshake requires OpenSSL access to TLS finished messages, and
production signature verification uses libsecp256k1 on the consensus hot path.
A no-CGO binary can run only by substituting incomplete networking behavior and
a different cryptographic backend. Publishing that binary as a node makes the
supported deployment boundary unclear.

The no-CGO implementations remain useful to consumers of individual Go packages
and for focused cross-backend tests.

## Decision

Require CGO for `cmd/goxrpl`. The supported daemon always includes the OpenSSL
peer TLS and libsecp256k1 verification shims. No build recipe or release path
produces a pure-Go daemon.

Individual libraries may retain no-CGO implementations, but those packages do
not compose into a supported node executable.

## Consequences

- Building `cmd/goxrpl` with `CGO_ENABLED=0` is intentionally unsupported.
- Node builders need the OpenSSL and libsecp256k1 development headers described
  in the [operator guide](../operating.md#build-requirements).
- CI may continue testing no-CGO library implementations without advertising a
  pure-Go node build.
