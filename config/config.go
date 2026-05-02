package config

import (
	"fmt"
	"path/filepath"
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
	NodeSeed         string                 `toml:"node_seed" mapstructure:"node_seed"`
	ClusterNodes     []string               `toml:"cluster_nodes" mapstructure:"cluster_nodes"`
	MaxTransactions  int                    `toml:"max_transactions" mapstructure:"max_transactions"`
	Overlay          OverlayConfig          `toml:"overlay" mapstructure:"overlay"`
	TransactionQueue TransactionQueueConfig `toml:"transaction_queue" mapstructure:"transaction_queue"`

	// 3. Ripple Protocol
	RelayProposals         string      `toml:"relay_proposals" mapstructure:"relay_proposals"`
	RelayValidations       string      `toml:"relay_validations" mapstructure:"relay_validations"`
	LedgerHistory          interface{} `toml:"ledger_history" mapstructure:"ledger_history"` // can be int or "full"
	FetchDepth             interface{} `toml:"fetch_depth" mapstructure:"fetch_depth"`
	ValidationSeed         string      `toml:"validation_seed" mapstructure:"validation_seed"`
	ValidatorToken         string      `toml:"validator_token" mapstructure:"validator_token"`
	ValidatorKeyRevocation string      `toml:"validator_key_revocation" mapstructure:"validator_key_revocation"`
	ValidatorsFile         string      `toml:"validators_file" mapstructure:"validators_file"`
	PathSearch             int         `toml:"path_search" mapstructure:"path_search"`
	PathSearchFast         int         `toml:"path_search_fast" mapstructure:"path_search_fast"`
	PathSearchMax          int         `toml:"path_search_max" mapstructure:"path_search_max"`
	PathSearchOld          int         `toml:"path_search_old" mapstructure:"path_search_old"`
	FeeDefault             int         `toml:"fee_default" mapstructure:"fee_default"`
	Workers                int         `toml:"workers" mapstructure:"workers"`
	IOWorkers              int         `toml:"io_workers" mapstructure:"io_workers"`
	PrefetchWorkers        int         `toml:"prefetch_workers" mapstructure:"prefetch_workers"`
	NetworkID              interface{} `toml:"network_id" mapstructure:"network_id"` // can be int or string
	LedgerReplay           int         `toml:"ledger_replay" mapstructure:"ledger_replay"`

	// 4. HTTPS Client
	SSLVerify     int    `toml:"ssl_verify" mapstructure:"ssl_verify"`
	SSLVerifyFile string `toml:"ssl_verify_file" mapstructure:"ssl_verify_file"`
	SSLVerifyDir  string `toml:"ssl_verify_dir" mapstructure:"ssl_verify_dir"`

	// 6. Database
	NodeDB            NodeDBConfig            `toml:"node_db" mapstructure:"node_db"`
	ImportDB          NodeDBConfig            `toml:"import_db" mapstructure:"import_db"`
	DatabasePath      string                  `toml:"database_path" mapstructure:"database_path"`
	SQLite            SQLiteConfig            `toml:"sqlite" mapstructure:"sqlite"`
	ValidationArchive ValidationArchiveConfig `toml:"validation_archive" mapstructure:"validation_archive"`

	// 7. Diagnostics
	DebugLogfile string        `toml:"debug_logfile" mapstructure:"debug_logfile"`
	Logging      LoggingConfig `toml:"logging" mapstructure:"logging"`
	Insight      InsightConfig `toml:"insight" mapstructure:"insight"`
	Perf         PerfConfig    `toml:"perf" mapstructure:"perf"`

	// 8. Voting
	Voting VotingConfig `toml:"voting" mapstructure:"voting"`

	// 9. Misc Settings
	NodeSize       string      `toml:"node_size" mapstructure:"node_size"`
	SigningSupport bool        `toml:"signing_support" mapstructure:"signing_support"`
	Crawl          CrawlConfig `toml:"crawl" mapstructure:"crawl"`
	VL             VLConfig    `toml:"vl" mapstructure:"vl"`
	BetaRPCAPI     int         `toml:"beta_rpc_api" mapstructure:"beta_rpc_api"`

	// Special startup commands
	RPCStartup             []map[string]interface{} `toml:"rpc_startup" mapstructure:"rpc_startup"`
	WebsocketPingFrequency int                      `toml:"websocket_ping_frequency" mapstructure:"websocket_ping_frequency"`
	ServerDomain           string                   `toml:"server_domain" mapstructure:"server_domain"`

	// WebSocketAllowedOrigins restricts which Origin headers may upgrade to a
	// WebSocket connection. An empty list allows all origins, matching
	// rippled's default behavior. Entries are matched against the host:port of
	// the request Origin (case-insensitive on host); the literal value "*"
	// also allows all origins.
	WebSocketAllowedOrigins []string `toml:"websocket_allowed_origins" mapstructure:"websocket_allowed_origins"`

	// Genesis file path (JSON format)
	// If empty, uses built-in default genesis configuration
	GenesisFile               string `toml:"genesis_file" mapstructure:"genesis_file"`
	GenesisAmendmentsDisabled bool   `toml:"genesis_amendments_disabled" mapstructure:"genesis_amendments_disabled"`

	// Validators configuration (loaded from separate file)
	Validators ValidatorsConfig `toml:"-" mapstructure:"-"`

	// Internal fields for configuration management
	configPath     string `toml:"-" mapstructure:"-"`
	validatorsPath string `toml:"-" mapstructure:"-"`
}

// ConfigPaths holds the paths to configuration files
type ConfigPaths struct {
	Main       string // Path to main config file (xrpld.toml)
	Validators string // Path to validators file (validators.toml)
}

// ConfigPathsFromDir returns configuration paths for a specific directory
func ConfigPathsFromDir(configDir string) ConfigPaths {
	return ConfigPaths{
		Main:       filepath.Join(configDir, "xrpld.toml"),
		Validators: filepath.Join(configDir, "validators.toml"),
	}
}

// GetConfigPath returns the path to the main configuration file
func (c *Config) GetConfigPath() string {
	return c.configPath
}

// GetValidatorsPath returns the path to the validators configuration file
func (c *Config) GetValidatorsPath() string {
	return c.validatorsPath
}

// GetNetworkID returns the network ID as an integer
func (c *Config) GetNetworkID() (int, error) {
	switch v := c.NetworkID.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case string:
		switch v {
		case "main":
			return 0, nil
		case "testnet":
			return 1, nil
		case "devnet":
			return 2, nil
		default:
			return 0, fmt.Errorf("unknown network name: %s", v)
		}
	case nil:
		return 0, fmt.Errorf("network_id is required but not set")
	default:
		return 0, fmt.Errorf("invalid network_id type: %T", v)
	}
}

// GetLedgerHistory returns the ledger history as an integer or -1 for "full"
func (c *Config) GetLedgerHistory() (int, error) {
	switch v := c.LedgerHistory.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case string:
		if v == "full" {
			return -1, nil
		}
		if v == "none" {
			return 0, nil
		}
		return 0, fmt.Errorf("invalid ledger_history value: %s", v)
	case nil:
		return 0, fmt.Errorf("ledger_history is required but not set")
	default:
		return 0, fmt.Errorf("invalid ledger_history type: %T", v)
	}
}

// GetFetchDepth returns the fetch depth as an integer or -1 for "full"
func (c *Config) GetFetchDepth() (int, error) {
	switch v := c.FetchDepth.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case string:
		if v == "full" {
			return -1, nil
		}
		return 0, fmt.Errorf("invalid fetch_depth value: %s", v)
	case nil:
		return 0, fmt.Errorf("fetch_depth is required but not set")
	default:
		return 0, fmt.Errorf("invalid fetch_depth type: %T", v)
	}
}

// IsValidator returns true if this node is configured as a validator
func (c *Config) IsValidator() bool {
	return c.ValidationSeed != "" || c.ValidatorToken != ""
}

// GetPort returns the configuration for a specific port by name
func (c *Config) GetPort(name string) (PortConfig, bool) {
	port, exists := c.Ports[name]
	return port, exists
}

// GetAdminPorts returns all ports that have admin access configured
func (c *Config) GetAdminPorts() map[string]PortConfig {
	adminPorts := make(map[string]PortConfig)
	for name, port := range c.Ports {
		if len(port.Admin) > 0 || port.AdminUser != "" {
			adminPorts[name] = port
		}
	}
	return adminPorts
}

// GetPeerPort returns the port configured for peer protocol
func (c *Config) GetPeerPort() (string, PortConfig, bool) {
	for name, port := range c.Ports {
		if containsProtocol(port.Protocol, "peer") {
			return name, port, true
		}
	}
	return "", PortConfig{}, false
}

// GetHTTPPorts returns all ports that support HTTP/HTTPS protocols
func (c *Config) GetHTTPPorts() map[string]PortConfig {
	httpPorts := make(map[string]PortConfig)
	for name, port := range c.Ports {
		if containsProtocol(port.Protocol, "http") || containsProtocol(port.Protocol, "https") {
			httpPorts[name] = port
		}
	}
	return httpPorts
}

// GetWebSocketPorts returns all ports that support WebSocket protocols
func (c *Config) GetWebSocketPorts() map[string]PortConfig {
	wsPorts := make(map[string]PortConfig)
	for name, port := range c.Ports {
		if containsProtocol(port.Protocol, "ws") || containsProtocol(port.Protocol, "wss") {
			wsPorts[name] = port
		}
	}
	return wsPorts
}

// containsProtocol checks if a protocol string contains a specific protocol
func containsProtocol(protocols, target string) bool {
	return contains(protocols, target)
}

// contains checks if a string contains a substring (case-insensitive)
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
