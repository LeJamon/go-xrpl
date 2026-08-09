//go:build !cgo

package peertls

import (
	"io"
	"net"
	"testing"
	"time"
)

type stubResourceConn struct{ closed bool }

func (c *stubResourceConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *stubResourceConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *stubResourceConn) Close() error                     { c.closed = true; return nil }
func (c *stubResourceConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (c *stubResourceConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (c *stubResourceConn) SetDeadline(time.Time) error      { return nil }
func (c *stubResourceConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stubResourceConn) SetWriteDeadline(time.Time) error { return nil }

type stubResourceListener struct{ closed bool }

func (l *stubResourceListener) Accept() (net.Conn, error) { return nil, io.EOF }
func (l *stubResourceListener) Close() error              { l.closed = true; return nil }
func (l *stubResourceListener) Addr() net.Addr            { return stubAddr("listener") }

type stubAddr string

func (a stubAddr) Network() string { return string(a) }
func (a stubAddr) String() string  { return string(a) }

func TestUnsupportedConstructorsDoNotCloseCallerResources(t *testing.T) {
	if Supported() {
		t.Fatal("Supported returned true in a non-CGO build")
	}
	conn := &stubResourceConn{}
	gotConn, err := Client(conn, &Config{})
	if gotConn != nil {
		t.Fatal("Client returned a connection")
	}
	if err != ErrSessionSigUnsupported {
		t.Fatalf("Client error = %v", err)
	}
	if conn.closed {
		t.Fatal("Client closed the caller connection")
	}

	listener := &stubResourceListener{}
	gotListener, err := NewListener(listener, &Config{})
	if gotListener != nil {
		t.Fatal("NewListener returned a listener")
	}
	if err != ErrSessionSigUnsupported {
		t.Fatalf("NewListener error = %v", err)
	}
	if listener.closed {
		t.Fatal("NewListener closed the caller listener")
	}
}
