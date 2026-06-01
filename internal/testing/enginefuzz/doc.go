// Package enginefuzz hosts a stateful, property-based fuzz harness over the
// goXRPL transaction engine.
//
// Every other fuzz target in the repository covers a stateless decode surface
// (codec, crypto, shamap-wire, peer-frame). This package adds the missing
// stateful layer: it generates structurally-plausible transaction sequences
// from the fuzzer's byte stream, applies them through internal/tx/engine
// against a seeded ledger, and asserts after every apply that the engine never
// reports an invariant violation (tec/tefINVARIANT_FAILED) and that total XRP
// is never inflated.
//
// The engine runs the full internal/tx/invariants set on every apply -- the
// same oracle rippled exercises under Antithesis -- so a fuzzer-found invariant
// violation is, by construction, a consensus-safety bug.
//
// The harness and fuzz target live in this package's _test.go files; this file
// exists only so the package has a non-test compilation unit. See issue #682.
package enginefuzz
