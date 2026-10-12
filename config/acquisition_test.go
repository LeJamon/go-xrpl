package config

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquisitionConfigResolvedMemoryBytes(t *testing.T) {
	const bytesPerMiBTest = uint64(1 << 20)

	tests := []struct {
		name    string
		memory  int64
		want    uint64
		wantErr bool
	}{
		{name: "zero uses default", want: DefaultAcquisitionMemoryMB * bytesPerMiBTest},
		{name: "explicit value", memory: MinAcquisitionMemoryMB, want: MinAcquisitionMemoryMB * bytesPerMiBTest},
		{name: "negative", memory: -1, wantErr: true},
		{name: "overflow", memory: int64(maxAcquisitionMemoryMB + 1), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := AcquisitionConfig{MemoryMB: test.memory}
			err := cfg.Validate()
			if test.wantErr {
				require.Error(t, err)
				assert.Zero(t, cfg.ResolvedMemoryBytes())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, cfg.ResolvedMemoryBytes())
		})
	}
}

func TestAcquisitionConfigValidateMinimum(t *testing.T) {
	for _, memoryMB := range []int64{1, MinAcquisitionMemoryMB - 1} {
		err := (AcquisitionConfig{MemoryMB: memoryMB}).Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "memory_mb")
	}
	require.NoError(t, (AcquisitionConfig{MemoryMB: MinAcquisitionMemoryMB}).Validate())
}

func TestLoadConfigAcquisitionMemory(t *testing.T) {
	maxMemoryMB := int64(math.MaxUint64 / uint64(bytesPerMiB))
	tests := []struct {
		name       string
		setting    string
		wantMemory int64
		wantErr    string
	}{
		{name: "omitted", wantMemory: 0},
		{name: "explicit zero", setting: "[acquisition]\nmemory_mb = 0", wantMemory: 0},
		{name: "explicit", setting: fmt.Sprintf("[acquisition]\nmemory_mb = %d", MinAcquisitionMemoryMB), wantMemory: MinAcquisitionMemoryMB},
		{name: "negative", setting: "[acquisition]\nmemory_mb = -1", wantErr: "acquisition.memory_mb must be non-negative"},
		{name: "too small", setting: fmt.Sprintf("[acquisition]\nmemory_mb = %d", MinAcquisitionMemoryMB-1), wantErr: "acquisition: memory_mb must be 0 or at least"},
		{name: "overflow", setting: fmt.Sprintf("[acquisition]\nmemory_mb = %d", maxMemoryMB+1), wantErr: "acquisition.memory_mb is too large"},
		{name: "non-integer", setting: "[acquisition]\nmemory_mb = \"256\"", wantErr: "acquisition.memory_mb must be an integer count"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contents := minimalTestConfig()
			if test.setting != "" {
				contents += "\n" + test.setting + "\n"
			}
			path := writeConfig(t, t.TempDir(), "goxrpl.toml", contents)
			cfg, err := LoadConfig(Paths{Main: path})
			if test.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, cfg)
			assert.Equal(t, test.wantMemory, cfg.Acquisition.MemoryMB)
			if test.wantMemory == 0 {
				assert.Equal(t, uint64(DefaultAcquisitionMemoryMB)*(1<<20), cfg.Acquisition.ResolvedMemoryBytes())
			} else {
				assert.Equal(t, uint64(test.wantMemory)*(1<<20), cfg.Acquisition.ResolvedMemoryBytes())
			}
		})
	}
}
