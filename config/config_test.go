package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int { return &v }

// completeTestConfig returns a TOML string with all required fields populated.
// IMPORTANT: Top-level keys MUST come before any [section] headers in TOML.
func completeTestConfig() string {
	return `
# Top-level fields (must come before any [section] headers)
database_path = "/tmp/test/db"
network_id = "main"
ledger_history = 256
fetch_depth = "full"
node_size = "tiny"
debug_logfile = "/tmp/test/debug.log"
relay_proposals = "trusted"
relay_validations = "all"
max_transactions = 250
peers_max = 21
compression = false

[server]
ports = ["port_test"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"

[node_db]
type = "pebble"
path = "/tmp/test/db"
cache_size = 16384
cache_age = 5
earliest_seq = 32570
online_delete = 512
delete_batch = 100
back_off_milliseconds = 100
age_threshold_seconds = 60
recovery_wait_seconds = 5

[overlay]
max_unknown_time = 600
max_diverged_time = 300

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

[sqlite]
journal_mode = "wal"
synchronous = "normal"
temp_store = "file"
page_size = 4096
journal_size_limit = 1582080
`
}

// minimalTestConfig returns a TOML string carrying only the required keys:
// everything else is optional and must fall back to documented defaults.
func minimalTestConfig() string {
	return `
database_path = "/tmp/test/db"
network_id = "main"
debug_logfile = "/tmp/test/debug.log"

[server]
ports = ["port_test"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"

[node_db]
type = "pebble"
path = "/tmp/test/db"
`
}

func writeConfig(t *testing.T, dir, name, contents string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(contents), 0644))
	return p
}

func TestLoadConfig(t *testing.T) {
	tempDir := t.TempDir()
	mainConfigPath := writeConfig(t, tempDir, "test_config.toml", completeTestConfig())

	config, err := LoadConfig(Paths{Main: mainConfigPath})
	require.NoError(t, err)
	require.NotNil(t, config)

	assert.Equal(t, []string{"port_test"}, config.Server.Ports)
	assert.Equal(t, "pebble", config.NodeDB.Type)
	assert.Equal(t, "/tmp/test/db", config.NodeDB.Path)

	portConfig, exists := config.Ports["port_test"]
	assert.True(t, exists)
	assert.Equal(t, 8080, portConfig.Port)
	assert.Equal(t, "127.0.0.1", portConfig.IP)
	assert.Equal(t, "http", portConfig.Protocol)
}

func TestLoadConfig_ServerAccessDefaults(t *testing.T) {
	tempDir := t.TempDir()
	mainConfigPath := writeConfig(t, tempDir, "xrpld.toml", `
database_path = "/tmp/test/db"
network_id = "main"
debug_logfile = "/tmp/test/debug.log"

[server]
ports = ["port_test"]
admin = ["127.0.0.1"]
admin_user = "common-user"
admin_password = "common-password"
secure_gateway = ["10.0.0.0/8"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"
admin = ["::1"]
admin_password = "port-password"
secure_gateway = ["192.168.0.0/16"]

[node_db]
type = "pebble"
path = "/tmp/test/db"
`)

	cfg, err := LoadConfig(Paths{Main: mainConfigPath})
	require.NoError(t, err)
	port := cfg.Ports["port_test"]
	assert.Equal(t, []string{"127.0.0.1", "::1"}, port.Admin)
	assert.Equal(t, "common-user", port.AdminUser)
	assert.Equal(t, "port-password", port.AdminPassword)
	assert.Equal(t, []string{"10.0.0.0/8", "192.168.0.0/16"}, port.SecureGateway)
}

// TestLoadConfig_MinimalConfig verifies that the optional tuning sections
// ([overlay], [transaction_queue], [sqlite], ledger_history, fetch_depth,
// node_size, relay_*, max_transactions) may be omitted entirely.
func TestLoadConfig_MinimalConfig(t *testing.T) {
	tempDir := t.TempDir()
	mainConfigPath := writeConfig(t, tempDir, "xrpld.toml", minimalTestConfig())

	config, err := LoadConfig(Paths{Main: mainConfigPath})
	require.NoError(t, err)
	require.NotNil(t, config)

	assert.Equal(t, 256, config.ResolvedLedgerHistory())
	assert.Equal(t, defaultFetchDepth, config.ResolvedFetchDepth())
	assert.Zero(t, config.MaxTransactions)
	assert.Empty(t, config.NodeSize)
}

func TestLoadConfig_WithValidators(t *testing.T) {
	tempDir := t.TempDir()

	configContent := `
database_path = "/tmp/test/db"
network_id = "main"
debug_logfile = "/tmp/test/debug.log"
validators_file = "test_validators.toml"

[server]
ports = ["port_test"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"

[node_db]
type = "pebble"
path = "/tmp/test/db"
`
	mainConfigPath := writeConfig(t, tempDir, "test_config.toml", configContent)

	validatorsContent := `
validator_list_sites = ["https://test.example.com"]
validator_list_keys = ["ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860"]
validator_list_threshold = 1
`
	writeConfig(t, tempDir, "test_validators.toml", validatorsContent)

	// validators_file is relative — must resolve against the main config dir.
	config, err := LoadConfig(Paths{Main: mainConfigPath})
	require.NoError(t, err)
	require.NotNil(t, config)

	assert.Equal(t, []string{"https://test.example.com"}, config.Validators.ValidatorListSites)
	assert.Equal(t, []string{"ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860"}, config.Validators.ValidatorListKeys)
	assert.Equal(t, 1, config.Validators.ValidatorListThreshold)
}

// TestLoadConfig_ValidatorsFilePrecedence verifies that an explicit
// validators_file in the main config wins over a caller-supplied
// paths.Validators.
func TestLoadConfig_ValidatorsFilePrecedence(t *testing.T) {
	tempDir := t.TempDir()

	configContent := `
database_path = "/tmp/test/db"
network_id = "main"
debug_logfile = "/tmp/test/debug.log"
validators_file = "explicit_validators.toml"

[server]
ports = ["port_test"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"

[node_db]
type = "pebble"
path = "/tmp/test/db"
`
	mainConfigPath := writeConfig(t, tempDir, "xrpld.toml", configContent)
	writeConfig(t, tempDir, "explicit_validators.toml", `
validator_list_sites = ["https://from-explicit.example.com"]
validator_list_keys = ["ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860"]
`)
	otherPath := writeConfig(t, tempDir, "other_validators.toml", `
validator_list_sites = ["https://from-paths.example.com"]
validator_list_keys = ["ED2677ABFFD1B33AC6FBC3062B71F1E8397C1505E1C42C64D11AD1B28FF73F4734"]
`)

	config, err := LoadConfig(Paths{Main: mainConfigPath, Validators: otherPath})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://from-explicit.example.com"}, config.Validators.ValidatorListSites)
}

func TestLoadValidatorsConfig_ExplicitPathIsAuthoritative(t *testing.T) {
	tempDir := t.TempDir()
	writeConfig(t, tempDir, "selected.txt", `[validators]
n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5
`)

	missingPath := filepath.Join(tempDir, "selected.toml")
	tests := []struct {
		name           string
		paths          Paths
		validatorsFile string
	}{
		{
			name:           "validators_file",
			paths:          Paths{Main: filepath.Join(tempDir, "xrpld.toml")},
			validatorsFile: "selected.toml",
		},
		{
			name:  "paths validators",
			paths: Paths{Validators: missingPath},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadValidatorsConfig(test.paths, test.validatorsFile)
			require.Error(t, err)
			assert.Contains(t, err.Error(), missingPath)
		})
	}
}

func TestLoadValidatorsConfig_ExplicitLegacyExtension(t *testing.T) {
	tempDir := t.TempDir()
	path := writeConfig(t, tempDir, "validators.cfg", `[validator_keys]
n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt legacy
`)

	validators, err := loadValidatorsConfig(Paths{Validators: path}, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt"}, validators.Validators)
}

func TestLoadConfig_ImplicitAdjacentValidatorsTxt(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
database_path = "/tmp/test/db"
network_id = "main"
debug_logfile = "/tmp/test/debug.log"

[server]
ports = ["port_test"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"

[node_db]
type = "pebble"
path = "/tmp/test/db"
`
	mainConfigPath := writeConfig(t, tempDir, "xrpld.toml", configContent)
	writeConfig(t, tempDir, "validators.txt", `[validator_list_sites]
https://implicit.example.com

[validator_list_keys]
ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860
`)

	config, err := LoadConfig(Paths{Main: mainConfigPath})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://implicit.example.com"}, config.Validators.ValidatorListSites)
	assert.Equal(t, []string{"ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860"}, config.Validators.ValidatorListKeys)
}

func TestLoadConfig_SkipValidators(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
database_path = "/tmp/test/db"
network_id = "main"
debug_logfile = "/tmp/test/debug.log"
validators_file = "missing.toml"

[server]
ports = ["port_test"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"

[node_db]
type = "pebble"
path = "/tmp/test/db"
`
	mainConfigPath := writeConfig(t, tempDir, "xrpld.toml", configContent)

	config, err := LoadConfig(Paths{Main: mainConfigPath, SkipValidators: true})
	require.NoError(t, err)
	assert.Empty(t, config.Validators.Validators)
	assert.Empty(t, config.Validators.ValidatorListKeys)
}

func TestLoadConfig_IgnoresImplicitValidatorsDirectory(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
database_path = "/tmp/test/db"
network_id = "main"
debug_logfile = "/tmp/test/debug.log"

[server]
ports = ["port_test"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"

[node_db]
type = "pebble"
path = "/tmp/test/db"
`
	mainConfigPath := writeConfig(t, tempDir, "xrpld.toml", configContent)
	require.NoError(t, os.Mkdir(filepath.Join(tempDir, "validators.txt"), 0o700))

	config, err := LoadConfig(Paths{Main: mainConfigPath})
	require.NoError(t, err)
	assert.Empty(t, config.Validators.Validators)
	assert.Empty(t, config.Validators.ValidatorListKeys)
}

func TestLoadConfig_RejectsImplicitSitesWithoutKeys(t *testing.T) {
	tempDir := t.TempDir()
	configContent := `
database_path = "/tmp/test/db"
network_id = "main"
debug_logfile = "/tmp/test/debug.log"

[server]
ports = ["port_test"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"

[node_db]
type = "pebble"
path = "/tmp/test/db"
`
	mainConfigPath := writeConfig(t, tempDir, "xrpld.toml", configContent)
	writeConfig(t, tempDir, "validators.txt", `[validator_list_sites]
https://invalid.example.com
`)

	_, err := LoadConfig(Paths{Main: mainConfigPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validator_list_sites requires")
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig(Paths{Main: "/nonexistent/path/xrpld.toml"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config file does not exist")
}

func TestLoadConfig_MissingValidatorsFile(t *testing.T) {
	tempDir := t.TempDir()

	configContent := `
database_path = "/tmp/test/db"
network_id = "main"
debug_logfile = "/tmp/test/debug.log"
validators_file = "/nonexistent/validators.toml"

[server]
ports = ["port_test"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"

[node_db]
type = "pebble"
path = "/tmp/test/db"
`
	mainConfigPath := writeConfig(t, tempDir, "test_config.toml", configContent)

	_, err := LoadConfig(Paths{Main: mainConfigPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validators file not found")
}

func TestConfigValidation_MissingRequiredFields(t *testing.T) {
	// Empty config should report ALL missing required fields — and only
	// keys that actual consumers read are required.
	config := &Config{
		Ports: map[string]PortConfig{},
	}

	err := ValidateConfig(config)
	require.Error(t, err)

	errMsg := err.Error()
	assert.Contains(t, errMsg, "server.ports")
	assert.Contains(t, errMsg, "node_db.type")
	assert.Contains(t, errMsg, "node_db.path")
	assert.Contains(t, errMsg, "database_path")
	assert.Contains(t, errMsg, "network_id")
	assert.Contains(t, errMsg, "debug_logfile")

	// Demoted-to-optional keys must NOT be reported as missing.
	for _, gone := range []string{
		"ledger_history", "fetch_depth", "node_size",
		"relay_proposals", "relay_validations", "max_transactions",
		"overlay.", "transaction_queue.", "sqlite.",
	} {
		assert.NotContains(t, errMsg, "missing required field: "+gone)
	}
}

func TestConfigLocalStateDir(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "filesystem database path",
			cfg:  Config{DatabasePath: "/var/lib/xrpld/db"},
			want: "/var/lib/xrpld/db",
		},
		{
			name: "PostgreSQL uses node store parent",
			cfg: Config{
				DatabasePath: "postgres://user:secret@db.example/xrpl",
				NodeDB:       NodeDBConfig{Path: "/var/lib/xrpld/db/pebble"},
			},
			want: "/var/lib/xrpld/db",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, test.cfg.LocalStateDir())
		})
	}
}

func validCompleteConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Ports: []string{"test_port"},
		},
		Ports: map[string]PortConfig{
			"test_port": {
				Port:     8080,
				IP:       "127.0.0.1",
				Protocol: "http",
			},
		},
		NodeDB: NodeDBConfig{
			Type: "pebble",
			Path: "/tmp/test",
		},
		DatabasePath:     "/tmp/test",
		NetworkID:        NetworkID{Set: true, Name: "main"},
		LedgerHistory:    LedgerHistory{Set: true, Count: 256},
		FetchDepth:       FetchDepth{Set: true, Full: true},
		NodeSize:         "tiny",
		DebugLogfile:     "/tmp/debug.log",
		RelayProposals:   "trusted",
		RelayValidations: "all",
		MaxTransactions:  250,
		Overlay: OverlayConfig{
			MaxUnknownTime:  600,
			MaxDivergedTime: 300,
		},
		TransactionQueue: TransactionQueueConfig{
			LedgersInQueue:                 intPtr(20),
			MinimumQueueSize:               intPtr(2000),
			RetrySequencePercent:           intPtr(25),
			MinimumEscalationMultiplier:    intPtr(128000),
			MinimumTxnInLedger:             intPtr(32),
			MinimumTxnInLedgerStandalone:   intPtr(1000),
			TargetTxnInLedger:              intPtr(256),
			NormalConsensusIncreasePercent: intPtr(20),
			SlowConsensusDecreasePercent:   intPtr(50),
			MaximumTxnPerAccount:           intPtr(10),
			MinimumLastLedgerBuffer:        intPtr(2),
		},
		SQLite: SQLiteConfig{
			JournalMode:      "wal",
			Synchronous:      "normal",
			TempStore:        "file",
			PageSize:         4096,
			JournalSizeLimit: 1582080,
		},
	}
}

func TestOverlayConfig_VerifyEndpointsValidation(t *testing.T) {
	cases := []struct {
		name    string
		value   *int
		wantErr bool
	}{
		{"absent", nil, false},
		{"zero", intPtr(0), false},
		{"one", intPtr(1), false},
		{"two_rejected", intPtr(2), true},
		{"negative_rejected", intPtr(-1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := OverlayConfig{VerifyEndpoints: tc.value}
			err := o.Validate()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "verify_endpoints")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConfigValidation_CompleteConfig(t *testing.T) {
	assert.NoError(t, ValidateConfig(validCompleteConfig()))
}

func TestConfigValidation_InvalidPort(t *testing.T) {
	config := validCompleteConfig()
	config.Ports = map[string]PortConfig{
		"invalid_port": {
			Port:     99999,
			IP:       "127.0.0.1",
			Protocol: "http",
		},
	}

	err := ValidateConfig(config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "port number must be between 1 and 65535")
}

// TestConfigValidation_MultiplePortErrorsReported verifies the "ALL errors
// at once" contract: two broken ports yield two distinct errors in a
// single validation pass.
func TestConfigValidation_MultiplePortErrorsReported(t *testing.T) {
	config := validCompleteConfig()
	config.Ports = map[string]PortConfig{
		"bad_port_a": {Port: 99999, IP: "127.0.0.1", Protocol: "http"},
		"bad_port_b": {Port: 8080, IP: "", Protocol: "http"},
	}

	err := ValidateConfig(config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad_port_a")
	assert.Contains(t, err.Error(), "bad_port_b")
}

// TestConfigValidation_SeedAndTokenMutuallyExclusive mirrors rippled's
// hard error (Config.cpp:635-638).
func TestConfigValidation_SeedAndTokenMutuallyExclusive(t *testing.T) {
	config := validCompleteConfig()
	config.Ports["peer_port"] = PortConfig{Port: 51235, IP: "0.0.0.0", Protocol: "peer"}
	config.ValidationSeed = "ssZkdwURFMBXenJPbrpE14b6noJSu"
	config.ValidatorToken = "some-token"

	err := ValidateConfig(config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot have both [validation_seed] and [validator_token]")

	config.ValidatorToken = ""
	assert.NoError(t, ValidateConfig(config))
}

// TestConfigValidation_RelayCaseInsensitive matches rippled's
// boost::iequals comparison (Config.cpp:607-633).
func TestConfigValidation_RelayCaseInsensitive(t *testing.T) {
	config := validCompleteConfig()
	config.RelayProposals = "ALL"
	config.RelayValidations = "Drop_Untrusted"
	assert.NoError(t, ValidateConfig(config))

	config.RelayProposals = "sometimes"
	err := ValidateConfig(config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid relay_proposals")
}

func TestConfigValidation_OverlayPublicIP(t *testing.T) {
	config := validCompleteConfig()
	config.Overlay.PublicIP = "203.0.113.7"
	assert.NoError(t, ValidateConfig(config))

	config.Overlay.PublicIP = "not-an-ip"
	err := ValidateConfig(config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public_ip")
}

// TestConfigValidation_TxQMaximumBelowMinimum mirrors rippled's
// setup_TxQ hard errors (TxQ.cpp:1930-1951).
func TestConfigValidation_TxQMaximumBelowMinimum(t *testing.T) {
	config := validCompleteConfig()
	config.TransactionQueue.MaximumTxnInLedger = intPtr(10) // below minimum_txn_in_ledger = 32

	err := ValidateConfig(config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minimum_txn_in_ledger (32) exceeds maximum_txn_in_ledger (10)")
}

// TestValidatePortString_TrailingGarbage: Sscanf used to accept
// "51235abc"; strconv.Atoi must reject it.
func TestValidatePortString_TrailingGarbage(t *testing.T) {
	require.NoError(t, validatePortString("51235"))
	require.Error(t, validatePortString("51235abc"))
	require.Error(t, validatePortString(""))
	require.Error(t, validatePortString("0"))
}

func TestConfigHelperMethods(t *testing.T) {
	config := &Config{
		NetworkID:     NetworkID{Set: true, Name: "main"},
		LedgerHistory: LedgerHistory{Set: true, Count: 1000},
		FetchDepth:    FetchDepth{Set: true, Full: true},
	}

	networkID, err := config.ResolvedNetworkID()
	assert.NoError(t, err)
	assert.Equal(t, 0, networkID)

	assert.Equal(t, 1000, config.ResolvedLedgerHistory())
	assert.Equal(t, math.MaxInt32, config.ResolvedFetchDepth()) // "full" maps to MaxInt32
}

func TestConfigHelperMethods_Defaults(t *testing.T) {
	config := &Config{}

	_, err := config.ResolvedNetworkID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required but not set")

	// Unset ledger_history / fetch_depth fall back to the rippled defaults.
	assert.Equal(t, 256, config.ResolvedLedgerHistory())
	assert.Equal(t, defaultFetchDepth, config.ResolvedFetchDepth())
}

func TestPortConfigMethods(t *testing.T) {
	port := PortConfig{
		Port:     8080,
		IP:       "127.0.0.1",
		Protocol: "peer",
	}

	assert.True(t, port.HasPeer())
	assert.Equal(t, "127.0.0.1:8080", port.BindAddress())
}

func TestValidatorsConfigMethods(t *testing.T) {
	validators := ValidatorsConfig{
		ValidatorListKeys:      []string{"key1", "key2", "key3"},
		ValidatorListThreshold: 0,
	}

	threshold := validators.EffectiveListThreshold()
	assert.Equal(t, 2, threshold) // floor(3/2) + 1 = 2
}

func TestValidatorsConfigMethods_DeduplicatePublisherKeys(t *testing.T) {
	key := "ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860"
	validators := ValidatorsConfig{
		ValidatorListKeys: []string{key, strings.ToLower(key), key},
	}

	assert.NoError(t, validators.Validate())
	assert.Equal(t, 1, validators.EffectiveListThreshold())

	validators.ValidatorListThreshold = 2
	err := validators.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unique validator_list_keys (1)")
}

func TestParseValidatorsTxt(t *testing.T) {
	content := `
# This is a comment
[validators]
n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5
n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt

[validator_list_sites]
https://vl.ripple.com

[validator_list_keys]
ED2677ABFFD1B33AC6FBC3062B71F1E8397C1505E1C42C64D11AD1B28FF73F4734
`

	config, err := ParseValidatorsTxt(content)
	require.NoError(t, err)

	assert.Len(t, config.Validators, 2)
	assert.Contains(t, config.Validators, "n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5")
	assert.Contains(t, config.ValidatorListSites, "https://vl.ripple.com")
	assert.Contains(t, config.ValidatorListKeys, "ED2677ABFFD1B33AC6FBC3062B71F1E8397C1505E1C42C64D11AD1B28FF73F4734")
}

func TestLoadValidatorsTxtFile_LegacyValidatorKeys(t *testing.T) {
	tempDir := t.TempDir()
	path := writeConfig(t, tempDir, "validators.txt", `[validator_keys]
n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt legacy
`)

	config, err := loadValidatorsTxtFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt"}, config.Validators)
	assert.NoError(t, config.Validate())
}

func TestParseValidatorsTxt_MergesLegacyValidatorKeys(t *testing.T) {
	config, err := ParseValidatorsTxt(`[validator_keys]
n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt legacy

[validators]
n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5 current
`)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5",
		"n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt",
	}, config.Validators)
}

// TestParseValidatorsTxt_Nicknames covers rippled's documented format
// (ValidatorList.cpp:145-155): `<key> [optional comment/nickname]`. The
// nickname must be stripped, leaving a key that passes validation.
func TestParseValidatorsTxt_Nicknames(t *testing.T) {
	content := `
[validators]
n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5    ValidatorOne
n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt some long comment
`

	config, err := ParseValidatorsTxt(content)
	require.NoError(t, err)

	require.Len(t, config.Validators, 2)
	assert.Equal(t, "n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5", config.Validators[0])
	assert.Equal(t, "n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt", config.Validators[1])
	assert.NoError(t, config.Validate())
}

// TestParseValidatorsTxt_BadThreshold: parse errors must propagate
// instead of being silently discarded.
func TestParseValidatorsTxt_BadThreshold(t *testing.T) {
	_, err := ParseValidatorsTxt("[validator_list_threshold]\nnot-a-number\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validator_list_threshold")

	config, err := ParseValidatorsTxt("[validator_list_threshold]\n2\n")
	require.NoError(t, err)
	assert.Equal(t, 2, config.ValidatorListThreshold)
}

func TestParseValidatorsTxt_ThresholdRequiresSingleWholeValue(t *testing.T) {
	for _, content := range []string{
		"[validator_list_threshold]\n1\n2\n",
		"[validator_list_threshold]\n1 2\n",
	} {
		_, err := ParseValidatorsTxt(content)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "validator_list_threshold")
	}

	config, err := ParseValidatorsTxt("[validator_list_threshold]\n2 # operator comment\n")
	require.NoError(t, err)
	assert.Equal(t, 2, config.ValidatorListThreshold)
}

func TestParseValidatorsTxt_EmptyThresholdUsesDefault(t *testing.T) {
	for _, content := range []string{
		"[validator_list_threshold]\n",
		"[validator_list_threshold]\n# operator comment\n",
	} {
		config, err := ParseValidatorsTxt(content)
		require.NoError(t, err)
		assert.Equal(t, 0, config.ValidatorListThreshold)
	}
}

func TestParseValidatorsTxt_ExactSectionBrackets(t *testing.T) {
	config, err := ParseValidatorsTxt(`[[validators]]
n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5
`)
	require.NoError(t, err)
	assert.Empty(t, config.Validators)
}

func TestParseValidatorsTxt_LineEndings(t *testing.T) {
	lines := []string{
		"[validators]",
		"n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5",
		"[validator_keys]",
		"n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt",
	}
	for _, separator := range []string{"\r\n", "\r"} {
		config, err := ParseValidatorsTxt(strings.Join(lines, separator))
		require.NoError(t, err)
		assert.Equal(t, []string{
			"n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5",
			"n9MqiExBcoG19UXwoLjBJnhsxEhAZMuWwJDRdkyDz1EkEkwzQTNt",
		}, config.Validators)
	}
}

// TestExampleConfigLoads keeps config/examples/xrpld.toml loadable by
// the strict loader, so the shipped example never drifts from the schema.
func TestExampleConfigLoads(t *testing.T) {
	_, err := LoadConfig(Paths{Main: filepath.Join("examples", "xrpld.toml")})
	require.NoError(t, err)
}
