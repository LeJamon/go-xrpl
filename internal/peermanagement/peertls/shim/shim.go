//go:build cgo

// Package shim provides Go bindings for the OpenSSL TLS shim used by
// peertls. It is intentionally low-level: callers in peertls own the
// goroutine pump and the BIO drain/fill cadence. Callers must call
// runtime.LockOSThread before any series of operations that may inspect
// peertls_last_error, since OpenSSL's per-thread error queue is per OS
// thread.
package shim

// #cgo pkg-config: libssl libcrypto
// #include <stdlib.h>
// #include "shim.h"
import "C"

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"
)

// Error codes mirrored from shim.h.
const (
	ErrCodeWantRead  = -1
	ErrCodeWantWrite = -2
	ErrCodeSyscall   = -3
	ErrCodeSSL       = -4
	ErrCodeZeroRet   = -5
	ErrCodeOther     = -99
)

var (
	ErrWantRead  = errors.New("peertls/shim: SSL_ERROR_WANT_READ")
	ErrWantWrite = errors.New("peertls/shim: SSL_ERROR_WANT_WRITE")
	ErrSyscall   = errors.New("peertls/shim: SSL_ERROR_SYSCALL")
	ErrSSL       = errors.New("peertls/shim: SSL_ERROR_SSL")
	ErrZeroRet   = errors.New("peertls/shim: SSL_ERROR_ZERO_RETURN")
	ErrOther     = errors.New("peertls/shim: unknown SSL error")
)

// CodeToErr maps a negative shim return code to an error.
func CodeToErr(code int) error {
	switch code {
	case ErrCodeWantRead:
		return ErrWantRead
	case ErrCodeWantWrite:
		return ErrWantWrite
	case ErrCodeSyscall:
		return ErrSyscall
	case ErrCodeSSL:
		return ErrSSL
	case ErrCodeZeroRet:
		return ErrZeroRet
	default:
		return ErrOther
	}
}

// enrichLastError returns err annotated with the OpenSSL last-error
// string from the *current OS thread*. Caller must have run the SSL
// operation that produced err under runtime.LockOSThread so the
// thread-local error queue still belongs to that operation.
//
// Only ErrSSL and ErrSyscall carry detailed OpenSSL error data; the
// other shim errors are control-flow signals (WantRead/WantWrite/...)
// and would only return stale strings.
func enrichLastError(err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrSSL) && !errors.Is(err, ErrSyscall) {
		return err
	}
	detail := lastErrorLocked()
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

func lastErrorLocked() string {
	c := C.peertls_last_error()
	if c == nil {
		return ""
	}
	return C.GoString(c)
}

// Ctx wraps peertls_ctx*.
type Ctx struct{ p *C.peertls_ctx }

// SSL wraps peertls_ssl*.
type SSL struct{ p *C.peertls_ssl }

// NewCtx creates a new SSL context. isServer selects role.
func NewCtx(isServer bool) (*Ctx, error) {
	flag := C.int(0)
	if isServer {
		flag = 1
	}
	p := C.peertls_ctx_new(flag)
	if p == nil {
		return nil, errors.New("peertls/shim: peertls_ctx_new failed")
	}
	return &Ctx{p: p}, nil
}

// Free releases the context.
func (c *Ctx) Free() {
	if c == nil || c.p == nil {
		return
	}
	C.peertls_ctx_free(c.p)
	c.p = nil
}

// UseCertPEM loads cert + key into the context.
func (c *Ctx) UseCertPEM(cert, key []byte) error {
	if len(cert) == 0 || len(key) == 0 {
		return errors.New("peertls/shim: cert and key must be non-empty")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rc := C.peertls_ctx_use_cert_pem(
		c.p,
		(*C.char)(unsafe.Pointer(&cert[0])), C.int(len(cert)),
		(*C.char)(unsafe.Pointer(&key[0])), C.int(len(key)),
	)
	if rc != 0 {
		return enrichLastError(CodeToErr(int(rc)))
	}
	return nil
}

// NewSSL creates an SSL bound to a fresh BIO_pair under the context.
func (c *Ctx) NewSSL() (*SSL, error) {
	p := C.peertls_new(c.p)
	if p == nil {
		return nil, errors.New("peertls/shim: peertls_new failed")
	}
	return &SSL{p: p}, nil
}

// Free releases the SSL and its BIO pair.
func (s *SSL) Free() {
	if s == nil || s.p == nil {
		return
	}
	C.peertls_free(s.p)
	s.p = nil
}

// Handshake drives one step of the TLS handshake. Returns nil on
// completion, ErrWantRead/ErrWantWrite if the pump must do more I/O, or
// another error.
//
// The cgo call and the OpenSSL last-error pickup must happen on the
// same OS thread (the per-thread error queue is thread-local). We
// runtime.LockOSThread for the duration of the pair so the goroutine
// can't be migrated between them.
func (s *SSL) Handshake() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rc := C.peertls_handshake(s.p)
	if rc == 0 {
		return nil
	}
	return enrichLastError(CodeToErr(int(rc)))
}

// Read decrypts up to len(buf) bytes. Returns (n, nil) on success or
// (0, err) when the SSL state machine wants more bytes (caller pumps).
func (s *SSL) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rc := C.peertls_read(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	if rc > 0 {
		return int(rc), nil
	}
	if rc == 0 {
		return 0, ErrZeroRet
	}
	return 0, enrichLastError(CodeToErr(int(rc)))
}

// Write encrypts len(buf) bytes. Same return convention as Read.
func (s *SSL) Write(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rc := C.peertls_write(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	if rc > 0 {
		return int(rc), nil
	}
	return 0, enrichLastError(CodeToErr(int(rc)))
}

// BIORead drains pending TLS record bytes from the network BIO.
// Returns (n, nil) where n may be 0 if the BIO is empty.
func (s *SSL) BIORead(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rc := C.peertls_bio_read(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	if rc < 0 {
		return 0, enrichLastError(CodeToErr(int(rc)))
	}
	return int(rc), nil
}

// BIOWrite feeds raw TLS record bytes into OpenSSL.
func (s *SSL) BIOWrite(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rc := C.peertls_bio_write(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	if rc < 0 {
		return 0, enrichLastError(CodeToErr(int(rc)))
	}
	return int(rc), nil
}

// GetFinished copies up to len(buf) bytes of the local-side Finished
// message into buf and returns the number of bytes copied. The OS
// thread is locked across the cgo call so the OpenSSL per-thread error
// queue stays consistent with any subsequent shim op on this goroutine.
func (s *SSL) GetFinished(buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	n := C.peertls_get_finished(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	return int(n)
}

// GetPeerFinished copies up to len(buf) bytes of the peer-side Finished
// message into buf and returns the number of bytes copied.
func (s *SSL) GetPeerFinished(buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	n := C.peertls_get_peer_finished(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	return int(n)
}

// Shutdown sends close_notify.
func (s *SSL) Shutdown() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rc := C.peertls_shutdown(s.p)
	if rc == 0 {
		return nil
	}
	return enrichLastError(CodeToErr(int(rc)))
}

// LastError returns the latest OpenSSL error string from this thread,
// or "" if none.
func LastError() string {
	c := C.peertls_last_error()
	if c == nil {
		return ""
	}
	return C.GoString(c)
}
