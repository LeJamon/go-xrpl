package config

import (
	"fmt"
	"strings"
)

// Config represents the complete xrpld configuration
// This mirrors the structure of rippled.cfg
type Config struct {
	// 1. Server section
	Server ServerConfig `toml:"server" mapstructure:"server"`

	// Port configurations (dynamic based on server.ports)
	Ports map[string]PortConfig `toml:"-" mapstructure:"-"`

	// 2. Peer Protocol
	Compression      bool                   `toml:"compression" mapstructure:"compression"`
	IPs              []string               `toml:"ips" mapstructure:"ips"`
	IPsFixed         []string               `toml:"ips_fixed" mapstructure:"ips_fixed"`
	PeerPrivate      int                    `toml:"peer_private" mapstructure:"peer_private"`
	PeersMax         int                    `toml:"peers_max" mapstructure:"peers_max"`
	ClusterNodes     []string               `toml:"cluster_nodes" mapstructure:"cluster_nodes"`
	MaxTransactions  int                    `toml:"max_transactions" mapstructure:"max_transactions"` // 0 = use default (250)
	Overlay          OverlayConfig          `toml:"overlay" mapstructure:"overlay"`
	TransactionQueue TransactionQueueConfig `toml:"transaction_queue" mapstructure:"transaction_queue"`

	// 3. Ripple Protocol
	RelayProposals   string        `toml:"relay_proposals" mapstructure:"relay_proposals"`     // optional; "" = default ("trusted")
	RelayValidations string        `toml:"relay_validations" mapstructure:"relay_validations"` // optional; "" = default ("all")
	LedgerHistory    LedgerHistory `toml:"ledger_history" mapstructure:"ledger_history"`       // integer, "full", or "none"; absent = 256
	FetchDepth       FetchDepth    `toml:"fetch_depth" mapstructure:"fetch_depth"`             // integer, "full", or "none"; absent = "full"; values < 10 are raised to 10
	ValidationSeed   string        `toml:"validation_seed" mapstructure:"validation_seed"`
	ValidatorToken   string        `toml:"validator_token" mapstructure:"validator_token"`
	ValidatorsFile   string        `toml:"validators_file" mapstructure:"validators_file"`
	NetworkID        NetworkID     `toml:"network_id" mapstructure:"network_id"` // integer or named string ("main", "testnet", "devnet")
	LedgerReplay     int           `toml:"ledger_replay" mapstructure:"ledger_replay"`

	// 6. Database
	NodeDB            NodeDBConfig            `toml:"node_db" mapstructure:"node_db"`
	DatabasePath      string                  `toml:"database_path" mapstructure:"database_path"`
	SQLite            SQLiteConfig            `toml:"sqlite" mapstructure:"sqlite"`
	ValidationArchive ValidationArchiveConfig `toml:"validation_archive" mapstructure:"validation_archive"`

	// 7. Diagnostics
	DebugLogfile string        `toml:"debug_logfile" mapstructure:"debug_logfile"`
	Logging      LoggingConfig `toml:"logging" mapstructure:"logging"`

	// 8. Voting
	Voting VotingConfig `toml:"voting" mapstructure:"voting"`

	// Amendments holds the operator's amendment voting preferences.
	Amendments AmendmentsConfig `toml:"amendments" mapstructure:"amendments"`

	// 9. Misc Settings
	NodeSize   string `toml:"node_size" mapstructure:"node_size"` // optional; "" = default ("medium")
	BetaRPCAPI int    `toml:"beta_rpc_api" mapstructure:"beta_rpc_api"`

	// WebsocketPingFrequency is the keepalive ping cadence in seconds for
	// WebSocket clients. 0 = use the built-in default (30 seconds).
	WebsocketPingFrequency int    `toml:"websocket_ping_frequency" mapstructure:"websocket_ping_frequency"`
	ServerDomain           string `toml:"server_domain" mapstructure:"server_domain"`

	// Genesis file path (JSON format)
	// If empty, uses built-in default genesis configuration
	GenesisFile               string `toml:"genesis_file" mapstructure:"genesis_file"`
	GenesisAmendmentsDisabled bool   `toml:"genesis_amendments_disabled" mapstructure:"genesis_amendments_disabled"`

	// Validators configuration (loaded from separate file)
	Validators ValidatorsConfig `toml:"-" mapstructure:"-"`
}

// ConfigPaths holds the paths to configuration files
type ConfigPaths struct {
	Main       string // Path to main config file (xrpld.toml)
	Validators string // Path to validators file (validators.toml)
}

// networkIDByName maps rippled's named network aliases to their canonical
// IDs. The names are case-sensitive, matching rippled's operator==
// comparison (Config.cpp:525-530).
var networkIDByName = map[string]int{
	"main":    0,
	"testnet": 1,
	"devnet":  2,
}

// GetNetworkID returns the network ID as an integer.
// String network names ("main", "testnet", "devnet") are mapped to their
// canonical IDs (0, 1, 2).
func (c *Config) GetNetworkID() (int, error) {
	if !c.NetworkID.Set {
		return 0, fmt.Errorf("network_id is required but not set")
	}
	if c.NetworkID.Name == "" {
		return c.NetworkID.ID, nil
	}
	id, ok := networkIDByName[c.NetworkID.Name]
	if !ok {
		// Unreachable for decoder-produced values; kept for hand-built configs.
		return 0, fmt.Errorf("unknown network name: %s", c.NetworkID.Name)
	}
	return id, nil
}

// defaultLedgerHistory mirrors rippled's LEDGER_HISTORY default (Config.h).
const defaultLedgerHistory = 256

// GetLedgerHistory returns the configured ledger history as an integer.
// "full" maps to math.MaxInt32 (matching rippled's uint32 max sentinel)
// so that downstream comparisons such as the online_delete cross-check
// fire the same way they do in rippled. When the key is absent the
// rippled default of 256 applies.
func (c *Config) GetLedgerHistory() int {
	if !c.LedgerHistory.Set {
		return defaultLedgerHistory
	}
	return c.LedgerHistory.Value()
}

// GetFetchDepth returns the configured fetch depth as an integer.
// "full" maps to math.MaxInt32, and any explicit count below 10 is
// raised to 10 to mirror rippled's hard floor (Config.cpp:671-672).
// When the key is absent the rippled default of "full" applies.
func (c *Config) GetFetchDepth() int {
	if !c.FetchDepth.Set {
		return FetchDepth{Set: true, Full: true}.Value()
	}
	return c.FetchDepth.Value()
}

// IsValidator returns true if this node is configured as a validator
func (c *Config) IsValidator() bool {
	return c.ValidationSeed != "" || c.ValidatorToken != ""
}

// GetPeerPort returns the port configured for peer protocol
func (c *Config) GetPeerPort() (string, PortConfig, bool) {
	for name, port := range c.Ports {
		if strings.Contains(port.Protocol, "peer") {
			return name, port, true
		}
	}
	return "", PortConfig{}, false
}

// GetHTTPPorts returns all ports that support HTTP/HTTPS protocols
func (c *Config) GetHTTPPorts() map[string]PortConfig {
	httpPorts := make(map[string]PortConfig)
	for name, port := range c.Ports {
		if strings.Contains(port.Protocol, "http") {
			httpPorts[name] = port
		}
	}
	return httpPorts
}

// GetWebSocketPorts returns all ports that support WebSocket protocols
func (c *Config) GetWebSocketPorts() map[string]PortConfig {
	wsPorts := make(map[string]PortConfig)
	for name, port := range c.Ports {
		if strings.Contains(port.Protocol, "ws") {
			wsPorts[name] = port
		}
	}
	return wsPorts
}
