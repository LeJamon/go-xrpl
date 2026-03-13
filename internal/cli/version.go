package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Long:  `Display version information for goXRPLd including build details and Go version.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("goXRPLd version %s\n", rootCmd.Version)
		fmt.Printf("Go version: %s\n", runtime.Version())
		fmt.Printf("OS/Arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)

		// TODO: Add build information like rippled
		// fmt.Printf("Git commit hash: %s\n", gitCommitHash)
		// fmt.Printf("Git build branch: %s\n", gitBranch)
		// fmt.Printf("Build timestamp: %s\n", buildTime)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
