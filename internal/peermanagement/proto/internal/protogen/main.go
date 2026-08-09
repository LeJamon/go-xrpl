package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	protocVersion      = "libprotoc 34.1"
	protocGenGoVersion = "protoc-gen-go v1.36.11"
)

func main() {
	checkVersion("protoc", protocVersion)
	checkVersion("protoc-gen-go", protocGenGoVersion)

	workingDirectory, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	moduleRoot, err := filepath.Abs(filepath.Join(workingDirectory, "../../.."))
	if err != nil {
		fatal(err)
	}
	command := exec.CommandContext(
		context.Background(),
		"protoc",
		"--go_out=.",
		"--go_opt=paths=source_relative",
		"--go_opt=Minternal/peermanagement/proto/xrpl.proto=github.com/LeJamon/go-xrpl/internal/peermanagement/proto",
		"internal/peermanagement/proto/xrpl.proto",
	)
	command.Dir = moduleRoot
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		fatal(err)
	}
}

func checkVersion(name, expected string) {
	output, err := exec.CommandContext(context.Background(), name, "--version").Output()
	if err != nil {
		fatal(err)
	}
	actual := string(bytes.TrimSpace(output))
	if actual != expected {
		fatal(fmt.Errorf("%s version is %q, want %q", name, actual, expected))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
