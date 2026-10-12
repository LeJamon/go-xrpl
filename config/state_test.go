package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStateConfigResolvedDefaults(t *testing.T) {
	got := (StateConfig{}).Resolved()
	require.Equal(t, int64(64), got.MemoryMB)
	require.Equal(t, int64(2048), got.ScratchMB)
	require.Equal(t, int64(16), got.CacheMB)
	require.Equal(t, int64(8), got.MemTableMB)
}

func TestStateConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  StateConfig
		wantErr string
	}{
		{name: "disabled allows zero allowances", config: StateConfig{}},
		{
			name:    "enabled memory minimum",
			config:  StateConfig{Enabled: true, MemoryMB: 7},
			wantErr: "state.memory_mb must be at least 8 MiB",
		},
		{
			name:    "negative scratch",
			config:  StateConfig{ScratchMB: -1},
			wantErr: "state.scratch_mb must be between 0 and 1073741824 MiB",
		},
		{
			name:    "oversized cache",
			config:  StateConfig{CacheMB: 1<<30 + 1},
			wantErr: "state.cache_mb must be between 0 and 1073741824 MiB",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, test.wantErr)
		})
	}
}
