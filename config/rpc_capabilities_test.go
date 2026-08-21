package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRPCCapabilityConfigLoad(t *testing.T) {
	tests := []struct {
		name        string
		settings    string
		wantSigning bool
		wantPathMax int
		wantPathSet bool
	}{
		{name: "defaults", wantPathMax: 3},
		{name: "signing enabled", settings: "signing_support = true\n", wantSigning: true, wantPathMax: 3},
		{name: "explicit zero", settings: "path_search_max = 0\n", wantPathSet: true},
		{name: "explicit limit", settings: "path_search_max = 7\n", wantPathMax: 7, wantPathSet: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), "goxrpl.toml", test.settings+minimalTestConfig())
			cfg, err := LoadConfig(Paths{Main: path, SkipValidators: true})
			require.NoError(t, err)
			require.Equal(t, test.wantSigning, cfg.SigningSupport)
			require.Equal(t, test.wantPathMax, cfg.ResolvedPathSearchMax())
			if test.wantPathSet {
				require.NotNil(t, cfg.PathSearchMax)
			} else {
				require.Nil(t, cfg.PathSearchMax)
			}
		})
	}
}

func TestResolvedPathSearchMaxValidatorDefault(t *testing.T) {
	explicit := 5
	tests := []struct {
		name string
		cfg  Config
		want int
	}{
		{name: "non-validator", cfg: Config{}, want: 3},
		{name: "validation seed", cfg: Config{ValidationSeed: "seed"}, want: 0},
		{name: "validator token", cfg: Config{ValidatorToken: "token"}, want: 0},
		{name: "validator explicit override", cfg: Config{ValidationSeed: "seed", PathSearchMax: &explicit}, want: 5},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, test.cfg.ResolvedPathSearchMax())
		})
	}
}

func TestPathSearchMaxRejectsNegative(t *testing.T) {
	path := writeConfig(t, t.TempDir(), "goxrpl.toml", "path_search_max = -1\n"+minimalTestConfig())
	_, err := LoadConfig(Paths{Main: path, SkipValidators: true})
	require.ErrorContains(t, err, "path_search_max must be non-negative")
}
