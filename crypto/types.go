package crypto

// Algorithm describes a public-key signature scheme supported by the XRPL.
type Algorithm interface {
	Prefix() byte
	// FamilySeedPrefix returns the byte sequence prepended to a 16-byte family
	// seed entropy before base58check encoding.
	//
	// secp256k1 returns rippled's TokenType::FamilySeed (0x21), matching
	// rippled/include/xrpl/protocol/tokens.h and producing seeds that start
	// with 's'. ed25519 returns the three-byte sequence {0x01, 0xE1, 0x4B},
	// which is an XRPL ecosystem convention defined by ripple-keypairs and
	// adopted by xrpl.js / xrpl-py — it produces seeds that start with 'sEd'
	// and lets DecodeSeed recover the algorithm from the encoded string.
	// Rippled itself stores seeds algorithm-agnostically and always uses
	// TokenType::FamilySeed=0x21 (rippled/src/libxrpl/protocol/Seed.cpp);
	// the multi-byte ed25519 prefix is layered on top by client libraries.
	//
	// Callers MUST NOT mutate the returned slice.
	FamilySeedPrefix() []byte
}
