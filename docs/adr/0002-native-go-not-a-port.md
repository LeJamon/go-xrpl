# 0002 — Native Go implementation, not a line-by-line port

## Status

Accepted.

## Context

Given rippled as the specification ([0001](0001-rippled-as-specification.md)), the
obvious shortcut is a mechanical C++ → Go transliteration. But rippled leans
heavily on C++ idioms — templates, RAII, operator overloading, deep inheritance —
that have no clean Go equivalent. A transliteration would carry that structure
into a language that fights it, producing code that is neither idiomatic C++ nor
idiomatic Go.

## Decision

Implement goXRPL natively in Go, preserving protocol *semantics* rather than code
*structure*. Use Go interfaces, composition, and table-driven designs in place of
template/inheritance hierarchies. The transaction engine, for example, dispatches
through small interfaces (`Transaction`, `Appliable`, `Preclaimer`, `TecApplier`)
that types opt into, instead of a central switch over a class hierarchy.

## Consequences

- The code reads as ordinary Go and is approachable to Go developers without C++
  background.
- Equivalence with rippled is established by behavior (conformance tests), not by
  structural correspondence — so the port must be verified, not assumed.
- Some rippled mechanisms are re-expressed (e.g. its threading model becomes
  goroutines — see [0003](0003-single-writer-engine.md)); the mapping is
  documented where it is non-obvious.
