// Command apidiff compares the public Go packages in two module trees.
package main

import (
	"fmt"
	"log"
	"os"
	"slices"
	"strings"

	"golang.org/x/exp/apidiff"
	"golang.org/x/tools/go/packages"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) != 3 {
		log.Fatal("usage: apidiff <base-directory> <new-directory>")
	}
	base, err := loadModule(os.Args[1])
	if err != nil {
		log.Fatalf("load base module: %v", err)
	}
	current, err := loadModule(os.Args[2])
	if err != nil {
		log.Fatalf("load current module: %v", err)
	}

	report := apidiff.ModuleChanges(base, current)
	if base.Path != current.Path {
		report.Changes = append(report.Changes, apidiff.Change{
			Message: fmt.Sprintf("module path changed from %s to %s", base.Path, current.Path),
		})
	}
	if len(report.Changes) == 0 {
		fmt.Println("No changes to the public API.")
		return
	}
	slices.SortFunc(report.Changes, func(a, b apidiff.Change) int {
		return strings.Compare(a.Message, b.Message)
	})
	if err := report.Text(os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func loadModule(dir string) (*apidiff.Module, error) {
	pkgs, err := packages.Load(&packages.Config{
		Dir:  dir,
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedModule,
	}, "./...")
	if err != nil {
		return nil, err
	}
	var module apidiff.Module
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			return nil, pkg.Errors[0]
		}
		if pkg.Module == nil || !pkg.Module.Main {
			continue
		}
		module.Path = pkg.Module.Path
		rel := strings.TrimPrefix(pkg.PkgPath, module.Path)
		parts := strings.Split(strings.TrimPrefix(rel, "/"), "/")
		if pkg.Name == "main" || parts[0] == "cmd" || parts[0] == "scripts" || slices.Contains(parts, "internal") {
			continue
		}
		module.Packages = append(module.Packages, pkg.Types)
	}
	if module.Path == "" {
		return nil, fmt.Errorf("no module packages found in %s", dir)
	}
	return &module, nil
}
