package protocol

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// Hash256Hex returns the canonical uppercase JSON representation of a Hash256.
func Hash256Hex(hash [32]byte) string {
	return strings.ToUpper(hex.EncodeToString(hash[:]))
}

// Hash256FromHex parses exactly 32 bytes of case-insensitive hexadecimal input.
func Hash256FromHex(value string) ([32]byte, error) {
	var hash [32]byte
	if len(value) != hex.EncodedLen(len(hash)) {
		return hash, fmt.Errorf("expected %d hex characters, got %d", hex.EncodedLen(len(hash)), len(value))
	}
	if _, err := hex.Decode(hash[:], []byte(value)); err != nil {
		return [32]byte{}, err
	}
	return hash, nil
}
