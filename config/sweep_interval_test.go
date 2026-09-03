package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolvedSweepInterval(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		expected time.Duration
	}{
		{name: "nil config", expected: 60 * time.Second},
		{name: "default", config: &Config{}, expected: 60 * time.Second},
		{name: "tiny", config: &Config{NodeSize: "tiny"}, expected: 10 * time.Second},
		{name: "small", config: &Config{NodeSize: "small"}, expected: 30 * time.Second},
		{name: "medium", config: &Config{NodeSize: "medium"}, expected: 60 * time.Second},
		{name: "large", config: &Config{NodeSize: "large"}, expected: 90 * time.Second},
		{name: "huge", config: &Config{NodeSize: "huge"}, expected: 120 * time.Second},
		{name: "explicit override", config: &Config{NodeSize: "huge", SweepInterval: sweepIntervalPtr(45)}, expected: 45 * time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, test.config.ResolvedSweepInterval())
		})
	}
}

func TestValidateSweepInterval(t *testing.T) {
	require.NoError(t, ValidateSweepInterval(nil))
	require.NoError(t, ValidateSweepInterval(sweepIntervalPtr(10)))
	require.NoError(t, ValidateSweepInterval(sweepIntervalPtr(600)))
	require.EqualError(t, ValidateSweepInterval(sweepIntervalPtr(9)), "sweep_interval must be between 10 and 600 seconds, got 9")
	require.EqualError(t, ValidateSweepInterval(sweepIntervalPtr(601)), "sweep_interval must be between 10 and 600 seconds, got 601")
}

func TestLoadSweepInterval(t *testing.T) {
	config, err := writeAndLoad(t, "sweep_interval = 45\n"+minimalTestConfig())
	require.NoError(t, err)
	require.NotNil(t, config.SweepInterval)
	require.Equal(t, 45, *config.SweepInterval)
	require.Equal(t, 45*time.Second, config.ResolvedSweepInterval())
}

func TestLoadSweepIntervalRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "below minimum", value: "9", wantErr: "between 10 and 600"},
		{name: "above maximum", value: "601", wantErr: "between 10 and 600"},
		{name: "string", value: `"60"`, wantErr: "integer count"},
		{name: "float", value: "60.5", wantErr: "integer count"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := writeAndLoad(t, "sweep_interval = "+tt.value+"\n"+minimalTestConfig())
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func sweepIntervalPtr(value int) *int {
	return &value
}
