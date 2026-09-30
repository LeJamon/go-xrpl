// Package enginefuzz hosts a stateful, property-based fuzz harness over the
// goXRPL transaction engine.
//
// The bounded traces generate structurally plausible transaction sequences
// from the fuzzer byte stream, apply them through internal/tx/engine against a
// seeded ledger, and verify result classification, rollback atomicity, fee and
// sequence accounting, state supply, and deterministic close behavior. The
// deterministic corpus also requires every supported generated kind to reach
// an applied result and the transaction and invariant phases.
//
// Invariant checks run on applied tes and fee-claiming tec paths. The harness
// records phase reachability and common-field/ApplyFlags coverage, but it is a
// bounded Go property target rather than a rippled differential oracle or a
// whole-node coverage claim.
package enginefuzz
