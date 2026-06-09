package cli

import (
	"runtime"
	rdebug "runtime/debug"
	"strings"
	"testing"
)

func TestVersionText_BaseLines(t *testing.T) {
	out := versionText("1.2.3")
	for _, want := range []string{
		"go-xrpl version 1.2.3\n",
		"Go version: " + runtime.Version() + "\n",
		"OS/Arch: " + runtime.GOOS + "/" + runtime.GOARCH + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("versionText missing %q in:\n%s", want, out)
		}
	}
}

func TestVCSText(t *testing.T) {
	tests := []struct {
		name     string
		settings []rdebug.BuildSetting
		want     string
	}{
		{
			name:     "no vcs info keeps output stable",
			settings: nil,
			want:     "",
		},
		{
			name: "clean revision with time",
			settings: []rdebug.BuildSetting{
				{Key: "vcs.revision", Value: "81f392511234abcd81f392511234abcd81f39251"},
				{Key: "vcs.time", Value: "2026-06-09T10:00:00Z"},
				{Key: "vcs.modified", Value: "false"},
			},
			want: "Git commit: 81f392511234\nCommit time: 2026-06-09T10:00:00Z\n",
		},
		{
			name: "dirty worktree marked modified",
			settings: []rdebug.BuildSetting{
				{Key: "vcs.revision", Value: "81f392511234abcd81f392511234abcd81f39251"},
				{Key: "vcs.modified", Value: "true"},
			},
			want: "Git commit: 81f392511234 (modified)\n",
		},
		{
			name: "short revision not truncated",
			settings: []rdebug.BuildSetting{
				{Key: "vcs.revision", Value: "81f3925"},
			},
			want: "Git commit: 81f3925\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := vcsText(tt.settings); got != tt.want {
				t.Errorf("vcsText = %q, want %q", got, tt.want)
			}
		})
	}
}
