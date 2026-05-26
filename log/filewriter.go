package log

import (
	"errors"
	"os"
	"sync"
)

// ErrLogNotRotatable is returned by Rotate when the root logger does not write
// to a rotatable file (e.g. it logs to stdout/stderr, or is not yet initialized).
var ErrLogNotRotatable = errors.New("log output is not a rotatable file")

// FileWriter is an io.Writer backed by an append-mode log file that can be
// reopened on demand. It backs the logrotate RPC: external tooling renames the
// active log file, then Rotate() closes the old descriptor and reopens the
// original path so subsequent writes land in a freshly-created file. The
// surrounding slog handler closes over the *FileWriter, so a rotation is
// transparent to it.
type FileWriter struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// NewFileWriter opens path in append mode and returns a rotatable writer.
func NewFileWriter(path string) (*FileWriter, error) {
	f, err := openLogFile(path)
	if err != nil {
		return nil, err
	}
	return &FileWriter{path: path, f: f}, nil
}

func openLogFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

// Write implements io.Writer. When the descriptor is absent (a prior reopen
// failed) writes are silently dropped, mirroring rippled's Logs::File::write
// guard against a null stream (Log.cpp).
func (w *FileWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return len(p), nil
	}
	return w.f.Write(p)
}

// Rotate closes the current descriptor and reopens the configured path, so
// writes continue against a freshly-created file after external rotation.
// Like rippled's closeAndReopen (Log.cpp), the reopen is always attempted: the
// close result is discarded (the descriptor is released regardless), and a
// failed reopen leaves no live descriptor so subsequent writes drop silently.
func (w *FileWriter) Rotate() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	f, err := openLogFile(w.path)
	if err != nil {
		return err
	}
	w.f = f
	return nil
}

// Rotate reopens the root logger's file output, returning ErrLogNotRotatable
// when logging is not file-backed (stdout/stderr or before the root config is
// set). Mirrors rippled's logs().rotate() (LogRotate.cpp).
func Rotate() error {
	cfg := rootCfg.Load()
	if cfg == nil {
		return ErrLogNotRotatable
	}
	fw, ok := cfg.Output.(*FileWriter)
	if !ok {
		return ErrLogNotRotatable
	}
	return fw.Rotate()
}
