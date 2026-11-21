package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/exp/apidiff"
)

func TestPublicAPIChanges(t *testing.T) {
	t.Setenv("GOWORK", "off")
	load := func(files map[string]string) *apidiff.Module {
		t.Helper()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/lib\n\ngo 1.25.0\n"), 0o600))
		for path, src := range files {
			full := filepath.Join(dir, path)
			require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
			require.NoError(t, os.WriteFile(full, []byte(src), 0o600))
		}
		module, err := loadModule(dir)
		require.NoError(t, err)
		return module
	}
	base := load(map[string]string{
		"lib.go":              "package lib\nconst Exported = 1",
		"old/lib.go":          "package old",
		"old/internal/lib.go": "package internal",
		"cmd/tool/lib.go":     "package tool",
		"scripts/util/lib.go": "package util",
		"nested/main.go":      "package main\nfunc main() {}",
	})
	current := load(map[string]string{
		"lib.go":     "package lib",
		"new/lib.go": "package newlib",
	})
	require.Len(t, base.Packages, 2)
	report := apidiff.ModuleChanges(base, current)
	require.Len(t, report.Changes, 3)
	require.Contains(t, report.String(), "Exported: removed")
	require.Contains(t, report.String(), "package example.com/lib/old: removed")
	require.Contains(t, report.String(), "package example.com/lib/new: added")
}
