package host

import (
	"github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/wasm"
)

// maxWasmDataLength bounds a contract's mutable data field, matching rippled's
// Protocol.h (4KB).
const maxWasmDataLength = 4 * 1024

// ComputeSha512Half returns the first 32 bytes of SHA-512 of the input.
func (e *Env) ComputeSha512Half(data []byte) ([]byte, wasm.HostFunctionError) {
	h := common.Sha512Half(data)
	b := make([]byte, 32)
	copy(b, h[:])
	return b, wasm.HfSuccess
}

// CheckSignature verifies a signature, dispatching on the public key's type. It
// returns 1 for a valid signature and 0 otherwise.
func (e *Env) CheckSignature(message, signature, pubkey []byte) (int32, wasm.HostFunctionError) {
	switch crypto.PublicKeyType(pubkey) {
	case crypto.KeyTypeSecp256k1:
		if secp256k1.SECP256K1().ValidateBytes(message, pubkey, signature) {
			return 1, wasm.HfSuccess
		}
	case crypto.KeyTypeEd25519:
		if ed25519.ED25519().ValidateBytes(message, pubkey, signature) {
			return 1, wasm.HfSuccess
		}
	default:
		// An unparseable public key is an error, not a "signature invalid"
		// result, matching rippled's `if (!publicKeyType(pubkey)) return
		// InvalidParams` (HostFuncImpl.cpp).
		return 0, wasm.HfInvalidParams
	}
	return 0, wasm.HfSuccess
}

// UpdateData stores the escrow's mutable data field, returning the byte count.
func (e *Env) UpdateData(data []byte) (int32, wasm.HostFunctionError) {
	if len(data) > maxWasmDataLength {
		return 0, wasm.HfDataFieldTooLarge
	}
	e.data = append([]byte(nil), data...)
	return int32(len(e.data)), wasm.HfSuccess
}
