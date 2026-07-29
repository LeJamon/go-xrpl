package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestReadRPCSecretRecord(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "without delimiter", input: "sSecret", want: "sSecret"},
		{name: "newline", input: "sSecret\n", want: "sSecret"},
		{name: "CRLF", input: "sSecret\r\n", want: "sSecret"},
		{name: "spaces preserved", input: "  secret with spaces  \n", want: "  secret with spaces  "},
		{name: "maximum length", input: strings.Repeat("x", maxRPCSecretBytes), want: strings.Repeat("x", maxRPCSecretBytes)},
		{name: "empty", wantErr: true},
		{name: "empty line", input: "\n", wantErr: true},
		{name: "multiline", input: "first\nsecond", wantErr: true},
		{name: "bare carriage return", input: "first\rsecond", wantErr: true},
		{name: "invalid UTF-8", input: string([]byte{0xff}), wantErr: true},
		{name: "extra empty record", input: "first\n\n", wantErr: true},
		{name: "oversize", input: strings.Repeat("x", maxRPCSecretBytes+1), wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readRPCSecretRecord(strings.NewReader(test.input))
			if test.wantErr {
				if err == nil {
					t.Fatalf("readRPCSecretRecord() = %q, want error", got)
				}
				if strings.Contains(err.Error(), test.input) && test.input != "" {
					t.Fatalf("error leaks secret input: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("readRPCSecretRecord(): %v", err)
			}
			if got != test.want {
				t.Errorf("readRPCSecretRecord() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReadRPCSecretRecordBoundsReads(t *testing.T) {
	reader := &countingReader{remaining: maxRPCSecretBytes * 2}
	if _, err := readRPCSecretRecord(reader); err == nil {
		t.Fatal("expected oversized secret error")
	}
	if reader.read > maxRPCSecretBytes+3 {
		t.Fatalf("read %d bytes, limit is %d", reader.read, maxRPCSecretBytes+3)
	}
}

type countingReader struct {
	remaining int
	read      int
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(buffer), r.remaining)
	for i := range n {
		buffer[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}

func TestReadRPCSecretFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("sSecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readRPCSecretFile(path)
	if err != nil {
		t.Fatalf("readRPCSecretFile(0600): %v", err)
	}
	if got != "sSecret" {
		t.Errorf("readRPCSecretFile(0600) = %q, want %q", got, "sSecret")
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRPCSecretFile(path); err == nil {
		t.Fatal("readRPCSecretFile(0644) succeeded, want permissions error")
	}
}

func TestReadRPCSecretFileRejectsNonRegularFile(t *testing.T) {
	if _, err := readRPCSecretFile(t.TempDir()); err == nil {
		t.Fatal("readRPCSecretFile(directory) succeeded")
	}
}

func TestRPCSecretFlagsResolveSources(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		cmd, flags := newSecretTestCommand(t, nil)
		secret, provided, err := flags.resolve(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if provided || secret != "" {
			t.Fatalf("resolve() = %q, %t, want no source", secret, provided)
		}
	})

	t.Run("stdin", func(t *testing.T) {
		cmd, flags := newSecretTestCommand(t, nil, "--secret-stdin")
		cmd.SetIn(strings.NewReader("  stdin secret  \r\n"))
		secret, provided, err := flags.resolve(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if !provided || secret != "  stdin secret  " {
			t.Fatalf("resolve() = %q, %t", secret, provided)
		}
	})

	t.Run("file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(path, []byte("file secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd, flags := newSecretTestCommand(t, nil, "--secret-file", path)
		secret, provided, err := flags.resolve(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if !provided || secret != "file secret" {
			t.Fatalf("resolve() = %q, %t", secret, provided)
		}
	})

	t.Run("prompt", func(t *testing.T) {
		var gotIn io.Reader
		var gotOut io.Writer
		ask := func(in io.Reader, out io.Writer) ([]byte, error) {
			gotIn, gotOut = in, out
			return []byte("prompt secret"), nil
		}
		cmd, flags := newSecretTestCommand(t, ask, "--secret-prompt")
		in := strings.NewReader("unused")
		out := &bytes.Buffer{}
		cmd.SetIn(in)
		cmd.SetErr(out)

		secret, provided, err := flags.resolve(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if !provided || secret != "prompt secret" {
			t.Fatalf("resolve() = %q, %t", secret, provided)
		}
		if gotIn != in || gotOut != out {
			t.Fatal("prompt did not receive command input and error streams")
		}
	})
}

func TestRPCSecretFlagsRejectSelectors(t *testing.T) {
	tests := [][]string{
		{"--secret-prompt", "--secret-stdin"},
		{"--secret-prompt", "--secret-file", "path"},
		{"--secret-file", "path", "--secret-stdin"},
		{"--secret-prompt", "--secret-file", "path", "--secret-stdin"},
	}

	for _, args := range tests {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			called := false
			cmd, flags := newSecretTestCommand(t, func(io.Reader, io.Writer) ([]byte, error) {
				called = true
				return []byte("must not be read"), nil
			}, args...)
			if _, _, err := flags.resolve(cmd); err == nil {
				t.Fatal("resolve() succeeded with conflicting selectors")
			}
			if called {
				t.Fatal("prompt called before selector validation")
			}
		})
	}
}

func TestRPCSecretFlagsRejectEmptyFilePath(t *testing.T) {
	cmd, flags := newSecretTestCommand(t, nil, "--secret-file=")
	if _, _, err := flags.resolve(cmd); err == nil {
		t.Fatal("resolve() succeeded with empty --secret-file")
	}
}

func TestRPCSecretSourceErrorsDoNotLeakValues(t *testing.T) {
	const secret = "do-not-print-this-secret"
	tests := []struct {
		name string
		args []string
		in   string
		ask  rpcSecretPrompt
	}{
		{name: "stdin multiline", args: []string{"--secret-stdin"}, in: secret + "\nsecond"},
		{
			name: "prompt multiline",
			args: []string{"--secret-prompt"},
			ask: func(io.Reader, io.Writer) ([]byte, error) {
				return []byte(secret + "\nsecond"), nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd, flags := newSecretTestCommand(t, test.ask, test.args...)
			cmd.SetIn(strings.NewReader(test.in))
			_, _, err := flags.resolve(cmd)
			if err == nil {
				t.Fatal("resolve() succeeded")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaks secret: %v", err)
			}
		})
	}
}

func TestPromptRPCSecretRejectsNonTerminal(t *testing.T) {
	var output bytes.Buffer
	value, err := promptRPCSecret(strings.NewReader("visible secret"), &output)
	if err == nil {
		t.Fatalf("promptRPCSecret() = %q, want error", value)
	}
	if output.Len() != 0 {
		t.Fatalf("prompt wrote to non-terminal output: %q", output.String())
	}
}

func TestRPCSecretPromptError(t *testing.T) {
	sentinel := errors.New("terminal unavailable")
	cmd, flags := newSecretTestCommand(t, func(io.Reader, io.Writer) ([]byte, error) {
		return nil, sentinel
	}, "--secret-prompt")
	if _, _, err := flags.resolve(cmd); !errors.Is(err, sentinel) {
		t.Fatalf("resolve() error = %v, want %v", err, sentinel)
	}
}

func newSecretTestCommand(t *testing.T, ask rpcSecretPrompt, args ...string) (*cobra.Command, *rpcSecretFlags) {
	t.Helper()
	cmd := &cobra.Command{Use: "test"}
	flags := bindRPCSecretFlags(cmd, ask)
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%q): %v", args, err)
	}
	return cmd, flags
}
