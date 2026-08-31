package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadConfigCheckpointShutdownGrace(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "default", want: 2 * time.Minute},
		{name: "extended", value: `"30m"`, want: 30 * time.Minute},
		{name: "subsecond", value: `"500ms"`, want: 500 * time.Millisecond},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contents := minimalTestConfig()
			if test.value != "" {
				contents = withCheckpointShutdownGrace(contents, test.value)
			}
			path := writeConfig(t, t.TempDir(), "goxrpl.toml", contents)
			cfg, err := LoadConfig(Paths{Main: path})
			require.NoError(t, err)
			require.Equal(t, test.want, cfg.ResolvedCheckpointShutdownGrace())
		})
	}
}

func TestLoadConfigRejectsInvalidCheckpointShutdownGrace(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "malformed", value: `"eventually"`, wantErr: "must be a valid duration string"},
		{name: "zero", value: `"0s"`, wantErr: "must be positive"},
		{name: "negative", value: `"-1s"`, wantErr: "must be positive"},
		{name: "numeric", value: `30`, wantErr: "must be a duration string"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeConfig(t, t.TempDir(), "goxrpl.toml",
				withCheckpointShutdownGrace(minimalTestConfig(), test.value))
			_, err := LoadConfig(Paths{Main: path})
			require.ErrorContains(t, err, "server.checkpoint_shutdown_grace")
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestValidateConfigRejectsNonPositiveCheckpointShutdownGrace(t *testing.T) {
	for _, grace := range []time.Duration{0, -time.Second} {
		t.Run(grace.String(), func(t *testing.T) {
			cfg := validCompleteConfig()
			cfg.Server.CheckpointShutdownGrace = &grace
			err := ValidateConfig(cfg)
			require.ErrorContains(t, err, "server checkpoint_shutdown_grace must be positive")
		})
	}
}

func TestResolvedCheckpointShutdownGraceIsNilSafe(t *testing.T) {
	var cfg *Config
	require.Equal(t, 2*time.Minute, cfg.ResolvedCheckpointShutdownGrace())
}

func withCheckpointShutdownGrace(contents, value string) string {
	return strings.Replace(contents,
		`ports = ["port_test"]`,
		`ports = ["port_test"]`+"\ncheckpoint_shutdown_grace = "+value,
		1,
	)
}
