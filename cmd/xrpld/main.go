package main

import (
	"github.com/LeJamon/go-xrpl/internal/cli"
)

func main() {
	// Use CLI for all cases - it handles both server mode and RPC commands
	// - No arguments: runs server (default command)
	// - With arguments: executes CLI commands
	cli.Execute()
}
