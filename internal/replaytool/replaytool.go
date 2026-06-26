// Package replaytool implements the offline mainnet-replay and fixture-replay
// developer commands (`replay`, `replay-range`). It re-executes captured
// rippled ledgers against the Go transaction engine and reports state-tree
// divergences; it is a development and conformance tool, distinct from the
// node's production inbound-ledger replay (internal/ledger/inbound and
// internal/consensus/adaptor).
package replaytool

import "github.com/spf13/cobra"

// NewCommands returns freshly-constructed replay tool commands for registration
// on a root command. Each call builds new *cobra.Command instances whose flags
// bind to a per-command runner struct rather than package globals, so two
// registered instances share no state.
func NewCommands() []*cobra.Command {
	return []*cobra.Command{newReplayCmd(), newReplayRangeCmd()}
}
