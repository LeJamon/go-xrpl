package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	ledgerstate "github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

// LedgerDataMethod handles the ledger_data RPC method
type LedgerDataMethod struct{ baseHandler }

func (m *LedgerDataMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	targetLedger, lookupValidated, lookupErr := lookupLedger(ctx, parsedLedgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}
	ledgerIndex := strconv.FormatUint(uint64(targetLedger.Sequence()), 10)
	fields := make(map[string]json.RawMessage)
	if params != nil {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
	}
	markerStr := ""
	markerRaw, hasMarker := fields["marker"]
	if hasMarker {
		var marker string
		if err := json.Unmarshal(markerRaw, &marker); err != nil {
			return nil, types.RpcErrorExpectedField("marker", "valid")
		}
		switch marker {
		case "":
			return nil, types.RpcErrorExpectedField("marker", "valid")
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
			return nil, types.RpcErrorExpectedField("binary", "boolean")
		}
	}

	limit, limitErr := ledgerDataLimit(fields, binaryMode, ctx.Role.IsUnlimited())
	if limitErr != nil {
		return nil, limitErr
	}

	entryType, typeErr := ledgerDataEntryType(fields)
	if typeErr != nil {
		return nil, typeErr
	}

	result, err := ctx.Services.Ledger.GetLedgerData(ctx.Context, ledgerIndex, limit, markerStr)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		// rippled's doLedgerData rejects a present-but-unparseable marker with
		// expected_field_error(jss::marker, "valid").
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, types.RpcErrorExpectedField("marker", "valid")
		}
		return nil, rpcInternalError("ledger_data: ledger query failed", err)
	}

	// Build state array based on binary flag
	state := make([]map[string]any, 0, len(result.State))
	for _, item := range result.State {
		if entryType != 0 {
			itemType, err := ledgerstate.DecodeType(item.Data)
			if err != nil || itemType != entryType {
				continue
			}
		}
		// Ensure index is uppercase hex (matching rippled's to_string(key))
		upperIndex := strings.ToUpper(item.Index)

		decoded, decodeErr := deserializeLedgerEntry(item.Data)
		decodedMap, _ := decoded.(map[string]any)

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
		ledgerJSON, ledgerErr := buildLedgerJSON(targetLedger, binaryMode, false, ctx.ApiVersion)
		if ledgerErr != nil {
			return nil, rpcInternalError("ledger_data: map root lookup failed", ledgerErr)
		}
		response["ledger"] = ledgerJSON
	}

	if result.Marker != "" {
		response["marker"] = result.Marker
	}

	return response, nil
}

func ledgerDataLimit(params map[string]json.RawMessage, binary, unlimited bool) (uint32, *types.RpcError) {
	maxLimit := uint64(limitLedgerData.Default)
	if binary {
		maxLimit = uint64(limitLedgerDataBinary.Default)
	}
	raw, ok := params["limit"]
	if !ok {
		return uint32(maxLimit), nil
	}

	value := string(raw)
	if strings.ContainsAny(value, ".eE") || value == "" || value == "null" || value == "true" || value == "false" {
		return 0, types.RpcErrorExpectedField("limit", "integer")
	}
	if strings.HasPrefix(value, "-") {
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return 0, types.RpcErrorExpectedField("limit", "integer")
		}
		return uint32(maxLimit), nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, types.RpcErrorExpectedField("limit", "integer")
	}
	if parsed > math.MaxInt32 {
		return 0, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	if !unlimited && parsed > maxLimit {
		parsed = maxLimit
	}
	return uint32(parsed), nil
}

func ledgerDataEntryType(params map[string]json.RawMessage) (entry.Type, *types.RpcError) {
	raw, ok := params["type"]
	if !ok {
		return 0, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, types.RpcErrorExpectedField("type", "string")
	}
	for _, candidate := range protocol.LedgerEntryTypes() {
		if candidate.Deprecated || candidate.RPCName == "" {
			continue
		}
		if strings.EqualFold(value, candidate.Name) || value == candidate.RPCName {
			return candidate.Type, nil
		}
	}
	return 0, types.RpcErrorInvalidField("type")
}

// deserializeLedgerEntry converts binary ledger entry data to JSON format
func deserializeLedgerEntry(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	// Use the binary codec's Decode function to convert binary to JSON
	return binarycodec.Decode(hex.EncodeToString(data))
}
