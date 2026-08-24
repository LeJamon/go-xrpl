package crypto

// Algorithm describes a public-key signature scheme supported by the XRPL:
// keypair derivation, signing and verification, plus the byte tags that
// identify the scheme in encoded private keys and family seeds.
type Algorithm interface {
	// Prefix returns the byte prepended to private keys returned by
	// DeriveKeypair.
	Prefix() byte
	// DeriveKeypair derives a keypair from a 16-byte family seed, returning the
	// hex-encoded private then public key.
	DeriveKeypair(seed []byte, validator bool) (privateKey, publicKey string, err error)
	// Sign returns the hex-encoded signature of msg under the hex-encoded
	// private key.
	Sign(msg, privKey string) (string, error)
	// Validate reports whether sig is a valid signature of msg under pubkey
	// (all hex-encoded).
	Validate(msg, pubkey, sig string) bool
	// FamilySeedPrefix returns the byte sequence prepended to a 16-byte family
	// seed entropy before base58check encoding.
	//
	// secp256k1 returns the standard family-seed prefix 0x21, producing seeds
	// that start with 's'. ed25519 returns {0x01, 0xE1, 0x4B},
	// which is an XRPL ecosystem convention defined by ripple-keypairs and
	// adopted by xrpl.js / xrpl-py — it produces seeds that start with 'sEd'
	// and lets DecodeSeed recover the algorithm from the encoded string.
	// Rippled stores seeds algorithm-agnostically with prefix 0x21; client
	// libraries layer the multi-byte Ed25519 prefix on top.
	//
	FamilySeedPrefix() []byte
}

// FamilySeedSize is the size in bytes of XRPL family-seed entropy.
const FamilySeedSize = 16
