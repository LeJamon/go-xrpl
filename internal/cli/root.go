package cli

import (
	"fmt"
	"os"

	"github.com/LeJamon/goXRPLd/config"
	"github.com/LeJamon/goXRPLd/version"
	"github.com/spf13/cobra"
)

var (
	// Global flags
	configFile string
	debug      bool
	verbose    bool
	quiet      bool

	// globalConfig holds the loaded configuration, available to all subcommands.
	// It is nil until initConfig() runs (which happens before any command's Run function).
	globalConfig *config.Config
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "xrpld",
	Short: "goXRPLd - XRPL Node Implementation in Go",
	Long: `goXRPLd is an idiomatic Go implementation of an XRPL (XRP Ledger) client
with concurrent processing capabilities. This is NOT a direct translation of the
C++ rippled implementation but rather a native Go implementation that follows
Go conventions and patterns while maintaining protocol compatibility.`,
	Version: version.Version,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
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
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "suppress output to console after startup")
	rootCmd.PersistentFlags().Bool("silent", false, "no output to console after startup")
}

// initConfig loads and validates the configuration file.
// The --conf flag is required for commands that need config (server).
// Commands like generate-config and help work without it.
func initConfig() {
	// Skip config loading for commands that don't need it
	if configFile == "" {
		return
	}

	cfg, err := config.LoadConfig(config.ConfigPaths{Main: configFile})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error:\n%v\n", err)
		os.Exit(1)
	}

	globalConfig = cfg
}
