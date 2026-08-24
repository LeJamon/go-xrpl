# Crypto

Package `crypto` contains the shared cryptographic contracts and helpers used by
go-xrpl. Concrete XRPL signing algorithms are provided by the `ed25519` and
`secp256k1` subpackages.

This package is protocol-oriented. It implements XRPL key encodings, family-seed
derivation, signature formats, and canonicality rules; it is not intended to be
a general-purpose cryptography library.

## Packages

| Package | Purpose |
| --- | --- |
| `crypto` | Common algorithm interface, key-type detection, DER handling, signature canonicality, random seed entropy, and best-effort secret erasure |
| `crypto/ed25519` | XRPL Ed25519 key derivation, signing, and verification |
| `crypto/secp256k1` | XRPL secp256k1 key derivation, signing, and verification |
| `crypto/sha512half` | SHA-512Half, the first 32 bytes of SHA-512 |
| `crypto/rfc1751` | RFC 1751 English-word encoding used by legacy seed tooling |
| `crypto/mptcrypto` | Optional native backend for Confidential MPT operations; see its [setup and distribution notes](mptcrypto/README.md) |

Directories below an `internal` directory are implementation details.

## Signing algorithms

Both algorithms implement `crypto.Algorithm`. Their zero values are ready for
use.

| Property | Ed25519 | secp256k1 |
| --- | --- | --- |
| Public-key encoding | `0xED` + 32-byte key | 33-byte compressed SEC key (`0x02` or `0x03`) |
| Private key returned by `DeriveKeypair` | `0xED` + 32-byte seed | `0x00` + 32-byte scalar |
| Signature encoding | 64-byte Ed25519 signature | DER-encoded ECDSA signature |
| Message handling | Signs message bytes directly | Signs SHA-512Half(message) |
| Validator key derivation | Not supported | Supported |

Family-seed entropy must be exactly `crypto.FamilySeedSize` (16 bytes). An
encoded XRPL seed is not accepted directly: decode it with `codec/addresscodec`
first, then pass the resulting entropy to `DeriveKeypair`.

```go
package main

import (
	"fmt"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
)

func main() {
	seed, err := rootcrypto.RandomSeed()
	if err != nil {
		panic(err)
	}
	defer rootcrypto.SecureErase(seed)

	algorithm := secp256k1.Algorithm{}
	privateKey, publicKey, err := algorithm.DeriveKeypair(seed, false)
	if err != nil {
		panic(err)
	}

	signature, err := algorithm.Sign("serialized signing payload", privateKey)
	if err != nil {
		panic(err)
	}

	fmt.Println(algorithm.Validate("serialized signing payload", publicKey, signature))
	// Output: true
}
```

The string-based `Sign` and `Validate` methods treat the Go string as raw bytes;
they do not decode hexadecimal message text. Public keys, private keys, and
signatures passed as strings are hexadecimal.

### Messages and precomputed digests

Use `Sign` and `Validate` when the input is an unhashed XRPL signing payload.
The secp256k1 implementation applies SHA-512Half before signing.

Use `secp256k1.SignDigestBytes`, `VerifyDigestBytes`, `SignDigest`, or
`ValidateDigest` only when the caller already has the exact 32-byte digest.
These APIs do not hash the digest again. Ed25519 signs message bytes directly
and therefore has no digest-signing API.

`secp256k1.Validate` requires a fully canonical low-S ECDSA signature. The
lower-level digest verification APIs accept either low-S or high-S signatures
where the XRPL protocol permits both. `crypto.ECDSACanonicality` exposes the
format and low-S classification when a caller must apply a specific rule.

## Shared helpers

- `PublicKeyType` recognizes only canonical 33-byte XRPL public-key encodings.
- `EncodeDERSignature` and `DERSigToRS` accept ECDSA scalars in the secp256k1
  range `[1, n-1]` and use strict, minimal DER integer encodings.
- `Ed25519Canonical` rejects signatures whose `S` scalar is outside the Ed25519
  subgroup order.
- `RandomBytes` and `RandomSeed` use the operating system CSPRNG.
- `sha512half.Sum(parts...)` hashes the supplied byte slices in order without
  inserting separators.
- `SecureErase` overwrites a byte slice in place on a best-effort basis. It
  cannot guarantee removal from compiler copies, registers, caches, or swap.

`rfc1751.WordFromBlob` returns a short, stable display word and is not a unique
identifier. The RFC 1751 decoder accepts the complete standard dictionary,
including `YOU`; this intentionally fixes a historical final-entry lookup bug
in rippled's legacy implementation.

## Build requirements

With cgo enabled, secp256k1 verification uses the system `libsecp256k1` through
`pkg-config`. On macOS, install `secp256k1` and `pkg-config` with Homebrew. With
`CGO_ENABLED=0`, verification uses the compatible pure-Go backend.

Confidential MPT cryptography is disabled unless the `mptcrypto` build tag,
cgo, and the locked native dependency are all available. Follow
[`crypto/mptcrypto/README.md`](mptcrypto/README.md) before enabling it; the
upstream package has separate distribution considerations.

## Verification

From the module root:

```sh
just test-pkg ./crypto/...
CGO_ENABLED=0 go test ./crypto/...
go vet ./crypto/...
```

The crypto tests include deterministic derivation and signing vectors, strict
DER and canonicality cases, fuzz seed corpora, Wycheproof ECDSA vectors, and
cgo/pure-Go verification agreement tests.
