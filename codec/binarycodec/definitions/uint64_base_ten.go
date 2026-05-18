package definitions

// IsBaseTenUInt64FieldName reports whether the given SField is a UInt64 that
// rippled emits as a decimal string (base 10) rather than the default
// lowercase hex. Mirrors rippled SField::sMD_BaseTen, see
// rippled/include/xrpl/protocol/detail/sfields.macro and
// rippled/src/libxrpl/protocol/STInteger.cpp:246.
func IsBaseTenUInt64FieldName(name string) bool {
	switch name {
	case "MaximumAmount", "OutstandingAmount", "MPTAmount", "LockedAmount":
		return true
	}
	return false
}
