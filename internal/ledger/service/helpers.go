package service

import (
	"errors"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/protocol"
)

func formatHashHex(hash [32]byte) string {
	return protocol.Hash256Hex(hash)
}

// decodeAccountIDLocal decodes an account address to its 20-byte ID
func decodeAccountIDLocal(address string) ([20]byte, error) {
	var accountID [20]byte
	if address == "" {
		return accountID, errors.New("empty address")
	}
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		return accountID, err
	}
	copy(accountID[:], accountIDBytes)
	return accountID, nil
}

// normalizeObjectType maps rippled's RPC type names (lowercase/snake_case)
// to the PascalCase ledger-entry type names.
func normalizeObjectType(objType string) string {
	if info, ok := protocol.LedgerEntryTypeByRPCName(objType); ok && !info.Deprecated {
		return info.Name
	}
	return objType
}
