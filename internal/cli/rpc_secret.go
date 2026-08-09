package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const maxRPCSecretBytes = 4096

type rpcSecretPrompt func(io.Reader, io.Writer) ([]byte, error)

type rpcSecretFlags struct {
	file   string
	stdin  bool
	prompt bool
	ask    rpcSecretPrompt
}

func bindRPCSecretFlags(cmd *cobra.Command, ask rpcSecretPrompt) *rpcSecretFlags {
	if ask == nil {
		ask = promptRPCSecret
	}

	flags := &rpcSecretFlags{ask: ask}
	cmd.Flags().BoolVar(&flags.prompt, "secret-prompt", false, "read the secret from a hidden terminal prompt")
	cmd.Flags().StringVar(&flags.file, "secret-file", "", "read the secret from an owner-only file")
	cmd.Flags().BoolVar(&flags.stdin, "secret-stdin", false, "read the secret from standard input")
	cmd.MarkFlagsMutuallyExclusive("secret-prompt", "secret-file", "secret-stdin")
	return flags
}

func (f *rpcSecretFlags) resolve(cmd *cobra.Command) (string, bool, error) {
	fileSelected := f.file != "" || cmd.Flags().Changed("secret-file")
	selected := 0
	if f.prompt {
		selected++
	}
	if fileSelected {
		selected++
	}
	if f.stdin {
		selected++
	}

	if selected == 0 {
		return "", false, nil
	}
	if selected > 1 {
		return "", false, fmt.Errorf("only one of --secret-prompt, --secret-file, and --secret-stdin may be used")
	}

	var (
		secret string
		err    error
	)
	switch {
	case f.prompt:
		var value []byte
		value, err = f.ask(cmd.InOrStdin(), cmd.ErrOrStderr())
		defer clear(value)
		if err == nil {
			secret, err = readRPCSecretRecord(bytes.NewReader(value))
		}
	case fileSelected:
		if f.file == "" {
			return "", false, fmt.Errorf("--secret-file requires a path")
		}
		secret, err = readRPCSecretFile(f.file)
	case f.stdin:
		secret, err = readRPCSecretRecord(cmd.InOrStdin())
	}
	if err != nil {
		return "", false, err
	}
	return secret, true, nil
}

func readRPCSecretFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening secret file: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspecting secret file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("secret file must be a regular file")
	}
	if permissions := info.Mode().Perm(); permissions&0o077 != 0 || permissions&0o400 == 0 {
		return "", fmt.Errorf("secret file must be owner-readable and inaccessible to group and other users (use mode 0600)")
	}

	secret, err := readRPCSecretRecord(file)
	if err != nil {
		return "", fmt.Errorf("reading secret file: %w", err)
	}
	return secret, nil
}

func readRPCSecretRecord(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxRPCSecretBytes+3))
	if err != nil {
		return "", fmt.Errorf("reading secret: %w", err)
	}
	defer clear(data)

	if len(data) == maxRPCSecretBytes+3 {
		return "", fmt.Errorf("secret exceeds the maximum length of %d bytes", maxRPCSecretBytes)
	}

	record := data
	if newline := bytes.IndexByte(data, '\n'); newline >= 0 {
		if newline != len(data)-1 {
			return "", fmt.Errorf("secret source must contain exactly one record")
		}
		record = data[:newline]
		if len(record) > 0 && record[len(record)-1] == '\r' {
			record = record[:len(record)-1]
		}
	}

	if len(record) == 0 {
		return "", fmt.Errorf("secret must not be empty")
	}
	if bytes.ContainsAny(record, "\r\n") {
		return "", fmt.Errorf("secret source must contain exactly one record")
	}
	if !utf8.Valid(record) {
		return "", fmt.Errorf("secret must be valid UTF-8")
	}
	if len(record) > maxRPCSecretBytes {
		return "", fmt.Errorf("secret exceeds the maximum length of %d bytes", maxRPCSecretBytes)
	}
	return string(record), nil
}

func promptRPCSecret(in io.Reader, out io.Writer) ([]byte, error) {
	input, ok := in.(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return nil, fmt.Errorf("cannot prompt for a secret without a terminal; use --secret-file or --secret-stdin")
	}
	if _, err := fmt.Fprint(out, "Secret: "); err != nil {
		return nil, fmt.Errorf("writing secret prompt: %w", err)
	}

	value, err := term.ReadPassword(int(input.Fd()))
	_, newlineErr := fmt.Fprintln(out)
	if err != nil {
		clear(value)
		return nil, fmt.Errorf("reading secret from terminal: %w", err)
	}
	if newlineErr != nil {
		clear(value)
		return nil, fmt.Errorf("writing secret prompt: %w", newlineErr)
	}
	return value, nil
}
