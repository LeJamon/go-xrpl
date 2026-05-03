package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// LedgerDataMethod handles the ledger_data RPC method
type LedgerDataMethod struct{ BaseHandler }

func (m *LedgerDataMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// Parse parameters
	var request struct {
		types.LedgerSpecifier
		Binary bool        `json:"binary,omitempty"`
		Limit  uint32      `json:"limit,omitempty"`
		Marker interface{} `json:"marker,omitempty"`
		Type   string      `json:"type,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Clamp limit using rippled's pageLength ranges from Tuning.h:
	//   binary mode: {16, 2048, 2048}
	//   JSON mode:   {16, 256, 256}
	limitRange := LimitLedgerData
	if request.Binary {
		limitRange = LimitLedgerDataBinary
	}
	limit := ClampLimit(request.Limit, limitRange, ctx.IsAdmin)

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Parse marker as string
	markerStr := ""
	if request.Marker != nil {
		if m, ok := request.Marker.(string); ok {
			markerStr = m
		}
	}

	result, err := ctx.Services.Ledger.GetLedgerData(ledgerIndex, limit, markerStr)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get ledger data: %v", err))
	}

	// Build state array based on binary flag
	state := make([]map[string]interface{}, len(result.State))
	for i, item := range result.State {
		// Ensure index is uppercase hex (matching rippled's to_string(key))
		upperIndex := strings.ToUpper(item.Index)

		if request.Binary {
			// Binary format: data as uppercase hex and index
			state[i] = map[string]interface{}{
				"data":  strings.ToUpper(hex.EncodeToString(item.Data)),
				"index": upperIndex,
			}
		} else {
			// JSON format: deserialize the ledger entry
			jsonObj, err := deserializeLedgerEntry(item.Data)
			if err != nil {
				// Fallback to binary format if deserialization fails
				state[i] = map[string]interface{}{
					"data":  strings.ToUpper(hex.EncodeToString(item.Data)),
					"index": upperIndex,
				}
			} else {
				if objMap, ok := jsonObj.(map[string]interface{}); ok {
					objMap["index"] = upperIndex
					state[i] = objMap
				} else {
					state[i] = map[string]interface{}{
						"data":  strings.ToUpper(hex.EncodeToString(item.Data)),
						"index": upperIndex,
					}
				}
			}
		}
	}

	response := map[string]interface{}{
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"state":        state,
		"validated":    result.Validated,
	}

	// Include ledger header info on first query (when no marker was provided)
	if result.LedgerHeader != nil {
		if request.Binary {
			// Binary format: include ledger_data as hex serialization
			response["ledger"] = map[string]interface{}{
				"ledger_data": strings.ToUpper(formatLedgerHeaderBinary(result.LedgerHeader)),
				"closed":      result.LedgerHeader.Closed,
			}
		} else {
			// JSON format: include full ledger header fields
			response["ledger"] = map[string]interface{}{
				"account_hash":          FormatLedgerHash(result.LedgerHeader.AccountHash),
				"close_flags":           result.LedgerHeader.CloseFlags,
				"close_time":            result.LedgerHeader.CloseTime,
				"close_time_human":      result.LedgerHeader.CloseTimeHuman,
				"close_time_iso":        result.LedgerHeader.CloseTimeISO,
				"close_time_resolution": result.LedgerHeader.CloseTimeResolution,
				"closed":                result.LedgerHeader.Closed,
				"ledger_hash":           FormatLedgerHash(result.LedgerHeader.LedgerHash),
				"ledger_index":          result.LedgerHeader.LedgerIndex,
				"parent_close_time":     result.LedgerHeader.ParentCloseTime,
				"parent_hash":           FormatLedgerHash(result.LedgerHeader.ParentHash),
				"total_coins":           fmt.Sprintf("%d", result.LedgerHeader.TotalCoins),
				"transaction_hash":      FormatLedgerHash(result.LedgerHeader.TransactionHash),
			}
		}
	}

	if result.Marker != "" {
		response["marker"] = result.Marker
		// Include limit in response only when paginating (marker present)
		response["limit"] = limit
	}

	return response, nil
}

// formatLedgerHeaderBinary creates a hex-encoded binary representation of ledger header
func formatLedgerHeaderBinary(hdr *types.LedgerHeaderInfo) string {
	// This is a simplified binary format - real implementation would match rippled's serialization
	var buf []byte

	// Sequence (4 bytes)
	seqBytes := make([]byte, 4)
	seqBytes[0] = byte(hdr.LedgerIndex >> 24)
	seqBytes[1] = byte(hdr.LedgerIndex >> 16)
	seqBytes[2] = byte(hdr.LedgerIndex >> 8)
	seqBytes[3] = byte(hdr.LedgerIndex)
	buf = append(buf, seqBytes...)

	// Total coins (8 bytes)
	coinsBytes := make([]byte, 8)
	coinsBytes[0] = byte(hdr.TotalCoins >> 56)
	coinsBytes[1] = byte(hdr.TotalCoins >> 48)
	coinsBytes[2] = byte(hdr.TotalCoins >> 40)
	coinsBytes[3] = byte(hdr.TotalCoins >> 32)
	coinsBytes[4] = byte(hdr.TotalCoins >> 24)
	coinsBytes[5] = byte(hdr.TotalCoins >> 16)
	coinsBytes[6] = byte(hdr.TotalCoins >> 8)
	coinsBytes[7] = byte(hdr.TotalCoins)
	buf = append(buf, coinsBytes...)

	// Parent hash, tx hash, account hash
	buf = append(buf, hdr.ParentHash[:]...)
	buf = append(buf, hdr.TransactionHash[:]...)
	buf = append(buf, hdr.AccountHash[:]...)

	// Parent close time (4 bytes)
	pctBytes := make([]byte, 4)
	pct := uint32(hdr.ParentCloseTime)
	pctBytes[0] = byte(pct >> 24)
	pctBytes[1] = byte(pct >> 16)
	pctBytes[2] = byte(pct >> 8)
	pctBytes[3] = byte(pct)
	buf = append(buf, pctBytes...)

	// Close time (4 bytes)
	ctBytes := make([]byte, 4)
	ct := uint32(hdr.CloseTime)
	ctBytes[0] = byte(ct >> 24)
	ctBytes[1] = byte(ct >> 16)
	ctBytes[2] = byte(ct >> 8)
	ctBytes[3] = byte(ct)
	buf = append(buf, ctBytes...)

	// Close time resolution (1 byte) and close flags (1 byte)
	buf = append(buf, byte(hdr.CloseTimeResolution))
	buf = append(buf, hdr.CloseFlags)

	return hex.EncodeToString(buf)
}

// deserializeLedgerEntry converts binary ledger entry data to JSON format
func deserializeLedgerEntry(data []byte) (interface{}, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	// Use the binary codec's Decode function to convert binary to JSON
	return binarycodec.Decode(hex.EncodeToString(data))
}
