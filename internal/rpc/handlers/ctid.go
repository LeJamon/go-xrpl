package handlers

import "fmt"

const (
	maxCTIDLedgerSequence = uint32(0x0FFFFFFF)
	maxCTIDComponent      = uint32(0xFFFF)
)

// EncodeCTID returns the canonical CTID when all components fit its wire format.
func EncodeCTID(ledgerSequence, transactionIndex, networkID uint32) (string, bool) {
	if ledgerSequence > maxCTIDLedgerSequence ||
		transactionIndex > maxCTIDComponent ||
		networkID > maxCTIDComponent {
		return "", false
	}

	value := uint64(0xC)<<60 |
		uint64(ledgerSequence)<<32 |
		uint64(transactionIndex)<<16 |
		uint64(networkID)
	return fmt.Sprintf("%016X", value), true
}
