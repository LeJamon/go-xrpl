package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const generatedValidatorsFilename = "validators.toml"

type generatedFile struct {
	path string
	data []byte
	mode os.FileMode
}

type generateOptions struct {
	network string
	output  string
}

func newGenerateConfigCommand() *cobra.Command {
	options := &generateOptions{}
	command := &cobra.Command{
		Use:   "generate-config",
		Short: "Generate a complete configuration file",
		Long: `Generate a complete goxrpl.toml configuration file with all required fields.
The generated file is a working starting point that passes validation.
Review and adjust the values before using it to start the server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGenerateConfig(cmd, options)
		},
	}
	command.Flags().StringVar(&options.network, "network", "main", "network type: main, testnet, or devnet")
	command.Flags().StringVar(&options.output, "output", "goxrpl.toml", "output file path")
	return command
}

func runGenerateConfig(cmd *cobra.Command, options *generateOptions) error {
	var networkID string
	switch options.network {
	case "main", "testnet", "devnet":
		networkID = options.network
	default:
		return fmt.Errorf("unknown network %q (valid: main, testnet, devnet)", options.network)
	}

	content := generateConfigContent(networkID)
	validatorsContent, hasValidators := generateValidatorsContent(networkID)
	files := make([]generatedFile, 0, 2)
	if hasValidators {
		validatorsOutput := filepath.Join(filepath.Dir(options.output), generatedValidatorsFilename)
		if filepath.Clean(validatorsOutput) == filepath.Clean(options.output) {
			return fmt.Errorf("output path must not be named %s", generatedValidatorsFilename)
		}
		files = append(files, generatedFile{path: validatorsOutput, data: []byte(validatorsContent), mode: 0o644})
	}
	files = append(files, generatedFile{path: options.output, data: []byte(content), mode: 0o600})
	if err := publishGeneratedFiles(files, os.Link); err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Configuration file generated: %s\n", options.output)
	fmt.Fprintf(w, "  Network: %s\n", networkID)
	if hasValidators {
		fmt.Fprintf(w, "  Validators: %s\n", filepath.Join(filepath.Dir(options.output), generatedValidatorsFilename))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Next steps:")
	fmt.Fprintln(w, "  1. Review and adjust the configuration values")
	if networkID == "devnet" {
		fmt.Fprintln(w, "  2. Configure trusted validators, or start in standalone mode:")
		fmt.Fprintln(w, "     goxrpl server --standalone --conf", options.output)
	} else {
		fmt.Fprintln(w, "  2. Start the server: goxrpl server --conf", options.output)
	}
	return nil
}

func publishGeneratedFiles(files []generatedFile, link func(string, string) error) error {
	seen := make(map[string]struct{}, len(files))
	pending := make([]generatedFile, 0, len(files))
	permissionUpdates := make([]generatedFile, 0, len(files))
	for _, file := range files {
		path, err := filepath.Abs(file.path)
		if err != nil {
			return fmt.Errorf("resolve output path %s: %w", file.path, err)
		}
		path = filepath.Clean(path)
		if _, ok := seen[path]; ok {
			return fmt.Errorf("generated outputs resolve to the same path: %s", file.path)
		}
		seen[path] = struct{}{}
		if info, err := os.Lstat(path); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("output already exists and is not a regular file: %s", file.path)
			}
			existing, readErr := os.ReadFile(path)
			if readErr != nil {
				return fmt.Errorf("read existing output %s: %w", file.path, readErr)
			}
			if !bytes.Equal(existing, file.data) {
				return fmt.Errorf("output already exists with different content: %s", file.path)
			}
			if currentMode := info.Mode().Perm(); currentMode&^file.mode != 0 {
				file.mode = currentMode & file.mode
				permissionUpdates = append(permissionUpdates, file)
			}
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect output path %s: %w", file.path, err)
		}
		pending = append(pending, file)
	}
	files = pending
	for _, file := range permissionUpdates {
		if err := os.Chmod(file.path, file.mode); err != nil {
			return fmt.Errorf("tighten output permissions %s: %w", file.path, err)
		}
	}

	temps := make([]string, len(files))
	defer func() {
		for _, path := range temps {
			if path != "" {
				_ = os.Remove(path)
			}
		}
	}()
	for i, file := range files {
		tmp, err := os.CreateTemp(filepath.Dir(file.path), "."+filepath.Base(file.path)+"-*")
		if err != nil {
			return fmt.Errorf("stage output %s: %w", file.path, err)
		}
		temps[i] = tmp.Name()
		if err := tmp.Chmod(file.mode); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("set output permissions %s: %w", file.path, err)
		}
		if _, err := tmp.Write(file.data); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("write staged output %s: %w", file.path, err)
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("sync staged output %s: %w", file.path, err)
		}
		if err := tmp.Close(); err != nil {
			return fmt.Errorf("close staged output %s: %w", file.path, err)
		}
	}

	published := make([]string, 0, len(files))
	for i, file := range files {
		if err := link(temps[i], file.path); err != nil {
			var rollbackErr error
			for _, path := range published {
				if removeErr := os.Remove(path); removeErr != nil {
					rollbackErr = errors.Join(rollbackErr, removeErr)
				}
			}
			if rollbackErr != nil {
				return fmt.Errorf("publish output %s: %w (rollback failed: %v)", file.path, err, rollbackErr)
			}
			return fmt.Errorf("publish output %s: %w", file.path, err)
		}
		published = append(published, file.path)
	}
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

	validatorsFile := `validators_file = "validators.toml"`
	if network == "devnet" {
		validatorsFile = `# Devnet has no public validator list. Configure trusted validators here,
# or start the server with --standalone.
# validators_file = "validators.toml"`
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

# Ripple Protocol (optional — defaults shown below)
relay_proposals = "trusted"
relay_validations = "all"
ledger_history = 256
ledger_cache_size = 256
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

# Validators file
%s

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

# Stall watchdog (optional). Monitors consensus-loop liveness; stack capture is
# reserved for the abort path.
# [watchdog]
# disabled = false
# warn_seconds = 10
# fatal_seconds = 90
# abort_seconds = 600

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
port = 2459
ip = "0.0.0.0"
protocol = "peer"

[port_ws_admin_local]
port = 6006
ip = "127.0.0.1"
admin = ["127.0.0.1"]
protocol = "ws"
# send_queue_limit: 0 uses the default 100; explicit values must be 1–65535.
send_queue_limit = 500

# =============================================================================
# Database
# =============================================================================

[node_db]
type = "pebble"
path = "./data/db/pebble"
online_delete = 512
advisory_delete = 0
cache_mb = 256
open_files = 500
cache_size = 16384
cache_age = 5
fast_load = false
fast_load_workers = 0 # 0 = automatic based on available CPUs; maximum 64
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
normal_consensus_increase_percent = 20
slow_consensus_decrease_percent = 50
maximum_txn_per_account = 10
minimum_last_ledger_buffer = 2
`, network, ips, network, validatorsFile)
}

func generateValidatorsContent(network string) (string, bool) {
	switch network {
	case "main":
		return `validator_list_sites = [
    "https://vl.ripple.com",
    "https://unl.xrplf.org"
]

validator_list_keys = [
    "ED2677ABFFD1B33AC6FBC3062B71F1E8397C1505E1C42C64D11AD1B28FF73F4734",
    "ED42AEC58B701EEBB77356FFFEC26F83C1F0407263530F068C7C73D392C7E06FD1"
]

validator_list_threshold = 0
`, true
	case "testnet":
		return `validator_list_sites = [
    "https://vl.altnet.rippletest.net"
]

validator_list_keys = [
    "ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860"
]

validator_list_threshold = 0
`, true
	default:
		return "", false
	}
}
