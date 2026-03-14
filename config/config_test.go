package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
workers = 0
io_workers = 0
prefetch_workers = 0
path_search = 2
path_search_fast = 2
path_search_max = 3
path_search_old = 2
ssl_verify = 1
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

[sqlite]
journal_mode = "wal"
synchronous = "normal"
temp_store = "file"
page_size = 4096
journal_size_limit = 1582080
`
}

func TestLoadConfig(t *testing.T) {
	tempDir := t.TempDir()

	mainConfigPath := filepath.Join(tempDir, "test_config.toml")
	err := os.WriteFile(mainConfigPath, []byte(completeTestConfig()), 0644)
	require.NoError(t, err)

	paths := ConfigPaths{
		Main: mainConfigPath,
	}

	config, err := LoadConfig(paths)
	require.NoError(t, err)
	require.NotNil(t, config)

	assert.Equal(t, []string{"port_test"}, config.Server.Ports)
	assert.Equal(t, "pebble", config.NodeDB.Type)
	assert.Equal(t, "/tmp/test/db", config.NodeDB.Path)

	portConfig, exists := config.GetPort("port_test")
	assert.True(t, exists)
	assert.Equal(t, 8080, portConfig.Port)
	assert.Equal(t, "127.0.0.1", portConfig.IP)
	assert.Equal(t, "http", portConfig.Protocol)
}

func TestLoadConfig_WithValidators(t *testing.T) {
	tempDir := t.TempDir()

	// Insert validators_file as a top-level key by replacing the first line after the comment
	configContent := `
# Top-level fields
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
workers = 0
io_workers = 0
prefetch_workers = 0
path_search = 2
path_search_fast = 2
path_search_max = 3
path_search_old = 2
ssl_verify = 1
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

[sqlite]
journal_mode = "wal"
synchronous = "normal"
temp_store = "file"
page_size = 4096
journal_size_limit = 1582080
`
	mainConfigPath := filepath.Join(tempDir, "test_config.toml")
	err := os.WriteFile(mainConfigPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Create validators file in the same dir
	validatorsContent := `
validator_list_sites = ["https://test.example.com"]
validator_list_keys = ["ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D58"]
validator_list_threshold = 1
`
	validatorsPath := filepath.Join(tempDir, "test_validators.toml")
	err = os.WriteFile(validatorsPath, []byte(validatorsContent), 0644)
	require.NoError(t, err)

	paths := ConfigPaths{
		Main:       mainConfigPath,
		Validators: validatorsPath,
	}

	config, err := LoadConfig(paths)
	require.NoError(t, err)
	require.NotNil(t, config)

	assert.Equal(t, []string{"https://test.example.com"}, config.Validators.ValidatorListSites)
	assert.Equal(t, []string{"ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D58"}, config.Validators.ValidatorListKeys)
	assert.Equal(t, 1, config.Validators.ValidatorListThreshold)
}

func TestLoadConfig_MissingFile(t *testing.T) {
	paths := ConfigPaths{
		Main: "/nonexistent/path/xrpld.toml",
	}

	_, err := LoadConfig(paths)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config file does not exist")
}

func TestLoadConfig_MissingValidatorsFile(t *testing.T) {
	tempDir := t.TempDir()

	// Config with validators_file as a top-level key pointing to nonexistent file
	configContent := `
# Top-level fields
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
workers = 0
io_workers = 0
prefetch_workers = 0
path_search = 2
path_search_fast = 2
path_search_max = 3
path_search_old = 2
ssl_verify = 1
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

[sqlite]
journal_mode = "wal"
synchronous = "normal"
temp_store = "file"
page_size = 4096
journal_size_limit = 1582080
`
	mainConfigPath := filepath.Join(tempDir, "test_config.toml")
	err := os.WriteFile(mainConfigPath, []byte(configContent), 0644)
	require.NoError(t, err)

	paths := ConfigPaths{
		Main: mainConfigPath,
	}

	_, err = LoadConfig(paths)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validators file not found")
}

func TestConfigValidation_MissingRequiredFields(t *testing.T) {
	// Empty config should report ALL missing fields
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
	assert.Contains(t, errMsg, "ledger_history")
	assert.Contains(t, errMsg, "fetch_depth")
	assert.Contains(t, errMsg, "node_size")
	assert.Contains(t, errMsg, "debug_logfile")
	assert.Contains(t, errMsg, "relay_proposals")
	assert.Contains(t, errMsg, "relay_validations")
	assert.Contains(t, errMsg, "max_transactions")
	assert.Contains(t, errMsg, "overlay.max_unknown_time")
	assert.Contains(t, errMsg, "overlay.max_diverged_time")
	assert.Contains(t, errMsg, "sqlite.journal_mode")
}

func TestConfigValidation_CompleteConfig(t *testing.T) {
	config := &Config{
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
		NetworkID:        "main",
		LedgerHistory:    256,
		FetchDepth:       "full",
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
			LedgersInQueue:                 20,
			MinimumQueueSize:               2000,
			RetrySequencePercent:           25,
			MinimumEscalationMultiplier:    500,
			MinimumTxnInLedger:             5,
			MinimumTxnInLedgerStandalone:   1000,
			TargetTxnInLedger:              50,
			NormalConsensusIncreasePercent: 20,
			SlowConsensusDecreasePercent:   50,
			MaximumTxnPerAccount:           10,
			MinimumLastLedgerBuffer:        2,
			ZeroBaseFeeTransactionFeeLevel: 256000,
		},
		SQLite: SQLiteConfig{
			JournalMode:      "wal",
			Synchronous:      "normal",
			TempStore:        "file",
			PageSize:         4096,
			JournalSizeLimit: 1582080,
		},
	}

	err := ValidateConfig(config)
	assert.NoError(t, err)
}

func TestConfigValidation_InvalidPort(t *testing.T) {
	config := &Config{
		Server: ServerConfig{
			Ports: []string{"invalid_port"},
		},
		Ports: map[string]PortConfig{
			"invalid_port": {
				Port:     99999,
				IP:       "127.0.0.1",
				Protocol: "http",
			},
		},
		NodeDB: NodeDBConfig{
			Type: "pebble",
			Path: "/tmp/test",
		},
		DatabasePath:     "/tmp/test",
		NetworkID:        "main",
		LedgerHistory:    256,
		FetchDepth:       "full",
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
			LedgersInQueue:                 20,
			MinimumQueueSize:               2000,
			RetrySequencePercent:           25,
			MinimumEscalationMultiplier:    500,
			MinimumTxnInLedger:             5,
			MinimumTxnInLedgerStandalone:   1000,
			TargetTxnInLedger:              50,
			NormalConsensusIncreasePercent: 20,
			SlowConsensusDecreasePercent:   50,
			MaximumTxnPerAccount:           10,
			MinimumLastLedgerBuffer:        2,
			ZeroBaseFeeTransactionFeeLevel: 256000,
		},
		SQLite: SQLiteConfig{
			JournalMode:      "wal",
			Synchronous:      "normal",
			TempStore:        "file",
			PageSize:         4096,
			JournalSizeLimit: 1582080,
		},
	}

	err := ValidateConfig(config)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "port number must be between 1 and 65535")
}

func TestNetworkDefaults(t *testing.T) {
	mainnetConfig := GetDefaultValidatorsConfig("main")
	assert.Contains(t, mainnetConfig.ValidatorListSites, "https://vl.ripple.com")
	assert.Contains(t, mainnetConfig.ValidatorListKeys, "ED2677ABFFD1B33AC6FBC3062B71F1E8397C1505E1C42C64D11AD1B28FF73F4734")

	testnetConfig := GetDefaultValidatorsConfig("testnet")
	assert.Contains(t, testnetConfig.ValidatorListSites, "https://vl.altnet.rippletest.net")
	assert.Contains(t, testnetConfig.ValidatorListKeys, "ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860")
}

func TestConfigHelperMethods(t *testing.T) {
	config := &Config{
		NetworkID:     "main",
		LedgerHistory: 1000,
		FetchDepth:    "full",
	}

	networkID, err := config.GetNetworkID()
	assert.NoError(t, err)
	assert.Equal(t, 0, networkID)

	ledgerHistory, err := config.GetLedgerHistory()
	assert.NoError(t, err)
	assert.Equal(t, 1000, ledgerHistory)

	fetchDepth, err := config.GetFetchDepth()
	assert.NoError(t, err)
	assert.Equal(t, -1, fetchDepth) // -1 means "full"
}

func TestConfigHelperMethods_NilErrors(t *testing.T) {
	config := &Config{}

	_, err := config.GetNetworkID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required but not set")

	_, err = config.GetLedgerHistory()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required but not set")

	_, err = config.GetFetchDepth()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "required but not set")
}

func TestPortConfigMethods(t *testing.T) {
	port := PortConfig{
		Port:     8080,
		IP:       "127.0.0.1",
		Protocol: "https,ws",
		Admin:    []string{"127.0.0.1"},
		SSLKey:   "/path/to/key",
		SSLCert:  "/path/to/cert",
	}

	assert.True(t, port.HasHTTPS())
	assert.True(t, port.HasWebSocket())
	assert.True(t, port.IsSecure())
	assert.True(t, port.IsAdminPort())
	assert.True(t, port.HasSSLConfig())
	assert.Equal(t, "127.0.0.1:8080", port.GetBindAddress())
}

func TestValidatorsConfigMethods(t *testing.T) {
	validators := ValidatorsConfig{
		ValidatorListKeys:      []string{"key1", "key2", "key3"},
		ValidatorListThreshold: 0,
	}

	threshold := validators.GetValidatorListThreshold()
	assert.Equal(t, 2, threshold) // floor(3/2) + 1 = 2

	assert.True(t, validators.HasValidatorListKeys())
	assert.Equal(t, 3, validators.GetValidatorListKeyCount())
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
