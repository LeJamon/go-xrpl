package crypto

type Algorithm interface {
	Prefix() byte
	// FamilySeedPrefix returns the byte sequence prepended to a 16-byte family
	// seed entropy before base58check encoding. For secp256k1 this is a single
	// byte (0x21); for ed25519 it is a three-byte sequence (0x01, 0xE1, 0x4B).
	FamilySeedPrefix() []byte
}
