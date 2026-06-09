package cli

import (
	"fmt"
	"os"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/replaytool"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/version"
	"github.com/spf13/cobra"
)

var (
	// Global flags
	configFile string
	debug      bool
	verbose    bool

	// globalConfig holds the loaded configuration, available to all subcommands.
	// It is nil until initConfig() runs (which happens before any command's Run function).
	globalConfig *config.Config

	// globalConfigErr captures any error from initConfig so commands that
	// don't need config (help, generate-config) can still run and commands
	// that do need it can surface the failure via their RunE return.
	globalConfigErr error
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "xrpld",
	Short: "go-xrpl - XRPL Node Implementation in Go",
	Long: `go-xrpl is an idiomatic Go implementation of an XRPL (XRP Ledger) client
with concurrent processing capabilities. This is NOT a direct translation of the
C++ rippled implementation but rather a native Go implementation that follows
Go conventions and patterns while maintaining protocol compatibility.`,
	Version: version.Version,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	// Register every transaction type with the tx registry before any
	// subcommand can run. Safe to call multiple times.
	all.RegisterAll()

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)

	// Global flags — operational concerns only
	rootCmd.PersistentFlags().StringVar(&configFile, "conf", "", "configuration file path (required)")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "enable normally suppressed debug logging")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose logging")

	// The replay developer commands live in their own package; register
	// them here rather than via self-registration into this package's root.
	for _, c := range replaytool.NewCommands() {
		rootCmd.AddCommand(c)
	}
}

// initConfig loads the configuration file when --conf is set; load
// errors land in globalConfigErr so commands that don't need config
// (help, generate-config) still run.
func initConfig() {
	globalConfig = nil
	globalConfigErr = nil
	if configFile == "" {
		return
	}
	cfg, err := config.LoadConfig(config.ConfigPaths{Main: configFile})
	if err != nil {
		globalConfigErr = fmt.Errorf("configuration error: %w", err)
		return
	}
	globalConfig = cfg
}

// requireConfig returns the loaded config or an error suitable for
// returning from a cobra RunE.
func requireConfig() (*config.Config, error) {
	if globalConfigErr != nil {
		return nil, globalConfigErr
	}
	if globalConfig == nil {
		return nil, fmt.Errorf("missing --conf flag: this command requires a configuration file")
	}
	return globalConfig, nil
}
