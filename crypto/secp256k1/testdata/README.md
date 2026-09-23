`rippled-3.3.0.json` pins key and signature expectations to rippled tag `3.3.0`,
commit `00a178fb92ca49521b937ae1a99d863765ea8a90`.

The account seeds, private keys and public keys are entries 0–3 and 93–94 of
`src/test/protocol/SecretKey_test.cpp`'s `kSecP256K1TestVectors`. Validator keys
and deterministic signatures were generated independently of the Go code,
using SHA-512Half and libsecp256k1 calls matching
`src/libxrpl/protocol/SecretKey.cpp`:

- `deriveDeterministicRootKey`: hash the seed followed by a big-endian counter,
  accepting the first valid secret scalar within 128 attempts.
- `Generator`: create the compressed root public key, hash it followed by the
  zero account ordinal and counter, and add the resulting secret tweak.
- `signDigest`: RFC6979 ECDSA over the supplied digest, serialized as low-S DER.

The independent derivation was checked against all 95 upstream account vectors
before selecting these fixtures. Public-generator derivation must reproduce the
same fixed account public key. Both message and digest signing must reproduce
the fixed signature bytes. These fixtures are source-derived expectations;
they do not require a rippled executable during Go tests.
