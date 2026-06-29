package log

import (
	"io"
	"sync/atomic"
	"time"
)

// asyncQueueDepth bounds the records buffered ahead of a slow destination when
// none is configured. At the few-thousand-records/s a node emits under load
// this is roughly a second of headroom; past it records drop rather than block.
const asyncQueueDepth = 4096

// asyncFlushTimeout caps how long flush waits for the queue to clear on the
// abort path. A wedged destination is the very failure this writer guards
// against, so the bounded wait keeps an abort from hanging on a write that
// never returns.
const asyncFlushTimeout = 2 * time.Second

// asyncWriter decouples record formatting from the underlying write(2). Records
// are copied into a bounded queue drained by one background goroutine, so a slow
// or blocked destination cannot park the caller. That matters most for the
// consensus strand, which logs while holding its engine lock: a synchronous
// write stalling there starves the ledger loop and trips the watchdog to fatal.
// A full queue drops and counts records — logging is best-effort, never a brake
// on the hot path.
type asyncWriter struct {
	dst     io.Writer
	queue   chan []byte
	flushCh chan chan struct{}
	dropped atomic.Uint64
}

func newAsyncWriter(dst io.Writer, depth int) *asyncWriter {
	if depth <= 0 {
		depth = asyncQueueDepth
	}
	w := &asyncWriter{
		dst:     dst,
		queue:   make(chan []byte, depth),
		flushCh: make(chan chan struct{}),
	}
	go w.drain()
	return w
}

// Write copies p — slog reuses its buffer once Handle returns — and enqueues it
// without blocking.
func (w *asyncWriter) Write(p []byte) (int, error) {
	b := make([]byte, len(p))
	copy(b, p)
	select {
	case w.queue <- b:
	default:
		w.dropped.Add(1)
	}
	return len(p), nil
}

func (w *asyncWriter) drain() {
	for {
		select {
		case b := <-w.queue:
			_, _ = w.dst.Write(b)
		case ack := <-w.flushCh:
			w.drainPending()
			close(ack)
		}
	}
}

func (w *asyncWriter) drainPending() {
	for {
		select {
		case b := <-w.queue:
			_, _ = w.dst.Write(b)
		default:
			return
		}
	}
}

// flush blocks until the drain goroutine has written everything queued at the
// time of the call, or until timeout elapses. Used on the abort/Fatal path so
// the final records reach the destination before os.Exit.
func (w *asyncWriter) flush(timeout time.Duration) {
	ack := make(chan struct{})
	select {
	case w.flushCh <- ack:
	case <-time.After(timeout):
		return
	}
	select {
	case <-ack:
	case <-time.After(timeout):
	}
}

func (w *asyncWriter) DroppedRecords() uint64 {
	return w.dropped.Load()
}

// DroppedLogRecords returns the records the root logger dropped because its
// async queue was full, or 0 when async logging is not enabled.
func DroppedLogRecords() uint64 {
	cfg := rootCfg.Load()
	if cfg == nil {
		return 0
	}
	if aw := cfg.asyncOut.Load(); aw != nil {
		return aw.DroppedRecords()
	}
	return 0
}
