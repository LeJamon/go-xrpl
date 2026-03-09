package account

import (
	"encoding/hex"
	"fmt"
	"github.com/LeJamon/goXRPLd/internal/tx/signerlist"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
)

// serializeSignerList serializes a SignerList ledger entry from a SignerListSet transaction
func serializeSignerList(tx *signerlist.SignerListSet, ownerID [20]byte) ([]byte, error) {
	// Convert owner ID to classic address
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	// Build the JSON representation for the binary codec
	jsonObj := map[string]any{
		"LedgerEntryType": "SignerList",
		"Account":         ownerAddress,
		"SignerQuorum":    tx.SignerQuorum,
		"OwnerNode":       "0", // UInt64 as string
	}

	// Add SignerEntries if present
	if len(tx.SignerEntries) > 0 {
		signerEntries := make([]map[string]any, len(tx.SignerEntries))
		for i, entry := range tx.SignerEntries {
			signerEntries[i] = map[string]any{
				"SignerEntry": map[string]any{
					"Account":      entry.SignerEntry.Account,
					"SignerWeight": entry.SignerEntry.SignerWeight,
				},
			}
		}
		jsonObj["SignerEntries"] = signerEntries
	}

	// Encode using the binary codec
	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode SignerList: %w", err)
	}

	// Convert hex string to bytes
	return hex.DecodeString(hexStr)
}
