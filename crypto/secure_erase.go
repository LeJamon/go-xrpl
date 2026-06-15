package crypto

import (
	"runtime"
)

// SecureErase overwrites the contents of a byte slice with zeros.
//
// The implementation is a plain Go loop followed by runtime.KeepAlive(b). The
// KeepAlive prevents the compiler from dropping the backing array before the
// stores complete, but it does not stop the optimiser from eliding stores it
// can prove are dead. Treat this as best-effort scrubbing: it is comparable
// in strength to the standard library's clear(b) (Go 1.21+) and weaker than
// platform primitives like memset_s / RtlSecureZeroMemory. Remnants may
// remain in caches, registers, or swap space.
//
// See: http://www.daemonology.net/blog/2014-09-04-how-to-zero-a-buffer.html
func SecureErase(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}
