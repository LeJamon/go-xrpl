package observability

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestShutdownHTTPServerBoundsBlockingListenerClose(t *testing.T) {
	t.Parallel()

	listener := &blockingCloseListener{
		acceptStarted: make(chan struct{}),
		release:       make(chan struct{}),
	}
	server := &http.Server{ReadHeaderTimeout: time.Second}
	serveDone := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(serveDone)
	}()
	<-listener.acceptStarted

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := ShutdownHTTPServer(ctx, server)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ShutdownHTTPServer() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("ShutdownHTTPServer() took %v, want bounded return", elapsed)
	}

	close(listener.release)
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("Serve() did not return after releasing listener")
	}
}

type blockingCloseListener struct {
	acceptStarted chan struct{}
	release       chan struct{}
	acceptOnce    sync.Once
}

func (l *blockingCloseListener) Accept() (net.Conn, error) {
	l.acceptOnce.Do(func() {
		close(l.acceptStarted)
	})
	<-l.release
	return nil, net.ErrClosed
}

func (l *blockingCloseListener) Close() error {
	<-l.release
	return nil
}

func (*blockingCloseListener) Addr() net.Addr {
	return blockingListenerAddr("127.0.0.1:0")
}

type blockingListenerAddr string

func (blockingListenerAddr) Network() string  { return "tcp" }
func (a blockingListenerAddr) String() string { return string(a) }
