package tx

// CheckFlags validates that no unsupported flags are set.
// mask should be the bitwise complement of all valid flags (~(validFlag1 | validFlag2 | ...)).
// If any bit in the mask is set in flags, returns temINVALID_FLAG.
func CheckFlags(flags uint32, mask uint32) error {
	if flags&mask != 0 {
		return Errorf(TemINVALID_FLAG, "invalid flags")
	}
	return nil
}

// CheckNoFlags validates that zero flags are set.
// Use for transaction types that accept no flags at all.
func CheckNoFlags(flags uint32) error {
	if flags != 0 {
		return Errorf(TemINVALID_FLAG, "invalid flags")
	}
	return nil
}

// CheckDestNotSrc validates that destination is not the same as source account.
func CheckDestNotSrc(account, destination string) error {
	if account == destination {
		return Errorf(TemDST_IS_SRC, "destination may not be source")
	}
	return nil
}

// CheckDestRequired validates that a destination field is present.
func CheckDestRequired(destination string) error {
	if destination == "" {
		return Errorf(TemDST_NEEDED, "Destination is required")
	}
	return nil
}

// IsValidPublicKey mirrors rippled's publicKeyType() (PublicKey.cpp): a public
// key is valid only if it is exactly 33 bytes prefixed 0xED (ed25519) or
// 0x02 / 0x03 (secp256k1 compressed). rippled never accepts 65-byte
// uncompressed secp256k1 keys, so neither do we.
//
// Address-derivation paths that compare a derived address against an
// account ID must gate on this — otherwise an arbitrary 33-byte
// payload can hex-encode into a valid-looking address.
func IsValidPublicKey(key []byte) bool {
	if len(key) != 33 {
		return false
	}
	return key[0] == 0xED || key[0] == 0x02 || key[0] == 0x03
}
