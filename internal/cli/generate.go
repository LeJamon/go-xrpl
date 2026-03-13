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
	Run: runGenerateConfig,
}

func init() {
	rootCmd.AddCommand(generateConfigCmd)

	generateConfigCmd.Flags().StringVar(&generateNetwork, "network", "main", "network type: main, testnet, or devnet")
	generateConfigCmd.Flags().StringVar(&generateOutput, "output", "xrpld.toml", "output file path")
}

func runGenerateConfig(cmd *cobra.Command, args []string) {
	var networkID string
	switch generateNetwork {
	case "main", "testnet", "devnet":
		networkID = generateNetwork
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown network %q (valid: main, testnet, devnet)\n", generateNetwork)
		os.Exit(1)
	}

	content := generateConfigContent(networkID)

	if err := os.WriteFile(generateOutput, []byte(content), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing config file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Configuration file generated: %s\n", generateOutput)
	fmt.Printf("  Network: %s\n", networkID)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Review and adjust the configuration values")
	fmt.Println("  2. Start the server: xrpld server --conf", generateOutput)
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

	return fmt.Sprintf(`# goXRPLd configuration file
# Generated for network: %s
# Review and adjust ALL values before starting the server.
# All fields listed here are REQUIRED unless marked as optional.

# =============================================================================
# Top-level settings (MUST come before any [section] headers in TOML)
# =============================================================================

# Peer Protocol
compression = false
peer_private = 0
peers_max = 21
max_transactions = 250

%s

# Ripple Protocol
relay_proposals = "trusted"
relay_validations = "all"
ledger_history = 256
fetch_depth = "full"
path_search = 2
path_search_fast = 2
path_search_max = 3
path_search_old = 2
workers = 0
io_workers = 0
prefetch_workers = 0
network_id = "%s"
ledger_replay = 0

# HTTPS Client
ssl_verify = 1

# Database path
database_path = "./data/db"

# Diagnostics
debug_logfile = "./data/log/debug.log"

# Misc
node_size = "medium"
signing_support = false
beta_rpc_api = 0

# Validators file (optional)
# validators_file = "validators.toml"

# Genesis file (optional — omit to use built-in defaults)
# genesis_file = "genesis.json"

# RPC commands to run at startup (optional)
rpc_startup = [
    { command = "log_level", severity = "warning" }
]

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
# Overlay & Transaction Queue
# =============================================================================

[overlay]
max_unknown_time = 600
max_diverged_time = 300

[transaction_queue]
ledgers_in_queue = 20
minimum_queue_size = 2000
retry_sequence_percent = 25
minimum_escalation_multiplier = 500
minimum_txn_in_ledger = 5
minimum_txn_in_ledger_standalone = 1000
target_txn_in_ledger = 50
maximum_txn_in_ledger = 0
normal_consensus_increase_percent = 20
slow_consensus_decrease_percent = 50
maximum_txn_per_account = 10
minimum_last_ledger_buffer = 2
zero_basefee_transaction_feelevel = 256000
`, network, ips, network)
}
