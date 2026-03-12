// Package amendment implements the XRPL amendment system for managing protocol
// feature activation.
//
// Amendments are identified by a 256-bit hash derived from their name. Each
// amendment has a support status (supported or unsupported by this node) and a
// voting default (yes or no). The Rules type tracks which amendments are
// currently enabled on a given ledger, allowing transaction processing code to
// gate behavior behind feature flags.
//
// The amendment registry is derived from rippled's features.macro.
package amendment
