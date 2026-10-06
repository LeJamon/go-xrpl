package config

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResourcesConfigResolve(t *testing.T) {
	tests := []struct {
		name      string
		config    ResourcesConfig
		available uint64
		want      uint64
	}{
		{name: "fixed takes precedence", config: ResourcesConfig{MemoryMB: 64, AvailableMemoryPercent: 90}, available: 1, want: 64 * 1 << 20},
		{name: "automatic percentage", config: ResourcesConfig{AvailableMemoryPercent: 25}, available: 101, want: 25},
		{name: "zero uses default", config: ResourcesConfig{}, available: 105, want: 21},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.config.Resolve(test.available)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestResourcesConfigValidate(t *testing.T) {
	for _, test := range []struct {
		name   string
		config ResourcesConfig
		want   string
	}{
		{name: "negative memory", config: ResourcesConfig{MemoryMB: -1}, want: "memory_mb must be non-negative"},
		{name: "memory overflow", config: ResourcesConfig{MemoryMB: math.MaxInt64}, want: "memory_mb is too large"},
		{name: "percentage too high", config: ResourcesConfig{AvailableMemoryPercent: 101}, want: "available_memory_percent must be between"},
		{name: "negative percentage", config: ResourcesConfig{AvailableMemoryPercent: -1}, want: "available_memory_percent must be between"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestLoadConfigResources(t *testing.T) {
	configPath := writeConfig(t, t.TempDir(), "goxrpl.toml", minimalTestConfig()+`

[resources]
memory_mb = 1024
available_memory_percent = 35
`)
	cfg, err := LoadConfig(Paths{Main: configPath})
	require.NoError(t, err)
	assert.Equal(t, int64(1024), cfg.Resources.MemoryMB)
	assert.Equal(t, 35, cfg.Resources.AvailableMemoryPercent)

	for _, value := range []string{"-1", "9223372036854775807"} {
		path := writeConfig(t, t.TempDir(), "goxrpl.toml", minimalTestConfig()+"\n[resources]\nmemory_mb = "+value+"\n")
		_, err := LoadConfig(Paths{Main: path})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resources.memory_mb")
	}
	for _, value := range []string{"101", "-1", `"20"`, "20.5"} {
		path := writeConfig(t, t.TempDir(), "goxrpl.toml", minimalTestConfig()+"\n[resources]\navailable_memory_percent = "+value+"\n")
		_, err := LoadConfig(Paths{Main: path})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resources.available_memory_percent")
	}
}
