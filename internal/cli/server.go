package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/node"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type serverOptions struct {
	standalone bool
	start      bool
	load       bool
	network    bool
	replay     bool
	ledger     string
	ledgerFile string
}

type nodeRunFunc func(
	context.Context,
	*config.Config,
	string,
	bool,
	service.StartupConfig,
	xrpllog.Logger,
	xrpllog.Logger,
	func(),
	func(),
	<-chan os.Signal,
) error

func runNode(
	ctx context.Context,
	cfg *config.Config,
	configPath string,
	standalone bool,
	startup service.StartupConfig,
	rootLogger,
	serverLog xrpllog.Logger,
	ready func(),
	stopping func(),
	reload <-chan os.Signal,
) error {
	return node.RunWithOptions(ctx, cfg, configPath, standalone, startup, rootLogger, serverLog, node.RunOptions{
		Ready:    ready,
		Stopping: stopping,
		Reload:   reload,
	})
}

func (a *application) newServerCommand(options *serverOptions) *cobra.Command {
	command := &cobra.Command{
		Use:   "server",
		Short: "Start the XRPL daemon server",
		Long: `Start the go-xrpl server which provides:
- HTTP JSON-RPC API endpoints
- WebSocket server for real-time subscriptions
- Health check endpoint
- All XRPL protocol methods

Requires --conf flag to specify the configuration file.
Use 'goxrpl generate-config' to create an initial configuration file.

GOXRPL_PPROF and GOXRPL_METRICS enable loopback diagnostic listeners. A
configured listener that cannot bind aborts startup. Exposing pprof on a
non-loopback address additionally requires GOXRPL_PPROF_ALLOW_UNSAFE=true.`,
		Args: cobra.NoArgs,
		RunE: a.serverRunner(options),
	}
	bindServerFlags(command.Flags(), options)
	return command
}

func bindServerFlags(flags *pflag.FlagSet, options *serverOptions) {
	flags.BoolVarP(&options.standalone, "standalone", "a", false, "run in standalone mode (no peers)")
	bindStartupFlags(flags, options)
}

func (a *application) serverRunner(options *serverOptions) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		return a.runServer(cmd, options)
	}
}

func (a *application) runServer(cmd *cobra.Command, options *serverOptions) (resultErr error) {
	runCtx, cancel := context.WithCancelCause(cmd.Context())
	defer cancel(nil)
	defer func() {
		cause := context.Cause(runCtx)
		if isProcessSignal(cause) && wrapsOnly(resultErr, cause) {
			resultErr = nil
		}
	}()
	if cause := context.Cause(runCtx); cause != nil {
		return cause
	}

	cfg, err := a.requireConfig(options.standalone)
	if err != nil {
		// Fold the guidance into the error so Execute() prints it once. A bare
		// pre-print here would duplicate the message Execute() emits.
		return fmt.Errorf("%w\n  Use 'goxrpl generate-config' to create an initial configuration file."+
			"\n  Example: goxrpl server --conf /path/to/goxrpl.toml", err)
	}
	if cause := context.Cause(runCtx); cause != nil {
		return cause
	}

	startup, err := startupConfig(
		options,
		commandFlagChanged(cmd, "ledger"),
		commandFlagChanged(cmd, "ledgerfile"),
		cfg.NodeDB.FastLoad,
	)
	if err != nil {
		return err
	}

	// Initialize structured logger from config + CLI flag overrides.
	logCfg, err := cfg.Logging.ToLogConfig(cfg.DebugLogfile)
	if err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}
	if a.options.debug {
		logCfg.Level = xrpllog.LevelDebug
	}
	if a.options.verbose {
		logCfg.Level = xrpllog.LevelTrace
	}
	rootLogger, logHandler := xrpllog.Init(logCfg)
	// Route subsystems that log through slog.Default() (consensus adaptor,
	// inbound-ledger, validator-list) through the same configured handler so
	// they honour the operator's level, format, output file, and rotation.
	slog.SetDefault(slog.New(logHandler))
	serverLog := rootLogger.Named(xrpllog.PartitionServer)

	serverLog.Info("Starting go-xrpl", "version", version.Version)

	auxiliary, err := bindAuxiliaryServers(os.Getenv, net.Listen)
	if err != nil {
		return err
	}
	ready := func() {
		auxiliary.Start(runCtx, cancel)
		for name, address := range auxiliary.Addresses() {
			serverLog.Info(name+" enabled", "addr", address)
		}
	}
	nodeErr := a.deps.runNode(
		runCtx,
		cfg,
		a.options.configFile,
		options.standalone,
		startup,
		rootLogger,
		serverLog,
		ready,
		func() { cancel(nil) },
		a.deps.reload,
	)
	cancel(nil)
	return errors.Join(nodeErr, auxiliary.Shutdown())
}

func bindStartupFlags(flags *pflag.FlagSet, options *serverOptions) {
	flags.BoolVar(&options.start, "start", false, "start from a fresh ledger")
	flags.BoolVar(&options.load, "load", false, "load the latest ledger from local storage")
	flags.BoolVar(&options.network, "net", false, "acquire the initial ledger from the network")
	flags.BoolVar(&options.replay, "replay", false, "replay the ledger close selected by --ledger")
	flags.StringVar(&options.ledger, "ledger", "", "load the ledger identified by a hash, sequence, or shortcut")
	flags.StringVar(&options.ledgerFile, "ledgerfile", "", "load a ledger from a JSON file")
}

func commandFlagChanged(command *cobra.Command, name string) bool {
	for current := command; current != nil; current = current.Parent() {
		if current.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func startupConfig(options *serverOptions, ledgerSet, ledgerFileSet, fastLoad bool) (service.StartupConfig, error) {
	startup := service.StartupConfig{Mode: service.StartupNormal}
	if options.start {
		startup.Mode = service.StartupFresh
	}

	switch {
	case ledgerSet:
		startup.Ledger = options.ledger
		if options.replay {
			startup.Mode = service.StartupReplay
		} else {
			startup.Mode = service.StartupLoad
		}
	case ledgerFileSet:
		startup = service.StartupConfig{Mode: service.StartupLoadFile, Ledger: options.ledgerFile}
	case options.load:
		startup = service.StartupConfig{Mode: service.StartupLoad}
	case fastLoad:
		startup = service.StartupConfig{Mode: service.StartupNormal}
	}

	if options.network && !fastLoad {
		if startup.Mode == service.StartupLoad || startup.Mode == service.StartupReplay {
			return service.StartupConfig{}, fmt.Errorf("--net is incompatible with --load or --ledger")
		}
		startup = service.StartupConfig{Mode: service.StartupNetwork}
	}
	if startup.Mode == service.StartupLoadFile && startup.Ledger == "" && !fastLoad {
		return service.StartupConfig{}, fmt.Errorf("--ledgerfile requires a non-empty path")
	}
	return startup, nil
}
