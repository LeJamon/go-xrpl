package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireConfigStandaloneSkipsValidators(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "xrpld.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(generateConfigContent("main")), 0o600))

	app := application{
		options: rootOptions{configFile: configPath},
		deps:    commandDependencies{loadConfig: config.LoadConfig},
	}
	cfg, err := app.requireConfig(true)
	require.NoError(t, err)
	assert.Empty(t, cfg.Validators.Validators)
	assert.Empty(t, cfg.Validators.ValidatorListKeys)
}

func TestNewRootCommandHasIsolatedState(t *testing.T) {
	var firstOut, secondOut bytes.Buffer
	first := NewRootCommand(IOStreams{Out: &firstOut, ErrOut: &firstOut})
	second := NewRootCommand(IOStreams{Out: &secondOut, ErrOut: &secondOut})

	first.SetArgs([]string{"--debug", "version"})
	_, err := first.ExecuteC()
	require.NoError(t, err)

	debugValue, err := second.PersistentFlags().GetBool("debug")
	require.NoError(t, err)
	assert.False(t, debugValue)
	compare, _, err := second.Find([]string{"compare"})
	require.NoError(t, err)
	showAll, err := compare.Flags().GetBool("all")
	require.NoError(t, err)
	assert.False(t, showAll)
	generate, _, err := second.Find([]string{"generate-config"})
	require.NoError(t, err)
	outputPath, err := generate.Flags().GetString("output")
	require.NoError(t, err)
	assert.Equal(t, "xrpld.toml", outputPath)
	rpc, _, err := second.Find([]string{"rpc"})
	require.NoError(t, err)
	allowInsecure, err := rpc.PersistentFlags().GetBool(insecureRPCFlag)
	require.NoError(t, err)
	assert.False(t, allowInsecure)

	second.SetArgs([]string{"version"})
	_, err = second.ExecuteC()
	require.NoError(t, err)
	assert.Contains(t, firstOut.String(), "go-xrpl version")
	assert.Contains(t, secondOut.String(), "go-xrpl version")
}

func TestRootCommandsExecuteConcurrently(t *testing.T) {
	const commandCount = 8

	var waitGroup sync.WaitGroup
	errors := make(chan error, commandCount)
	for i := range commandCount {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()

			root := NewRootCommand(IOStreams{Out: io.Discard, ErrOut: io.Discard})
			if i%2 == 0 {
				root.SetArgs([]string{"--debug", "version"})
			} else {
				root.SetArgs([]string{"version"})
			}
			_, err := root.ExecuteC()
			errors <- err
		}()
	}
	waitGroup.Wait()
	close(errors)

	for err := range errors {
		require.NoError(t, err)
	}
}

func TestRootCommandNoArgumentContracts(t *testing.T) {
	for _, args := range [][]string{
		{"version", "extra"},
		{"generate-config", "extra"},
		{"server", "extra"},
		{"extra"},
	} {
		root := NewRootCommand(IOStreams{Out: bytes.NewBuffer(nil), ErrOut: bytes.NewBuffer(nil)})
		root.SetArgs(args)
		if _, err := root.ExecuteC(); err == nil {
			t.Errorf("ExecuteC(%q) succeeded", args)
		}
	}
}

func TestRunExitBehavior(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"version"}, IOStreams{Out: &stdout, ErrOut: &stderr})
		assert.Equal(t, 0, code)
		assert.Contains(t, stdout.String(), "go-xrpl version")
		assert.Empty(t, stderr.String())
	})

	t.Run("ordinary error is printed once", func(t *testing.T) {
		var stderr bytes.Buffer
		code := Run(context.Background(), []string{"does-not-exist"}, IOStreams{
			Out:    bytes.NewBuffer(nil),
			ErrOut: &stderr,
		})
		assert.Equal(t, 1, code)
		assert.Equal(t, 1, strings.Count(stderr.String(), "Error:"))
		assert.Contains(t, stderr.String(), `unknown command "does-not-exist"`)
	})

	t.Run("reported error is not printed again", func(t *testing.T) {
		var stderr bytes.Buffer
		root := &cobra.Command{Use: "test", SilenceErrors: true, SilenceUsage: true}
		root.SetErr(&stderr)
		root.AddCommand(&cobra.Command{
			Use: "reported",
			RunE: func(*cobra.Command, []string) error {
				return cmdexit.ErrReported
			},
		})
		code := executeRoot(context.Background(), root, []string{"reported"})
		assert.Equal(t, 1, code)
		assert.Empty(t, stderr.String())
	})
}

func TestConfigLoadsOnlyForCommandsThatRequireIt(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.toml")

	versionRoot := NewRootCommand(IOStreams{Out: bytes.NewBuffer(nil), ErrOut: bytes.NewBuffer(nil)})
	versionRoot.SetArgs([]string{"--conf", missing, "version"})
	if _, err := versionRoot.ExecuteC(); err != nil {
		t.Fatalf("version with missing --conf: %v", err)
	}

	rpcRoot := NewRootCommand(IOStreams{Out: bytes.NewBuffer(nil), ErrOut: bytes.NewBuffer(nil)})
	rpcRoot.SetArgs([]string{"--conf", missing, "rpc", "ping"})
	if _, err := rpcRoot.ExecuteC(); err == nil || !strings.Contains(err.Error(), "configuration error") {
		t.Fatalf("rpc with missing --conf error = %v, want configuration error", err)
	}
}
