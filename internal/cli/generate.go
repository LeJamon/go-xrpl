package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	generateNetwork string
	generateOutput  string
)

var generateConfigCmd = &cobra.Command{
	Use:   "generate-config",
	Short: "Generate a complete configuration file",
	Long: `Generate a complete xrpld.toml configuration file with all required fields.
The generated file is a working starting point that passes validation.
Review and adjust the values before using it to start the server.`,
	RunE: runGenerateConfig,
}

func init() {
	rootCmd.AddCommand(generateConfigCmd)

	generateConfigCmd.Flags().StringVar(&generateNetwork, "network", "main", "network type: main, testnet, or devnet")
	generateConfigCmd.Flags().StringVar(&generateOutput, "output", "xrpld.toml", "output file path")
}

func runGenerateConfig(cmd *cobra.Command, args []string) error {
	var networkID string
	switch generateNetwork {
	case "main", "testnet", "devnet":
		networkID = generateNetwork
	default:
		return fmt.Errorf("unknown network %q (valid: main, testnet, devnet)", generateNetwork)
	}

	content := generateConfigContent(networkID)

	if err := os.WriteFile(generateOutput, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Configuration file generated: %s\n", generateOutput)
	fmt.Fprintf(w, "  Network: %s\n", networkID)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next steps:")
	fmt.Fprintln(w, "  1. Review and adjust the configuration values")
	fmt.Fprintln(w, "  2. Start the server: xrpld server --conf", generateOutput)
	return nil
}

func generateConfigContent(network string) string {
	// Network-specific values
	var ips string
	switch network {
	case "main":
		ips = `ips = [
    "r.ripple.com 51235",
    "sahyadri.isrdc.in 51235",
    "hubs.xrpkuwait.com 51235",
    "hub.xrpl-commons.org 51235"
]`
	case "testnet":
		ips = `ips = [
    "r.altnet.rippletest.net 51235"
]`
	case "devnet":
		ips = `ips = []`
	}

	return fmt.Sprintf(`# go-xrpl configuration file
# Generated for network: %s
# Review and adjust ALL values before starting the server.
# All fields listed here are REQUIRED unless marked as optional.

# =============================================================================
# Top-level settings (MUST come before any [section] headers in TOML)
# =============================================================================

# Peer Protocol (optional tuning)
compression = false
peer_private = 0
peers_max = 21
max_transactions = 250

%s

# Ripple Protocol (optional — defaults: trusted / all / 256 / "full")
relay_proposals = "trusted"
relay_validations = "all"
ledger_history = 256
fetch_depth = "full"
network_id = "%s"
ledger_replay = 0

# Database path
database_path = "./data/db"

# Diagnostics
debug_logfile = "./data/log/debug.log"

# Misc (optional)
node_size = "medium"
beta_rpc_api = 0

# Operator domain emitted in the peer handshake (optional)
# server_domain = "example.com"

# WebSocket keepalive ping cadence in seconds (optional — default 30)
# websocket_ping_frequency = 30

# Validators file (optional)
# validators_file = "validators.toml"

# Genesis file (optional — omit to use built-in defaults)
# genesis_file = "genesis.json"

# =============================================================================
# Logging
# =============================================================================

[logging]
level  = "info"   # trace | debug | info | warn | error
format = "text"   # text (human-readable) | json (for log aggregators)
output = "stdout" # stdout | stderr | /path/to/logfile

# Per-partition level overrides (uncomment to increase verbosity per subsystem)
# [logging.partitions]
# Tx              = "debug"
# Flow            = "debug"
# Pathfinder      = "debug"
# LedgerConsensus = "debug"
# NodeStore       = "debug"

# =============================================================================
# Server Configuration
# =============================================================================

[server]
ports = ["port_rpc_admin_local", "port_peer", "port_ws_admin_local"]

[port_rpc_admin_local]
port = 5005
ip = "127.0.0.1"
admin = ["127.0.0.1"]
protocol = "http"

[port_peer]
port = 51235
ip = "0.0.0.0"
protocol = "peer"

[port_ws_admin_local]
port = 6006
ip = "127.0.0.1"
admin = ["127.0.0.1"]
protocol = "ws"
send_queue_limit = 500

# =============================================================================
# Database
# =============================================================================

[node_db]
type = "pebble"
path = "./data/db/pebble"
online_delete = 512
advisory_delete = 0
cache_size = 16384
cache_age = 5
fast_load = false
earliest_seq = 32570
delete_batch = 100
back_off_milliseconds = 100
age_threshold_seconds = 60
recovery_wait_seconds = 5

[sqlite]
journal_mode = "wal"
synchronous = "normal"
temp_store = "file"
page_size = 4096
journal_size_limit = 1582080

# =============================================================================
# Overlay & Transaction Queue (optional tuning — values shown are the
# built-in defaults; omit either section to use them)
# =============================================================================

[overlay]
max_unknown_time = 600
max_diverged_time = 300
# public_ip = "203.0.113.7"

[transaction_queue]
ledgers_in_queue = 20
minimum_queue_size = 2000
retry_sequence_percent = 25
minimum_escalation_multiplier = 128000
minimum_txn_in_ledger = 32
minimum_txn_in_ledger_standalone = 1000
target_txn_in_ledger = 256
maximum_txn_in_ledger = 0
normal_consensus_increase_percent = 20
slow_consensus_decrease_percent = 50
maximum_txn_per_account = 10
minimum_last_ledger_buffer = 2
`, network, ips, network)
}
