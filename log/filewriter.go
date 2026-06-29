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

// Sync flushes the underlying file descriptor to stable storage on a best-effort
// basis. It is used on the abort path so the final records survive os.Exit, which
// does not run deferred close hooks. A missing descriptor (post-failed-reopen) is
// a no-op; the fsync result is returned for callers that care, but the abort path
// ignores it.
func (w *FileWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	return w.f.Sync()
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

// Sync drains any async queue, then flushes the root logger's file output to
// stable storage on a best-effort basis. It returns nil when logging is not
// file-backed (stdout/stderr writers are flushed by syncing their own
// descriptors, not through here), but the async drain still runs so buffered
// records reach the destination. It exists for the abort path, where os.Exit
// skips deferred Close/Sync hooks.
func Sync() error {
	cfg := rootCfg.Load()
	if cfg == nil {
		return nil
	}
	if aw := cfg.asyncOut.Load(); aw != nil {
		aw.flush(asyncFlushTimeout)
	}
	fw, ok := cfg.Output.(*FileWriter)
	if !ok {
		return nil
	}
	return fw.Sync()
}
