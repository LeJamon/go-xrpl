// Package tx defines the transaction model and the registry of transaction
// types.
//
// Each transaction type lives in its own subpackage and declares a single
// Descriptor: its constructor and every static per-type attribute the engine,
// invariant checks, Batch and the transaction queue consult. The subpackage's
// Register function passes it to Register, which rejects an incomplete
// descriptor, and all.RegisterAll calls every subpackage's Register at startup.
// Field templates live in template.go and type names in package protocol.
package tx
