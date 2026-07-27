package cli

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartupConfigFromFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want service.StartupConfig
	}{
		{
			name: "normal",
			want: service.StartupConfig{Mode: service.StartupNormal},
		},
		{
			name: "fresh",
			args: []string{"--start"},
			want: service.StartupConfig{Mode: service.StartupFresh},
		},
		{
			name: "latest local",
			args: []string{"--load"},
			want: service.StartupConfig{Mode: service.StartupLoad},
		},
		{
			name: "network",
			args: []string{"--net"},
			want: service.StartupConfig{Mode: service.StartupNetwork},
		},
		{
			name: "selected ledger",
			args: []string{"--ledger", "43"},
			want: service.StartupConfig{Mode: service.StartupLoad, Ledger: "43"},
		},
		{
			name: "explicit empty ledger means latest",
			args: []string{"--ledger="},
			want: service.StartupConfig{Mode: service.StartupLoad},
		},
		{
			name: "ledger file",
			args: []string{"--ledgerfile", "ledger.json"},
			want: service.StartupConfig{Mode: service.StartupLoadFile, Ledger: "ledger.json"},
		},
		{
			name: "replay selected ledger",
			args: []string{"--replay", "--ledger", "32570"},
			want: service.StartupConfig{Mode: service.StartupReplay, Ledger: "32570"},
		},
		{
			name: "replay latest ledger",
			args: []string{"--replay", "--ledger="},
			want: service.StartupConfig{Mode: service.StartupReplay},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := pflag.NewFlagSet(tt.name, pflag.ContinueOnError)
			bindStartupFlags(flags)
			require.NoError(t, flags.Parse(tt.args))

			got, err := startupConfigFromFlags(flags)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStartupConfigFromFlagsRejectsInvalidCombinations(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "replay without ledger",
			args:    []string{"--replay"},
			wantErr: "--replay requires --ledger",
		},
		{
			name:    "empty ledger file",
			args:    []string{"--ledgerfile="},
			wantErr: "--ledgerfile requires a non-empty path",
		},
		{
			name:    "fresh and load",
			args:    []string{"--start", "--load"},
			wantErr: "startup modes are mutually exclusive",
		},
		{
			name:    "network and selected ledger",
			args:    []string{"--net", "--ledger", "43"},
			wantErr: "startup modes are mutually exclusive",
		},
		{
			name:    "load and replay",
			args:    []string{"--load", "--replay", "--ledger", "32570"},
			wantErr: "startup modes are mutually exclusive",
		},
		{
			name:    "ledger and ledger file",
			args:    []string{"--ledger", "43", "--ledgerfile", "ledger.json"},
			wantErr: "startup modes are mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := pflag.NewFlagSet(tt.name, pflag.ContinueOnError)
			bindStartupFlags(flags)
			require.NoError(t, flags.Parse(tt.args))

			_, err := startupConfigFromFlags(flags)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
