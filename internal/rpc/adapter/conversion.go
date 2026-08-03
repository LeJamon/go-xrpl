package adapter

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/protocol"
)

func formatLedgerHash(hash [32]byte) string {
	return protocol.Hash256Hex(hash)
}

func formatHash(data []byte) string {
	return strings.ToUpper(hex.EncodeToString(data))
}
