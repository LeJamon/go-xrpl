//go:build cgo

// Package shim is the cgo binding for the OpenSSL TLS shim used by
// peertls. Every cgo call locks the OS thread because OpenSSL's error
// queue is thread-local and we read it from Go on the same thread.
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

// Mirrored from shim.h.
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

// enrichLastError annotates ErrSSL/ErrSyscall with the thread-local
// OpenSSL error string. Other shim errors are control-flow signals and
// would surface stale strings.
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

type Ctx struct{ p *C.peertls_ctx }

type SSL struct{ p *C.peertls_ssl }

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

func (c *Ctx) Free() {
	if c == nil || c.p == nil {
		return
	}
	C.peertls_ctx_free(c.p)
	c.p = nil
}

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
// Free with SSL.Free; that releases the BIO too.
func (c *Ctx) NewSSL() (*SSL, error) {
	p := C.peertls_new(c.p)
	if p == nil {
		return nil, errors.New("peertls/shim: peertls_new failed")
	}
	return &SSL{p: p}, nil
}

func (s *SSL) Free() {
	if s == nil || s.p == nil {
		return
	}
	C.peertls_free(s.p)
	s.p = nil
}

// Handshake returns nil on completion, ErrWantRead/ErrWantWrite if the
// caller must pump I/O, or another error.
func (s *SSL) Handshake() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rc := C.peertls_handshake(s.p)
	if rc == 0 {
		return nil
	}
	return enrichLastError(CodeToErr(int(rc)))
}

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

// BIORead drains pending TLS records from the network BIO; returns 0
// when the BIO is empty.
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

// BIOWrite feeds raw TLS records into OpenSSL. Has partial-accept
// semantics: returns the number actually consumed.
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

// GetFinished returns the FULL Finished length even when buf is smaller;
// caller detects truncation via (returned > len(buf)).
func (s *SSL) GetFinished(buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	n := C.peertls_get_finished(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	return int(n)
}

func (s *SSL) GetPeerFinished(buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	n := C.peertls_get_peer_finished(s.p, unsafe.Pointer(&buf[0]), C.int(len(buf)))
	return int(n)
}

func (s *SSL) Shutdown() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	rc := C.peertls_shutdown(s.p)
	if rc == 0 {
		return nil
	}
	return enrichLastError(CodeToErr(int(rc)))
}

// LastError returns the OpenSSL error string from the current thread.
func LastError() string {
	c := C.peertls_last_error()
	if c == nil {
		return ""
	}
	return C.GoString(c)
}
