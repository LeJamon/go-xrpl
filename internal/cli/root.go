package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	"github.com/LeJamon/go-xrpl/internal/replaytool"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/version"
	"github.com/spf13/cobra"
)

type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

type rootOptions struct {
	configFile string
	debug      bool
	verbose    bool
}

type commandDependencies struct {
	loadConfig func(config.Paths) (*config.Config, error)
	httpClient *http.Client
	runNode    nodeRunFunc
	reload     <-chan os.Signal
}

type application struct {
	options rootOptions
	deps    commandDependencies
}

func defaultCommandDependencies() commandDependencies {
	return commandDependencies{
		loadConfig: config.LoadConfig,
		httpClient: newRPCHTTPClient(),
		runNode:    runNode,
	}
}

func NewRootCommand(streams IOStreams) *cobra.Command {
	return newRootCommand(streams, defaultCommandDependencies())
}

func newRootCommand(streams IOStreams, deps commandDependencies) *cobra.Command {
	all.RegisterAll()
	streams = resolvedStreams(streams)
	if deps.loadConfig == nil {
		deps.loadConfig = config.LoadConfig
	}
	if deps.httpClient == nil {
		deps.httpClient = newRPCHTTPClient()
	}
	if deps.runNode == nil {
		deps.runNode = runNode
	}

	app := &application{deps: deps}
	defaultServerOptions := &serverOptions{}
	root := &cobra.Command{
		Use:   "xrpld",
		Short: "go-xrpl - XRPL Node Implementation in Go",
		Long: `go-xrpl is an idiomatic Go implementation of an XRPL (XRP Ledger) client
with concurrent processing capabilities. This is NOT a direct translation of the
C++ rippled implementation but rather a native Go implementation that follows
Go conventions and patterns while maintaining protocol compatibility.`,
		Version:       version.Version,
		Args:          cobra.NoArgs,
		RunE:          app.serverRunner(defaultServerOptions),
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.ErrOut)

	root.PersistentFlags().StringVar(&app.options.configFile, "conf", "", "configuration file path (required by server, rpc)")
	root.PersistentFlags().BoolVar(&app.options.debug, "debug", false, "enable normally suppressed debug logging")
	root.PersistentFlags().BoolVarP(&app.options.verbose, "verbose", "v", false, "verbose logging")
	bindServerFlags(root.Flags(), defaultServerOptions)

	root.AddCommand(
		app.newServerCommand(defaultServerOptions),
		newCompareCommand(),
		newGenerateConfigCommand(),
		app.newMigrateRotationStateCommand(),
		app.newRPCCommand(),
		newVersionCommand(),
	)
	for _, command := range replaytool.NewCommands() {
		root.AddCommand(command)
	}
	return root
}

func resolvedStreams(streams IOStreams) IOStreams {
	if streams.In == nil {
		streams.In = os.Stdin
	}
	if streams.Out == nil {
		streams.Out = os.Stdout
	}
	if streams.ErrOut == nil {
		streams.ErrOut = os.Stderr
	}
	return streams
}

func (a *application) requireConfig(skipValidators bool) (*config.Config, error) {
	if a.options.configFile == "" {
		return nil, fmt.Errorf("missing --conf flag: this command requires a configuration file")
	}
	cfg, err := a.deps.loadConfig(config.Paths{
		Main:           a.options.configFile,
		SkipValidators: skipValidators,
	})
	if err != nil {
		return nil, fmt.Errorf("configuration error: %w", err)
	}
	return cfg, nil
}

func Run(ctx context.Context, args []string, streams IOStreams) int {
	return runWithSignalSource(ctx, args, streams, systemProcessSignals)
}

func runWithSignalSource(
	ctx context.Context,
	args []string,
	streams IOStreams,
	source processSignalSource,
) int {
	signals := source(ctx)
	defer signals.stop()
	deps := defaultCommandDependencies()
	deps.reload = signals.reload
	root := newRootCommand(streams, deps)
	return executeRoot(signals.ctx, root, args)
}

func executeRoot(ctx context.Context, root *cobra.Command, args []string) int {
	root.SetArgs(args)
	_, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	if !errors.Is(err, cmdexit.ErrReported) {
		fmt.Fprintf(root.ErrOrStderr(), "Error: %v\n", err)
	}
	return 1
}
