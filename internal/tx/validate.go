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
