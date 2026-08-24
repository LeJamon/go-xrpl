// Package amendment models XRP Ledger amendment metadata, per-ledger activation
// rules, and a node's live amendment policy and observed state.
//
// Feature values describe the process-wide registry and local capability. Rules
// is a read-only activation snapshot for one ledger. Table is the concurrent,
// mutable node state used for operator vote preferences, validated-ledger
// observations, and amendment-block detection. Support, activation, and voting
// policy are independent states.
package amendment
