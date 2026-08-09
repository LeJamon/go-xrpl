package state

// Hash256 returns a 32-byte Hash256 field value.
func (f Field) Hash256() [32]byte {
	var h [32]byte
	copy(h[:], f.Value)
	return h
}
