// Command apidiff compares the supported public Go packages in two module
// trees. It intentionally keeps the package selection here instead of relying
// on an entire-module comparison, which would include commands and tooling.
package main

import (
	"errors"
	"flag"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/exp/apidiff"
	"golang.org/x/tools/go/packages"
)

func main() {
	base := flag.String("base", "", "base module directory")
	newTree := flag.String("new", "", "new module directory")
	flag.Parse()
	if *base == "" || *newTree == "" || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}

	oldModule, err := loadModule(*base)
	if err != nil {
		fatal("load base module: %v", err)
	}
	newModule, err := loadModule(*newTree)
	if err != nil {
		fatal("load new module: %v", err)
	}
	if oldModule.Path != newModule.Path {
		fatal("module paths differ: %q and %q", oldModule.Path, newModule.Path)
	}

	report := apidiff.ModuleChanges(oldModule, newModule)
	sort.Slice(report.Changes, func(i, j int) bool {
		if report.Changes[i].Message != report.Changes[j].Message {
			return report.Changes[i].Message < report.Changes[j].Message
		}
		return !report.Changes[i].Compatible && report.Changes[j].Compatible
	})
	if len(report.Changes) == 0 {
		fmt.Println("No changes to the public API.")
		return
	}
	if err := report.Text(os.Stdout); err != nil {
		fatal("write report: %v", err)
	}
}

func loadModule(root string) (*apidiff.Module, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	packagesFound, err := packages.Load(&packages.Config{
		Dir:  root,
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedModule,
	}, "./...")
	if err != nil {
		return nil, err
	}

	var loadErrors []error
	var modulePath string
	for _, pkg := range packagesFound {
		for _, pkgErr := range pkg.Errors {
			loadErrors = append(loadErrors, pkgErr)
		}
		if pkg.Module != nil && pkg.Module.Main {
			if modulePath == "" {
				modulePath = pkg.Module.Path
			} else if modulePath != pkg.Module.Path {
				loadErrors = append(loadErrors, fmt.Errorf("multiple main modules: %q and %q", modulePath, pkg.Module.Path))
			}
		}
	}
	if len(loadErrors) != 0 {
		return nil, errors.Join(loadErrors...)
	}
	if modulePath == "" {
		return nil, fmt.Errorf("module path not found in %s", root)
	}

	selected := make([]*types.Package, 0, len(packagesFound))
	for _, pkg := range packagesFound {
		if pkg.Module == nil || pkg.Module.Path != modulePath || !isPublic(pkg.PkgPath, pkg.Name, modulePath) {
			continue
		}
		if pkg.Types == nil {
			return nil, fmt.Errorf("package %s has no type information", pkg.PkgPath)
		}
		selected = append(selected, pkg.Types)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Path() < selected[j].Path() })
	return &apidiff.Module{Path: modulePath, Packages: selected}, nil
}

func isPublic(pkgPath, name, modulePath string) bool {
	rel, ok := strings.CutPrefix(pkgPath, modulePath+"/")
	if !ok || name == "main" {
		return false
	}
	parts := strings.Split(rel, "/")
	if len(parts) == 0 || parts[0] == "cmd" || parts[0] == "scripts" {
		return false
	}
	for _, part := range parts {
		if part == "internal" {
			return false
		}
	}
	return true
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
