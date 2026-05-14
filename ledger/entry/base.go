package entry

// BaseEntry contains fields common to all entries
type BaseEntry struct {
	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
	Flags             uint32
}
