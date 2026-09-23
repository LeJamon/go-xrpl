package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBackfillConfig(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		want          bool
	}{
		{"default", "", true},
		{"enabled", "backfill = true\n", true},
		{"disabled", "backfill = false\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := writeAndLoad(t, tc.setting+"network_id = \"testnet\"\n"+baseTOMLWithoutUnionFields())
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.ResolvedBackfill())
			require.EqualValues(t, 256, cfg.GetLedgerHistoryUint32())
		})
	}
	for _, value := range []string{"1", "\"false\"", "[]"} {
		_, err := writeAndLoad(t, "backfill = "+value+"\nnetwork_id = \"testnet\"\n"+baseTOMLWithoutUnionFields())
		require.ErrorContains(t, err, "backfill must be a boolean")
	}
}

func TestBackfillHistoryCount(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  uint32
	}{
		{"0", 0}, {"1", 1}, {"256", 256}, {"\"none\"", 0}, {"\"full\"", ^uint32(0)},
	} {
		cfg, err := writeAndLoad(t, "ledger_history = "+tc.value+"\nnetwork_id = \"testnet\"\n"+baseTOMLWithoutUnionFields())
		require.NoError(t, err)
		require.Equal(t, tc.want, cfg.GetLedgerHistoryUint32())
	}
}
