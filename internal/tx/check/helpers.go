package check

import (
	"encoding/hex"
	"fmt"
	"strconv"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// parseFee parses the fee string (in drops) to uint64.
// Returns 0 if the fee is empty or invalid.
func parseFee(fee string) uint64 {
	if fee == "" {
		return 0
	}
	v, err := strconv.ParseUint(fee, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// serializeCheck serializes a Check ledger entry
func serializeCheck(checkTx *CheckCreate, ownerID, destID [20]byte, sequence uint32, sendMax tx.Amount) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	destAddress, err := addresscodec.EncodeAccountIDToClassicAddress(destID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode destination address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType": "Check",
		"Account":         ownerAddress,
		"Destination":     destAddress,
		"Sequence":        sequence,
		"OwnerNode":       "0",
		"Flags":           uint32(0),
	}

	// Serialize SendMax - XRP as string drops, IOU as object
	if sendMax.IsNative() {
		jsonObj["SendMax"] = fmt.Sprintf("%d", sendMax.Drops())
	} else {
		jsonObj["SendMax"] = map[string]any{
			"value":    sendMax.Value(),
			"currency": sendMax.Currency,
			"issuer":   sendMax.Issuer,
		}
	}

	if checkTx.Expiration != nil {
		jsonObj["Expiration"] = *checkTx.Expiration
	}

	if checkTx.DestinationTag != nil {
		jsonObj["DestinationTag"] = *checkTx.DestinationTag
	}

	if checkTx.InvoiceID != "" {
		jsonObj["InvoiceID"] = checkTx.InvoiceID
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Check: %w", err)
	}

	return hex.DecodeString(hexStr)
}
