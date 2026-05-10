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

// IsValidPublicKey reports whether `key` is a well-formed XRPL public
// key. Mirrors rippled's publicKeyType() (PublicKey.cpp) — accepts:
//
//	33 bytes prefixed 0x02 / 0x03 (secp256k1 compressed)
//	33 bytes prefixed 0xED        (ed25519)
//	65 bytes prefixed 0x04        (secp256k1 uncompressed)
//
// Anything else is rejected. Callers that derive an address from a
// supposed pubkey to compare against an account ID MUST gate on this
// — otherwise a 33-byte payload with an arbitrary prefix can be
// hex-encoded into a valid-looking address that may collide with a
// real account, falsely qualifying for code paths reserved to the
// master signer (e.g. SetRegularKey's lsfPasswordSpent free-fee
// discount).
func IsValidPublicKey(key []byte) bool {
	if len(key) == 33 {
		return key[0] == 0x02 || key[0] == 0x03 || key[0] == 0xED
	}
	if len(key) == 65 {
		return key[0] == 0x04
	}
	return false
}
