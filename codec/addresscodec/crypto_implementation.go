package addresscodec

// CryptoImplementation describes the cryptographic operations the address
// codec needs from a signature algorithm: keypair derivation, signing and
// verification, plus the family-seed prefix that selects the algorithm's
// base58 seed encoding.
type CryptoImplementation interface {
	DeriveKeypair(decodedSeed []byte, validator bool) (string, string, error)
	Sign(msg, privKey string) (string, error)
	Validate(msg, pubkey, sig string) bool
	// FamilySeedPrefix returns the byte sequence prepended to the 16-byte seed
	// entropy before base58check encoding. Callers must not mutate the
	// returned slice.
	FamilySeedPrefix() []byte
}
