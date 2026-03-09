package handlers

import (
	"encoding/hex"
)

// FormatLedgerHash formats a 32-byte hash as hex string
func FormatLedgerHash(hash [32]byte) string {
	return hex.EncodeToString(hash[:])
}
