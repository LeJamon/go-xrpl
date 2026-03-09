package entry

import (
	"encoding/binary"
)

// BaseEntry contains fields common to all entries
type BaseEntry struct {
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
	Flags             uint32
}

// Hash implements a basic hashing mechanism for the base entry
func (b *BaseEntry) Hash() [32]byte {
	var result [32]byte
	binary.BigEndian.PutUint32(result[:4], b.PreviousTxnLgrSeq)
	binary.BigEndian.PutUint32(result[4:8], b.Flags)
	copy(result[8:], b.PreviousTxnID[:])
	return result
}
