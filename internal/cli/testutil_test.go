package cli

import (
	"io"
	"os"
	"testing"
)

// silenceStdout redirects os.Stdout to a discarded pipe for the duration of a
// test. CLI helpers print their results to os.Stdout via the fmt package; the
// returned restore function reinstates the original stdout and must be deferred.
func silenceStdout(t *testing.T) (restore func()) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		close(done)
	}()
	return func() {
		_ = w.Close()
		<-done
		_ = r.Close()
		os.Stdout = orig
	}
}
