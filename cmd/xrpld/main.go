package main

import (
	"context"
	"os"

	"github.com/LeJamon/go-xrpl/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], cli.IOStreams{
		In:     os.Stdin,
		Out:    os.Stdout,
		ErrOut: os.Stderr,
	}))
}
