# Architecture Decision Records

An Architecture Decision Record (ADR) captures a significant design decision: the
context that forced a choice, the decision taken, and its consequences. They are
immutable once accepted — a later decision supersedes an earlier one with a new
record rather than editing history.

| # | Decision | Status |
|---|----------|--------|
| [0001](0001-rippled-as-specification.md) | rippled as the specification | Accepted |
| [0002](0002-native-go-not-a-port.md) | Native Go implementation, not a line-by-line port | Accepted |
| [0003](0003-single-writer-engine.md) | Single-writer transaction engine | Accepted |
| [0004](0004-storage-architecture.md) | Content-addressed node store + relational indexes | Accepted |
| [0005](0005-cgo-for-crypto-and-tls.md) | CGO for peer TLS and secp256k1, with a pure-Go fallback | Accepted |
