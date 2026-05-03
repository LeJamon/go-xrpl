package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// baseTOMLWithoutUnionFields returns a complete-but-not-quite TOML config
// for use as a fixture in TOML decode tests. Callers prepend the union
// fields they want to exercise.
func baseTOMLWithoutUnionFields() string {
	return `
database_path = "/tmp/test/db"
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
online_delete = 0
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

func writeAndLoad(t *testing.T, contents string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "xrpld.toml")
	require.NoError(t, os.WriteFile(p, []byte(contents), 0o644))
	return LoadConfig(ConfigPaths{Main: p})
}

func TestTypedFields_LedgerHistory_Integer(t *testing.T) {
	toml := "ledger_history = 1024\nfetch_depth = \"full\"\nnetwork_id = \"main\"\n" + baseTOMLWithoutUnionFields()
	cfg, err := writeAndLoad(t, toml)
	require.NoError(t, err)

	assert.True(t, cfg.LedgerHistory.Set)
	assert.False(t, cfg.LedgerHistory.Full)
	assert.Equal(t, 1024, cfg.LedgerHistory.Count)
	assert.Equal(t, 1024, cfg.LedgerHistory.Value())

	got, err := cfg.GetLedgerHistory()
	require.NoError(t, err)
	assert.Equal(t, 1024, got)
}

func TestTypedFields_LedgerHistory_Full(t *testing.T) {
	toml := "ledger_history = \"full\"\nfetch_depth = \"full\"\nnetwork_id = \"main\"\n" + baseTOMLWithoutUnionFields()
	cfg, err := writeAndLoad(t, toml)
	require.NoError(t, err)

	assert.True(t, cfg.LedgerHistory.Set)
	assert.True(t, cfg.LedgerHistory.Full)
	assert.Equal(t, -1, cfg.LedgerHistory.Value())

	got, err := cfg.GetLedgerHistory()
	require.NoError(t, err)
	assert.Equal(t, -1, got)
}

func TestTypedFields_LedgerHistory_None(t *testing.T) {
	toml := "ledger_history = \"none\"\nfetch_depth = \"full\"\nnetwork_id = \"main\"\n" + baseTOMLWithoutUnionFields()
	cfg, err := writeAndLoad(t, toml)
	require.NoError(t, err)

	assert.True(t, cfg.LedgerHistory.Set)
	assert.False(t, cfg.LedgerHistory.Full)
	assert.Equal(t, 0, cfg.LedgerHistory.Count)
}

func TestTypedFields_LedgerHistory_Invalid(t *testing.T) {
	toml := "ledger_history = \"sometimes\"\nfetch_depth = \"full\"\nnetwork_id = \"main\"\n" + baseTOMLWithoutUnionFields()
	_, err := writeAndLoad(t, toml)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ledger_history")
}

func TestTypedFields_FetchDepth_Integer(t *testing.T) {
	toml := "ledger_history = 256\nfetch_depth = 512\nnetwork_id = \"main\"\n" + baseTOMLWithoutUnionFields()
	cfg, err := writeAndLoad(t, toml)
	require.NoError(t, err)

	assert.True(t, cfg.FetchDepth.Set)
	assert.False(t, cfg.FetchDepth.Full)
	assert.Equal(t, 512, cfg.FetchDepth.Count)

	got, err := cfg.GetFetchDepth()
	require.NoError(t, err)
	assert.Equal(t, 512, got)
}

func TestTypedFields_FetchDepth_Full(t *testing.T) {
	toml := "ledger_history = 256\nfetch_depth = \"full\"\nnetwork_id = \"main\"\n" + baseTOMLWithoutUnionFields()
	cfg, err := writeAndLoad(t, toml)
	require.NoError(t, err)

	assert.True(t, cfg.FetchDepth.Set)
	assert.True(t, cfg.FetchDepth.Full)
	assert.Equal(t, -1, cfg.FetchDepth.Value())
}

func TestTypedFields_FetchDepth_Invalid(t *testing.T) {
	toml := "ledger_history = 256\nfetch_depth = \"deep\"\nnetwork_id = \"main\"\n" + baseTOMLWithoutUnionFields()
	_, err := writeAndLoad(t, toml)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid fetch_depth")
}

func TestTypedFields_NetworkID_Integer(t *testing.T) {
	toml := "ledger_history = 256\nfetch_depth = \"full\"\nnetwork_id = 21338\n" + baseTOMLWithoutUnionFields()
	cfg, err := writeAndLoad(t, toml)
	require.NoError(t, err)

	assert.True(t, cfg.NetworkID.Set)
	assert.Equal(t, 21338, cfg.NetworkID.ID)
	assert.Empty(t, cfg.NetworkID.Name)

	got, err := cfg.GetNetworkID()
	require.NoError(t, err)
	assert.Equal(t, 21338, got)
}

func TestTypedFields_NetworkID_NamedStrings(t *testing.T) {
	cases := map[string]int{
		"main":    0,
		"testnet": 1,
		"devnet":  2,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			toml := "ledger_history = 256\nfetch_depth = \"full\"\nnetwork_id = \"" + name + "\"\n" + baseTOMLWithoutUnionFields()
			cfg, err := writeAndLoad(t, toml)
			require.NoError(t, err)
			assert.Equal(t, name, cfg.NetworkID.Name)
			got, err := cfg.GetNetworkID()
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestTypedFields_NetworkID_UnknownName(t *testing.T) {
	toml := "ledger_history = 256\nfetch_depth = \"full\"\nnetwork_id = \"someothernet\"\n" + baseTOMLWithoutUnionFields()
	_, err := writeAndLoad(t, toml)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown network name")
}

func TestTypedFields_RPCStartup(t *testing.T) {
	toml := `
ledger_history = 256
fetch_depth = "full"
network_id = "main"

rpc_startup = [
	{ command = "log_level", severity = "warning" },
	{ command = "subscribe", streams = ["ledger"] },
]
` + baseTOMLWithoutUnionFields()
	cfg, err := writeAndLoad(t, toml)
	require.NoError(t, err)

	require.Len(t, cfg.RPCStartup, 2)

	assert.Equal(t, "log_level", cfg.RPCStartup[0].Command)
	assert.Equal(t, "warning", cfg.RPCStartup[0].Params["severity"])

	assert.Equal(t, "subscribe", cfg.RPCStartup[1].Command)
	streams, ok := cfg.RPCStartup[1].Params["streams"].([]any)
	require.True(t, ok, "expected streams to be []any, got %T", cfg.RPCStartup[1].Params["streams"])
	require.Len(t, streams, 1)
	assert.Equal(t, "ledger", streams[0])

	// AsMap round-trips back to the legacy shape.
	m := cfg.RPCStartup[0].AsMap()
	assert.Equal(t, "log_level", m["command"])
	assert.Equal(t, "warning", m["severity"])
}

func TestTypedFields_RPCStartup_MissingCommand(t *testing.T) {
	toml := `
ledger_history = 256
fetch_depth = "full"
network_id = "main"

rpc_startup = [{ severity = "warning" }]
` + baseTOMLWithoutUnionFields()
	_, err := writeAndLoad(t, toml)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing 'command'")
}

func TestTypedFields_AbsentFieldsReportedAsMissing(t *testing.T) {
	// Omit the three union fields entirely; loader must report them.
	_, err := writeAndLoad(t, baseTOMLWithoutUnionFields())
	require.Error(t, err)
	msg := err.Error()
	for _, want := range []string{"network_id", "ledger_history", "fetch_depth"} {
		assert.True(t, strings.Contains(msg, "missing required field: "+want), "expected missing-field error for %q in:\n%s", want, msg)
	}
}

func TestTypedFields_ZeroValueIsZero(t *testing.T) {
	var lh LedgerHistory
	assert.True(t, lh.IsZero())
	var fd FetchDepth
	assert.True(t, fd.IsZero())
	var nid NetworkID
	assert.True(t, nid.IsZero())

	lh = LedgerHistory{Set: true, Count: 1}
	assert.False(t, lh.IsZero())
}
