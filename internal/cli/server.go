package cli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/node"
	"github.com/LeJamon/go-xrpl/internal/observability"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/version"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	standalone bool
)

// serverCmd represents the server command (default action)
var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the XRPL daemon server",
	Long: `Start the go-xrpl server which provides:
- HTTP JSON-RPC API endpoints
- WebSocket server for real-time subscriptions
- Health check endpoint
- All XRPL protocol methods

Requires --conf flag to specify the configuration file.
Use 'xrpld generate-config' to create an initial configuration file.`,
	RunE: runServer,
}

func init() {
	rootCmd.AddCommand(serverCmd)

	// Set server as the default command
	rootCmd.RunE = runServer

	// Server-specific flags — operational concerns only
	serverCmd.Flags().BoolVarP(&standalone, "standalone", "a", false, "run in standalone mode (no peers)")
	bindStartupFlags(serverCmd.Flags())
}

func runServer(cmd *cobra.Command, args []string) error {
	if _, err := requireConfig(); err != nil {
		// Fold the guidance into the error so Execute() prints it once. A bare
		// pre-print here would duplicate the message Execute() emits.
		return fmt.Errorf("%w\n  Use 'xrpld generate-config' to create an initial configuration file."+
			"\n  Example: xrpld server --conf /path/to/xrpld.toml", err)
	}

	startup, err := startupConfigFromFlags(cmd.Flags())
	if err != nil {
		return err
	}

	// Initialize structured logger from config + CLI flag overrides.
	logCfg, err := globalConfig.Logging.ToLogConfig(globalConfig.DebugLogfile)
	if err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}
	if debug {
		logCfg.Level = xrpllog.LevelDebug
	}
	if verbose {
		logCfg.Level = xrpllog.LevelTrace
	}
	rootLogger, logHandler := xrpllog.Init(logCfg)
	// Route subsystems that log through slog.Default() (consensus adaptor,
	// inbound-ledger, validator-list) through the same configured handler so
	// they honour the operator's level, format, output file, and rotation.
	slog.SetDefault(slog.New(logHandler))
	serverLog := rootLogger.Named(xrpllog.PartitionServer)

	serverLog.Info("Starting go-xrpl", "version", version.Version)

	// Set GOXRPL_PPROF=:6060 (or any addr:port) to enable pprof. Off by default.
	if addr := os.Getenv("GOXRPL_PPROF"); addr != "" {
		go func() {
			if err := observability.StartPProf(addr); err != nil {
				serverLog.Warn("pprof server failed", "addr", addr, "err", err)
			}
		}()
		serverLog.Info("pprof enabled", "addr", addr)
	}

	// Set GOXRPL_METRICS=:9100 (or any addr:port) to expose Prometheus
	// metrics at /metrics. Off by default.
	if addr := os.Getenv("GOXRPL_METRICS"); addr != "" {
		go func() {
			if err := startMetricsServer(addr); err != nil {
				serverLog.Warn("metrics server failed", "addr", addr, "err", err)
			}
		}()
		serverLog.Info("prometheus metrics enabled", "addr", addr)
	}

	return node.Run(globalConfig, configFile, standalone, startup, rootLogger, serverLog)
}

func bindStartupFlags(flags *pflag.FlagSet) {
	flags.Bool("start", false, "start from a fresh ledger")
	flags.Bool("load", false, "load the latest ledger from local storage")
	flags.Bool("net", false, "acquire the initial ledger from the network")
	flags.Bool("replay", false, "replay the ledger close selected by --ledger")
	flags.String("ledger", "", "load the ledger identified by a hash, sequence, or shortcut")
	flags.String("ledgerfile", "", "load a ledger from a JSON file")
}

func startupConfigFromFlags(flags *pflag.FlagSet) (service.StartupConfig, error) {
	start, err := flags.GetBool("start")
	if err != nil {
		return service.StartupConfig{}, err
	}
	load, err := flags.GetBool("load")
	if err != nil {
		return service.StartupConfig{}, err
	}
	network, err := flags.GetBool("net")
	if err != nil {
		return service.StartupConfig{}, err
	}
	replay, err := flags.GetBool("replay")
	if err != nil {
		return service.StartupConfig{}, err
	}
	ledger, err := flags.GetString("ledger")
	if err != nil {
		return service.StartupConfig{}, err
	}
	ledgerFile, err := flags.GetString("ledgerfile")
	if err != nil {
		return service.StartupConfig{}, err
	}

	ledgerSet := flags.Changed("ledger")
	ledgerFileSet := flags.Changed("ledgerfile")
	if replay && !ledgerSet {
		return service.StartupConfig{}, fmt.Errorf("--replay requires --ledger")
	}
	if ledgerFileSet && ledgerFile == "" {
		return service.StartupConfig{}, fmt.Errorf("--ledgerfile requires a non-empty path")
	}

	selected := 0
	for _, active := range []bool{
		start,
		load,
		network,
		replay,
		ledgerFileSet,
		ledgerSet && !replay,
	} {
		if active {
			selected++
		}
	}
	if selected > 1 {
		return service.StartupConfig{}, fmt.Errorf(
			"startup modes are mutually exclusive: choose only one of --start, --load, --net, --replay, --ledger, or --ledgerfile",
		)
	}

	switch {
	case start:
		return service.StartupConfig{Mode: service.StartupFresh}, nil
	case load:
		return service.StartupConfig{Mode: service.StartupLoad}, nil
	case network:
		return service.StartupConfig{Mode: service.StartupNetwork}, nil
	case replay:
		return service.StartupConfig{Mode: service.StartupReplay, Ledger: ledger}, nil
	case ledgerFileSet:
		return service.StartupConfig{Mode: service.StartupLoadFile, Ledger: ledgerFile}, nil
	case ledgerSet:
		return service.StartupConfig{Mode: service.StartupLoad, Ledger: ledger}, nil
	default:
		return service.StartupConfig{Mode: service.StartupNormal}, nil
	}
}
