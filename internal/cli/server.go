package cli

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/LeJamon/go-xrpl/internal/node"
	"github.com/LeJamon/go-xrpl/internal/observability"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/version"
	"github.com/spf13/cobra"
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
}

func runServer(cmd *cobra.Command, args []string) error {
	if _, err := requireConfig(); err != nil {
		// Fold the guidance into the error so Execute() prints it once. A bare
		// pre-print here would duplicate the message Execute() emits.
		return fmt.Errorf("%w\n  Use 'xrpld generate-config' to create an initial configuration file."+
			"\n  Example: xrpld server --conf /path/to/xrpld.toml", err)
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

	return node.Run(globalConfig, configFile, standalone, rootLogger, serverLog)
}
