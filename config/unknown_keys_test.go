package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_RejectsUnknownKeys(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(string) string
		unknown []string
	}{
		{
			name:    "top-level key",
			mutate:  func(c string) string { return "peers_maxx = 21\n" + c },
			unknown: []string{"peers_maxx"},
		},
		{
			name:    "top-level table",
			mutate:  func(c string) string { return c + "\n[node_dbb]\ntype = \"pebble\"\n" },
			unknown: []string{"node_dbb"},
		},
		{
			name:    "nested key",
			mutate:  func(c string) string { return c + "online_delette = 512\n" },
			unknown: []string{"node_db.online_delette"},
		},
		{
			name:    "doubly nested key",
			mutate:  func(c string) string { return c + "\n[overlay.resource_limits]\nmax_entriess = 10\n" },
			unknown: []string{"overlay.resource_limits.max_entriess"},
		},
		{
			name: "port section key",
			mutate: func(c string) string {
				return strings.Replace(c, "protocol = \"http\"", "protocol = \"http\"\nadmn = [\"127.0.0.1\"]", 1)
			},
			unknown: []string{"port_test.admn"},
		},
		{
			name: "all unknown keys reported together",
			mutate: func(c string) string {
				return "zeta = 1\nalpha = 2\n" + c + "cache_mbb = 1\n"
			},
			unknown: []string{"alpha", "node_db.cache_mbb", "zeta"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), "goxrpl.toml", tt.mutate(minimalTestConfig()))

			_, err := LoadConfig(Paths{Main: path})

			var unknownErr *UnknownKeysError
			require.ErrorAs(t, err, &unknownErr)
			assert.Equal(t, path, unknownErr.File)
			assert.Equal(t, tt.unknown, unknownErr.Keys)
			for _, key := range tt.unknown {
				assert.Contains(t, err.Error(), key)
			}
		})
	}
}

func TestLoadConfig_AcceptsFreeFormSections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{
			name: "logging partitions take arbitrary names",
			mutate: func(c string) string {
				return c + "\n[logging]\nlevel = \"info\"\n\n[logging.partitions]\nAnyPartition = \"debug\"\n"
			},
		},
		{
			name: "port section named freely in server.ports",
			mutate: func(c string) string {
				return strings.ReplaceAll(c, "port_test", "Admin_RPC")
			},
		},
		{
			name: "port_ section not listed in server.ports",
			mutate: func(c string) string {
				return c + "\n[port_unused]\nport = 9090\nnot_a_port_key = true\n"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), "goxrpl.toml", tt.mutate(minimalTestConfig()))

			_, err := LoadConfig(Paths{Main: path})

			require.NoError(t, err)
		})
	}
}

func TestLoadConfig_RejectsUnknownValidatorsFileKey(t *testing.T) {
	dir := t.TempDir()
	validatorsPath := writeConfig(t, dir, "validators.toml", `
validators = ["n9KorY8QtTdRx7TVDpwnG9NvyxsDwHUKUEeDLY3AkiGncVaSXZi5"]
validator_list_site = ["https://vl.example.com"]
`)
	path := writeConfig(t, dir, "goxrpl.toml", "validators_file = \"validators.toml\"\n"+minimalTestConfig())

	_, err := LoadConfig(Paths{Main: path})

	var unknownErr *UnknownKeysError
	require.ErrorAs(t, err, &unknownErr)
	assert.Equal(t, validatorsPath, unknownErr.File)
	assert.Equal(t, []string{"validator_list_site"}, unknownErr.Keys)
}

// TestTrackedExampleConfigsLoad keeps every node configuration tracked in the
// repository loadable. The smoke configuration names a validators file by its
// in-container path, so its validators file is loaded on its own.
func TestTrackedExampleConfigsLoad(t *testing.T) {
	root := filepath.Join("..")
	smoke := filepath.Join(root, "scripts", "consensus-smoke", "configs")

	_, err := LoadConfig(Paths{Main: filepath.Join("examples", "goxrpl.toml")})
	require.NoError(t, err)

	_, err = LoadConfig(Paths{Main: filepath.Join(smoke, "goxrpl-0.toml"), SkipValidators: true})
	require.NoError(t, err)

	for _, validators := range []string{
		filepath.Join("examples", "validators.toml"),
		filepath.Join(smoke, "validators.toml"),
	} {
		_, err := loadValidatorsTomlFile(validators)
		require.NoError(t, err, validators)
	}
}

// TestOperatingGuideSnippetsUseKnownKeys checks the TOML fragments in
// docs/operating.md against the same key set the loader enforces.
func TestOperatingGuideSnippetsUseKnownKeys(t *testing.T) {
	guide, err := os.ReadFile(filepath.Join("..", "docs", "operating.md"))
	require.NoError(t, err)

	snippets := regexp.MustCompile("(?s)```toml\n(.*?)```").FindAllSubmatch(guide, -1)
	require.NotEmpty(t, snippets)

	for _, snippet := range snippets {
		v := viper.New()
		v.SetConfigType("toml")
		require.NoError(t, v.ReadConfig(strings.NewReader(string(snippet[1]))), string(snippet[1]))

		_, unknown, err := decodeConfig(v)

		require.NoError(t, err, string(snippet[1]))
		assert.Empty(t, unknown, string(snippet[1]))
	}
}
