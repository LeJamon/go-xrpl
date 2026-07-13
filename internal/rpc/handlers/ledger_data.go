package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// LedgerDataMethod handles the ledger_data RPC method
type LedgerDataMethod struct{ BaseHandler }

func (m *LedgerDataMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	ledgerIndex, selErr := resolveLedgerSelector(parsedLedgerSpec)
	if selErr != nil {
		return nil, selErr
	}
	targetLedger, lookupValidated, lookupErr := LookupLedger(ctx, parsedLedgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}
	fields := make(map[string]json.RawMessage)
	if params != nil {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
	}
	markerStr := ""
	markerRaw, hasMarker := fields["marker"]
	if hasMarker {
		var marker string
		if err := json.Unmarshal(markerRaw, &marker); err != nil {
			return nil, types.RPCErrorExpectedField("marker", "valid")
		}
		switch marker {
		case "":
			return nil, types.RPCErrorExpectedField("marker", "valid")
		case "0":
			markerStr = strings.Repeat("0", 64)
		default:
			markerStr = marker
		}
	}

	binaryMode := false
	if raw, ok := fields["binary"]; ok {
		var valid bool
		binaryMode, valid = rawJSONBool(raw)
		if !valid {
			return nil, types.RPCErrorExpectedField("binary", "boolean")
		}
	}

	maxLimit := int64(LimitLedgerData.Max)
	if binaryMode {
		maxLimit = int64(LimitLedgerDataBinary.Max)
	}
	limitValue := int64(-1)
	if raw, ok := fields["limit"]; ok {
		value, err := decodeRawJSONValue(raw)
		if boolean, ok := value.(bool); ok {
			if boolean {
				limitValue = 1
			} else {
				limitValue = 0
			}
		} else if number, valid := value.(json.Number); err == nil && valid && !strings.ContainsAny(number.String(), ".eE") {
			limitValue, err = number.Int64()
			if err != nil || limitValue < math.MinInt32 || limitValue > math.MaxInt32 {
				return nil, types.RPCErrorExpectedField("limit", "integer")
			}
		} else {
			return nil, types.RPCErrorExpectedField("limit", "integer")
		}
	}
	if limitValue < 0 || (limitValue > maxLimit && !ctx.Unlimited) {
		limitValue = maxLimit
	}
	limit := uint32(limitValue)

	typeFilter := ""
	if raw, ok := fields["type"]; ok {
		if err := json.Unmarshal(raw, &typeFilter); err != nil {
			return nil, types.RPCErrorExpectedField("type", "string")
		}
		if !validLedgerDataType(typeFilter) {
			return nil, types.RPCErrorInvalidField("type")
		}
	}

	result, err := ctx.Services.Ledger.GetLedgerData(ctx.Context, ledgerIndex, limit, markerStr)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		// rippled's doLedgerData rejects a present-but-unparseable marker with
		// expected_field_error(jss::marker, "valid").
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, types.RPCErrorExpectedField("marker", "valid")
		}
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to get ledger data: %v", err))
	}

	// Build state array based on binary flag
	state := make([]map[string]any, 0, len(result.State))
	for _, item := range result.State {
		// Ensure index is uppercase hex (matching rippled's to_string(key))
		upperIndex := strings.ToUpper(item.Index)

		decoded, decodeErr := deserializeLedgerEntry(item.Data)
		decodedMap, _ := decoded.(map[string]any)
		if typeFilter != "" {
			actualType, _ := decodedMap["LedgerEntryType"].(string)
			if !ledgerDataTypeMatches(typeFilter, actualType) {
				continue
			}
		}

		if binaryMode {
			// Binary format: data as uppercase hex and index
			state = append(state, map[string]any{
				"data":  strings.ToUpper(hex.EncodeToString(item.Data)),
				"index": upperIndex,
			})
		} else {
			// JSON format: deserialize the ledger entry
			if decodeErr != nil {
				// Fallback to binary format if deserialization fails
				state = append(state, map[string]any{
					"data":  strings.ToUpper(hex.EncodeToString(item.Data)),
					"index": upperIndex,
				})
			} else {
				if decodedMap != nil {
					addLedgerEntryJSONFields(decodedMap, upperIndex)
					state = append(state, decodedMap)
				} else {
					state = append(state, map[string]any{
						"data":  strings.ToUpper(hex.EncodeToString(item.Data)),
						"index": upperIndex,
					})
				}
			}
		}
	}

	response := map[string]any{
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"state":        state,
		"validated":    result.Validated,
	}
	if !targetLedger.IsClosed() {
		response["ledger_current_index"] = targetLedger.Sequence()
	}
	response["validated"] = lookupValidated

	// Include ledger header info on first query (when no marker was provided)
	if !hasMarker {
		response["ledger"] = buildLedgerJSON(targetLedger, binaryMode, false, ctx.ApiVersion)
	}

	if result.Marker != "" {
		response["marker"] = result.Marker
	}

	return response, nil
}

func validLedgerDataType(filter string) bool {
	for _, entry := range ledgerEntryFilterTypes {
		if filter == entry.key || strings.EqualFold(entry.typeName, filter) {
			return true
		}
	}
	return false
}

func ledgerDataTypeMatches(filter, actual string) bool {
	return strings.EqualFold(filter, actual) || filter == sleTypeToRPCName(actual)
}

// formatLedgerHeaderBinary creates a hex-encoded binary representation of ledger header
func formatLedgerHeaderBinary(hdr *types.LedgerHeaderInfo) string {
	// This is a simplified binary format - real implementation would match rippled's serialization
	buf := make([]byte, 0, 4+8+len(hdr.ParentHash)+len(hdr.TransactionHash)+len(hdr.AccountHash)+4+4+1+1)

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
func deserializeLedgerEntry(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	// Use the binary codec's Decode function to convert binary to JSON
	return binarycodec.Decode(hex.EncodeToString(data))
}
