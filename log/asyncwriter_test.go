package log

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuf is a concurrency-safe io.Writer so the drain goroutine and the test
// goroutine never race on the underlying buffer.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestAsyncWriter_FlushDeliversRecords verifies queued records reach the
// destination once flush drains the queue.
func TestAsyncWriter_FlushDeliversRecords(t *testing.T) {
	dst := &syncBuf{}
	w := newAsyncWriter(dst, 0)
	t.Cleanup(w.close)

	for _, s := range []string{"alpha\n", "bravo\n", "charlie\n"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatalf("Write(%q): %v", s, err)
		}
	}
	w.flush(time.Second)

	got := dst.String()
	for _, want := range []string{"alpha", "bravo", "charlie"} {
		if !strings.Contains(got, want) {
			t.Errorf("flushed output missing %q: %q", want, got)
		}
	}
	if d := w.DroppedRecords(); d != 0 {
		t.Errorf("DroppedRecords() = %d, want 0", d)
	}
}

// blockingWriter parks in Write until released, signalling entry so a test can
// drive the drain goroutine into a known blocked state.
type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.entered <- struct{}{}
	<-b.release
	return len(p), nil
}

// TestAsyncWriter_DropsWhenFull verifies that a blocked destination makes Write
// shed records (counted, never blocking) rather than parking the caller.
func TestAsyncWriter_DropsWhenFull(t *testing.T) {
	bw := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	w := newAsyncWriter(bw, 1) // queue depth 1
	t.Cleanup(w.close)

	// First record is dequeued by the drain goroutine, which then blocks in the
	// destination's Write — leaving the depth-1 queue empty.
	if _, err := w.Write([]byte("a")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	<-bw.entered

	w.Write([]byte("b")) // fills the queue
	w.Write([]byte("c")) // queue full → dropped
	w.Write([]byte("d")) // dropped

	if d := w.DroppedRecords(); d != 2 {
		t.Errorf("DroppedRecords() = %d, want 2", d)
	}
	close(bw.release)
}

// TestNewHandler_Async verifies the async config path wires an async writer that
// delivers records after a flush.
func TestNewHandler_Async(t *testing.T) {
	dst := &syncBuf{}
	cfg := &Config{Level: LevelInfo, Format: "text", Output: dst, Async: true}
	l := New(NewHandler(cfg), cfg)

	l.Info("async-hello")

	aw := cfg.asyncOut.Load()
	if aw == nil {
		t.Fatal("NewHandler did not install an async writer for Async config")
	}
	t.Cleanup(aw.close)
	aw.flush(time.Second)

	if !strings.Contains(dst.String(), "async-hello") {
		t.Errorf("async record not delivered after flush: %q", dst.String())
	}
}

// TestSync_FlushesAsyncQueue verifies the package Sync drains the async queue,
// the guarantee the watchdog abort and Fatal paths depend on.
func TestSync_FlushesAsyncQueue(t *testing.T) {
	prev := rootCfg.Load()
	t.Cleanup(func() { rootCfg.Store(prev) })

	dst := &syncBuf{}
	cfg := &Config{Level: LevelInfo, Format: "text", Output: dst, Async: true}
	l := New(NewHandler(cfg), cfg)
	t.Cleanup(func() {
		if aw := cfg.asyncOut.Load(); aw != nil {
			aw.close()
		}
	})
	rootCfg.Store(cfg)

	l.Error("abort-record")
	if err := Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !strings.Contains(dst.String(), "abort-record") {
		t.Errorf("Sync did not flush the async queue: %q", dst.String())
	}
}
