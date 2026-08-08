//go:build cgo

// Package shim is the cgo binding for the OpenSSL TLS engine used by peertls.
package shim

// #cgo pkg-config: libssl libcrypto
// #include <stdlib.h>
// #include "shim.h"
import "C"

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"unsafe"
)

const errorBufferSize = C.PEERTLS_ERROR_SIZE

const (
	errCodeWantRead   = int(C.PEERTLS_ERR_WANT_READ)
	errCodeWantWrite  = int(C.PEERTLS_ERR_WANT_WRITE)
	errCodeSyscall    = int(C.PEERTLS_ERR_SYSCALL)
	errCodeSSL        = int(C.PEERTLS_ERR_SSL)
	errCodeZeroReturn = int(C.PEERTLS_ERR_ZERO_RET)
	errCodeWriteRetry = int(C.PEERTLS_ERR_WRITE_RETRY)
)

var (
	ErrWantRead   = errors.New("peertls/shim: SSL_ERROR_WANT_READ")
	ErrWantWrite  = errors.New("peertls/shim: SSL_ERROR_WANT_WRITE")
	ErrSyscall    = errors.New("peertls/shim: SSL_ERROR_SYSCALL")
	ErrSSL        = errors.New("peertls/shim: SSL_ERROR_SSL")
	ErrZeroRet    = errors.New("peertls/shim: SSL_ERROR_ZERO_RETURN")
	ErrWriteRetry = errors.New(
		"peertls/shim: SSL_write retry data or operation changed")
	ErrOther   = errors.New("peertls/shim: OpenSSL operation failed")
	ErrClosed  = errors.New("peertls/shim: closed handle")
	ErrLength  = errors.New("peertls/shim: buffer exceeds OpenSSL int range")
	ErrContext = errors.New("peertls/shim: TLS context construction failed")
)

type errorBuffer [errorBufferSize]byte

func (b *errorBuffer) pointer() *C.char {
	return (*C.char)(unsafe.Pointer(&b[0]))
}

func (b *errorBuffer) detail() string {
	if b[0] == 0 {
		return ""
	}
	return C.GoString(b.pointer())
}

func codeError(code int, detail string) error {
	var err error
	switch code {
	case errCodeWantRead:
		return ErrWantRead
	case errCodeWantWrite:
		return ErrWantWrite
	case errCodeSyscall:
		err = ErrSyscall
	case errCodeSSL:
		err = ErrSSL
	case errCodeZeroReturn:
		return ErrZeroRet
	case errCodeWriteRetry:
		err = ErrWriteRetry
	default:
		err = ErrOther
	}
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

func checkedOpenSSLLength(length int) (C.size_t, error) {
	if length < 0 || uint64(length) > math.MaxInt32 {
		return 0, ErrLength
	}
	return C.size_t(length), nil
}

// Ctx owns an OpenSSL SSL_CTX. Its methods, including Free, are serialized.
type Ctx struct {
	mu sync.Mutex
	p  *C.peertls_ctx
}

// SSL owns an OpenSSL SSL and its memory BIO pair. Its methods, including Free,
// are serialized. Free may be called more than once.
type SSL struct {
	mu sync.Mutex
	p  *C.peertls_ssl
}

func NewCtx(isServer bool) (*Ctx, error) {
	return newCtx(isServer, "", false)
}

func NewCtxWithCipherList(isServer bool, cipherList string) (*Ctx, error) {
	if cipherList == "" {
		return nil, fmt.Errorf("%w: cipher list must not be empty", ErrContext)
	}
	return newCtx(isServer, cipherList, true)
}

func newCtx(isServer bool, cipherList string, override bool) (*Ctx, error) {
	flag := C.int(0)
	if isServer {
		flag = 1
	}
	var ciphers *C.char
	if override {
		ciphers = C.CString(cipherList)
		defer C.free(unsafe.Pointer(ciphers))
	}
	var detail errorBuffer
	p := C.peertls_ctx_new(flag, ciphers, detail.pointer(), C.size_t(len(detail)))
	if p == nil {
		if message := detail.detail(); message != "" {
			return nil, fmt.Errorf("%w: %s", ErrContext, message)
		}
		return nil, ErrContext
	}
	return &Ctx{p: p}, nil
}

func (c *Ctx) Free() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p == nil {
		return
	}
	C.peertls_ctx_free(c.p)
	c.p = nil
}

func (c *Ctx) UseCertPEM(cert, key []byte) error {
	if len(cert) == 0 || len(key) == 0 {
		return errors.New("peertls/shim: cert and key must be non-empty")
	}
	certLen, err := checkedOpenSSLLength(len(cert))
	if err != nil {
		return err
	}
	keyLen, err := checkedOpenSSLLength(len(key))
	if err != nil {
		return err
	}
	if c == nil {
		return ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p == nil {
		return ErrClosed
	}
	var detail errorBuffer
	rc := C.peertls_ctx_use_cert_pem(
		c.p,
		(*C.char)(unsafe.Pointer(&cert[0])), certLen,
		(*C.char)(unsafe.Pointer(&key[0])), keyLen,
		detail.pointer(), C.size_t(len(detail)),
	)
	if rc != 0 {
		return codeError(int(rc), detail.detail())
	}
	return nil
}

func (c *Ctx) NewSSL() (*SSL, error) {
	if c == nil {
		return nil, ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p == nil {
		return nil, ErrClosed
	}
	var detail errorBuffer
	p := C.peertls_new(c.p, detail.pointer(), C.size_t(len(detail)))
	if p == nil {
		if message := detail.detail(); message != "" {
			return nil, fmt.Errorf("peertls/shim: create connection: %s", message)
		}
		return nil, errors.New("peertls/shim: create connection failed")
	}
	return &SSL{p: p}, nil
}

func (c *Ctx) protocolBounds() (int, int, error) {
	if c == nil {
		return 0, 0, ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p == nil {
		return 0, 0, ErrClosed
	}
	var minVersion C.int
	var maxVersion C.int
	if C.peertls_ctx_protocol_bounds(c.p, &minVersion, &maxVersion) != 1 {
		return 0, 0, ErrOther
	}
	return int(minVersion), int(maxVersion), nil
}

func (c *Ctx) options() (uint64, uint64, error) {
	if c == nil {
		return 0, 0, ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p == nil {
		return 0, 0, ErrClosed
	}
	return uint64(C.peertls_ctx_options(c.p)),
		uint64(C.peertls_required_options()), nil
}

func (c *Ctx) setProtocolBoundsForTest(minVersion, maxVersion int) error {
	if c == nil {
		return ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.p == nil {
		return ErrClosed
	}
	var detail errorBuffer
	rc := C.peertls_ctx_set_protocol_bounds_for_test(
		c.p, C.int(minVersion), C.int(maxVersion),
		detail.pointer(), C.size_t(len(detail)))
	if rc != 0 {
		return codeError(int(rc), detail.detail())
	}
	return nil
}

func (s *SSL) Free() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return
	}
	C.peertls_free(s.p)
	s.p = nil
}

func (s *SSL) Handshake() error {
	return s.run(false)
}

func (s *SSL) Shutdown() error {
	return s.run(true)
}

func (s *SSL) run(shutdown bool) error {
	if s == nil {
		return ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return ErrClosed
	}
	var detail errorBuffer
	var rc C.int
	if shutdown {
		rc = C.peertls_shutdown(s.p, detail.pointer(), C.size_t(len(detail)))
	} else {
		rc = C.peertls_handshake(s.p, detail.pointer(), C.size_t(len(detail)))
	}
	if rc == 0 {
		return nil
	}
	return codeError(int(rc), detail.detail())
}

func (s *SSL) Read(buf []byte) (int, error) {
	if s == nil {
		return 0, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return 0, ErrClosed
	}
	if len(buf) == 0 {
		return 0, nil
	}
	length, err := checkedOpenSSLLength(len(buf))
	if err != nil {
		return 0, err
	}
	var detail errorBuffer
	rc := C.peertls_read(s.p, unsafe.Pointer(&buf[0]), length,
		detail.pointer(), C.size_t(len(detail)))
	if rc > 0 {
		return int(rc), nil
	}
	return 0, codeError(int(rc), detail.detail())
}

func (s *SSL) Write(buf []byte) (int, error) {
	if s == nil {
		return 0, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return 0, ErrClosed
	}
	if len(buf) == 0 {
		return 0, nil
	}
	length, err := checkedOpenSSLLength(len(buf))
	if err != nil {
		return 0, err
	}
	var detail errorBuffer
	rc := C.peertls_write(s.p, unsafe.Pointer(&buf[0]), length,
		detail.pointer(), C.size_t(len(detail)))
	if rc > 0 {
		return int(rc), nil
	}
	return 0, codeError(int(rc), detail.detail())
}

func (s *SSL) BIORead(buf []byte) (int, error) {
	return s.bio(buf, false)
}

func (s *SSL) BIOWrite(buf []byte) (int, error) {
	return s.bio(buf, true)
}

func (s *SSL) bio(buf []byte, write bool) (int, error) {
	if s == nil {
		return 0, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return 0, ErrClosed
	}
	if len(buf) == 0 {
		return 0, nil
	}
	length, err := checkedOpenSSLLength(len(buf))
	if err != nil {
		return 0, err
	}
	var detail errorBuffer
	var rc C.int
	if write {
		rc = C.peertls_bio_write(s.p, unsafe.Pointer(&buf[0]), length,
			detail.pointer(), C.size_t(len(detail)))
	} else {
		rc = C.peertls_bio_read(s.p, unsafe.Pointer(&buf[0]), length,
			detail.pointer(), C.size_t(len(detail)))
	}
	if rc < 0 {
		return 0, codeError(int(rc), detail.detail())
	}
	return int(rc), nil
}

func (s *SSL) GetFinished(buf []byte) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return 0
	}
	var pointer unsafe.Pointer
	if len(buf) != 0 {
		pointer = unsafe.Pointer(&buf[0])
	}
	n := C.peertls_get_finished(s.p, pointer, C.size_t(len(buf)))
	return int(n)
}

func (s *SSL) GetPeerFinished(buf []byte) int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return 0
	}
	var pointer unsafe.Pointer
	if len(buf) != 0 {
		pointer = unsafe.Pointer(&buf[0])
	}
	n := C.peertls_get_peer_finished(s.p, pointer, C.size_t(len(buf)))
	return int(n)
}

func (s *SSL) version() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return 0
	}
	return int(C.peertls_ssl_version(s.p))
}

func (s *SSL) pendingWriteSize() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p == nil {
		return 0
	}
	return int(C.peertls_pending_write_size(s.p))
}
