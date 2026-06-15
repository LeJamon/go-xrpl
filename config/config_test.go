package config

import (
	"math"
	"os"
	"path/filepath"
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

	config, err := LoadConfig(ConfigPaths{Main: mainConfigPath})
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

// TestLoadConfig_MinimalConfig verifies that the optional tuning sections
// ([overlay], [transaction_queue], [sqlite], ledger_history, fetch_depth,
// node_size, relay_*, max_transactions) may be omitted entirely.
func TestLoadConfig_MinimalConfig(t *testing.T) {
	tempDir := t.TempDir()
	mainConfigPath := writeConfig(t, tempDir, "xrpld.toml", minimalTestConfig())

	config, err := LoadConfig(ConfigPaths{Main: mainConfigPath})
	require.NoError(t, err)
	require.NotNil(t, config)

	assert.Equal(t, 256, config.GetLedgerHistory())
	assert.Equal(t, defaultFetchDepth, config.GetFetchDepth())
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
	config, err := LoadConfig(ConfigPaths{Main: mainConfigPath})
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
	writeConfig(t, tempDir, "explicit_validators.toml",
		`validator_list_sites = ["https://from-explicit.example.com"]`)
	otherPath := writeConfig(t, tempDir, "other_validators.toml",
		`validator_list_sites = ["https://from-paths.example.com"]`)

	config, err := LoadConfig(ConfigPaths{Main: mainConfigPath, Validators: otherPath})
	require.NoError(t, err)
	assert.Equal(t, []string{"https://from-explicit.example.com"}, config.Validators.ValidatorListSites)
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig(ConfigPaths{Main: "/nonexistent/path/xrpld.toml"})
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

	_, err := LoadConfig(ConfigPaths{Main: mainConfigPath})
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

	networkID, err := config.GetNetworkID()
	assert.NoError(t, err)
	assert.Equal(t, 0, networkID)

	assert.Equal(t, 1000, config.GetLedgerHistory())
	assert.Equal(t, math.MaxInt32, config.GetFetchDepth()) // "full" maps to MaxInt32
}

func TestConfigHelperMethods_Defaults(t *testing.T) {
	config := &Config{}

	_, err := config.GetNetworkID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required but not set")

	// Unset ledger_history / fetch_depth fall back to the rippled defaults.
	assert.Equal(t, 256, config.GetLedgerHistory())
	assert.Equal(t, defaultFetchDepth, config.GetFetchDepth())
}

func TestPortConfigMethods(t *testing.T) {
	port := PortConfig{
		Port:     8080,
		IP:       "127.0.0.1",
		Protocol: "peer",
	}

	assert.True(t, port.HasPeer())
	assert.Equal(t, "127.0.0.1:8080", port.GetBindAddress())
}

func TestValidatorsConfigMethods(t *testing.T) {
	validators := ValidatorsConfig{
		ValidatorListKeys:      []string{"key1", "key2", "key3"},
		ValidatorListThreshold: 0,
	}

	threshold := validators.GetValidatorListThreshold()
	assert.Equal(t, 2, threshold) // floor(3/2) + 1 = 2
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

// TestExampleConfigLoads keeps config/examples/xrpld.toml loadable by
// the strict loader, so the shipped example never drifts from the schema.
func TestExampleConfigLoads(t *testing.T) {
	_, err := LoadConfig(ConfigPaths{Main: filepath.Join("examples", "xrpld.toml")})
	require.NoError(t, err)
}
