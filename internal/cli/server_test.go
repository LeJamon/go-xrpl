package cli

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultServerCommandHasStartupFlags(t *testing.T) {
	root := NewRootCommand(IOStreams{})
	for _, name := range []string{"standalone", "start", "load", "net", "replay", "ledger", "ledgerfile"} {
		require.NotNil(t, root.Flags().Lookup(name), "root command is missing --%s", name)
	}
}

func TestStartupConfig(t *testing.T) {
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
			options := &serverOptions{}
			bindStartupFlags(flags, options)
			require.NoError(t, flags.Parse(tt.args))

			got, err := startupConfig(options, flags.Changed("ledger"), flags.Changed("ledgerfile"))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStartupConfigRejectsInvalidCombinations(t *testing.T) {
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
			options := &serverOptions{}
			bindStartupFlags(flags, options)
			require.NoError(t, flags.Parse(tt.args))

			_, err := startupConfig(options, flags.Changed("ledger"), flags.Changed("ledgerfile"))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestExplicitServerUsesFlagsBeforeOrAfterSubcommand(t *testing.T) {
	t.Setenv("GOXRPL_PPROF", "")
	t.Setenv("GOXRPL_METRICS", "")
	t.Setenv("GOXRPL_PPROF_ALLOW_UNSAFE", "")

	configPath := writeServerTestConfig(t)
	for _, test := range []struct {
		name string
		args []string
	}{
		{
			name: "before subcommand",
			args: []string{"--conf", configPath, "--standalone", "--load", "server"},
		},
		{
			name: "after subcommand",
			args: []string{"--conf", configPath, "server", "--standalone", "--load"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var gotStandalone bool
			var gotStartup service.StartupConfig
			deps := commandDependencies{
				loadConfig: config.LoadConfig,
				runNode: func(
					_ context.Context,
					_ *config.Config,
					_ string,
					standalone bool,
					startup service.StartupConfig,
					_, _ xrpllog.Logger,
				) error {
					gotStandalone = standalone
					gotStartup = startup
					return nil
				},
			}

			root := newRootCommand(
				IOStreams{Out: bytes.NewBuffer(nil), ErrOut: bytes.NewBuffer(nil)},
				deps,
			)
			root.SetArgs(test.args)
			_, err := root.ExecuteC()
			require.NoError(t, err)
			assert.True(t, gotStandalone)
			assert.Equal(t, service.StartupConfig{Mode: service.StartupLoad}, gotStartup)
		})
	}
}

func TestServerBindFailurePreventsNodeStart(t *testing.T) {
	configPath := writeServerTestConfig(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	t.Setenv("GOXRPL_PPROF", "")
	t.Setenv("GOXRPL_METRICS", listener.Addr().String())
	t.Setenv("GOXRPL_PPROF_ALLOW_UNSAFE", "")

	var nodeStarted atomic.Bool
	deps := defaultCommandDependencies()
	deps.runNode = func(
		context.Context,
		*config.Config,
		string,
		bool,
		service.StartupConfig,
		xrpllog.Logger,
		xrpllog.Logger,
	) error {
		nodeStarted.Store(true)
		return nil
	}
	root := newRootCommand(IOStreams{Out: bytes.NewBuffer(nil), ErrOut: bytes.NewBuffer(nil)}, deps)
	root.SetArgs([]string{"--conf", configPath, "server", "--standalone"})
	_, err = root.ExecuteC()
	require.ErrorContains(t, err, "bind metrics server")
	assert.False(t, nodeStarted.Load())
}

func TestServerCancellationShutsDownAuxiliaryListeners(t *testing.T) {
	configPath := writeServerTestConfig(t)
	address := availableLoopbackAddress(t)
	t.Setenv("GOXRPL_PPROF", "")
	t.Setenv("GOXRPL_METRICS", address)
	t.Setenv("GOXRPL_PPROF_ALLOW_UNSAFE", "")

	started := make(chan struct{})
	deps := defaultCommandDependencies()
	deps.runNode = func(
		ctx context.Context,
		_ *config.Config,
		_ string,
		_ bool,
		_ service.StartupConfig,
		_,
		_ xrpllog.Logger,
	) error {
		close(started)
		<-ctx.Done()
		return context.Cause(ctx)
	}
	root := newRootCommand(IOStreams{Out: bytes.NewBuffer(nil), ErrOut: bytes.NewBuffer(nil)}, deps)
	root.SetArgs([]string{"--conf", configPath, "server", "--standalone"})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := root.ExecuteContextC(ctx)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("node runner did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("server error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not return after cancellation")
	}

	listener, err := net.Listen("tcp", address)
	require.NoError(t, err, "auxiliary listener was not released")
	require.NoError(t, listener.Close())
}

func writeServerTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "xrpld.toml")
	require.NoError(t, os.WriteFile(path, []byte(generateConfigContent("devnet")), 0o600))
	return path
}

func availableLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}
