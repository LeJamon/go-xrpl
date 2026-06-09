// Package replaytool implements the offline mainnet-replay and fixture-replay
// developer commands (`replay`, `replay-range`). It re-executes captured
// rippled ledgers against the Go transaction engine and reports state-tree
// divergences; it is a development and conformance tool, distinct from the
// node's production inbound-ledger replay (internal/ledger/inbound and
// internal/consensus/adaptor).
package replaytool

import "github.com/spf13/cobra"

// NewCommands returns the replay tool's cobra commands for registration on a
// root command. The commands' flags are configured at package init time.
func NewCommands() []*cobra.Command {
	return []*cobra.Command{replayCmd, replayRangeCmd}
}
