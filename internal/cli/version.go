package cli

import (
	"fmt"
	"runtime"
	rdebug "runtime/debug" // aliased: the cli package has a `debug` flag variable
	"strings"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Long:  `Display version information for go-xrpl including build details and Go version.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Print(versionText(rootCmd.Version))
	},
}

// versionText renders the version command output. VCS details (commit,
// commit time, dirty marker) come from the binary's embedded build info
// and are omitted when absent (e.g. `go run` or non-VCS builds), keeping
// the base output stable.
func versionText(version string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "go-xrpl version %s\n", version)
	fmt.Fprintf(&b, "Go version: %s\n", runtime.Version())
	fmt.Fprintf(&b, "OS/Arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if info, ok := rdebug.ReadBuildInfo(); ok {
		b.WriteString(vcsText(info.Settings))
	}
	return b.String()
}

// vcsText formats the vcs.* build settings: short commit hash with a
// "(modified)" marker for dirty worktrees, plus the commit timestamp.
func vcsText(settings []rdebug.BuildSetting) string {
	kv := make(map[string]string, len(settings))
	for _, s := range settings {
		kv[s.Key] = s.Value
	}

	var b strings.Builder
	if rev := kv["vcs.revision"]; rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		if kv["vcs.modified"] == "true" {
			rev += " (modified)"
		}
		fmt.Fprintf(&b, "Git commit: %s\n", rev)
	}
	if t := kv["vcs.time"]; t != "" {
		fmt.Fprintf(&b, "Commit time: %s\n", t)
	}
	return b.String()
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
