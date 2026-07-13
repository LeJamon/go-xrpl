package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	ledgerheader "github.com/LeJamon/go-xrpl/internal/ledger/header"
	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	ledgerstate "github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

// LedgerDataMethod handles the ledger_data RPC method
type LedgerDataMethod struct{ BaseHandler }

func (m *LedgerDataMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	rawParams := make(map[string]json.RawMessage)
	if len(params) > 0 {
		if err := json.Unmarshal(params, &rawParams); err != nil {
			return nil, types.RPCErrorInvalidParams("Invalid parameters")
		}
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	selection, selErr := parseRawLedgerSelector(rawParams, ledgerselector.Current(), lookupLedgerSelectorErrors)
	if selErr != nil {
		return nil, selErr
	}
	if _, resolveErr := resolveLedgerSelection(ctx, selection); resolveErr != nil {
		return nil, resolveErr
	}
	ledgerIndex := selection.String()

	// Validate a present marker up front, mirroring rippled's doLedgerData which
	// runs key.parseHex before touching the view: a present non-string marker
	// (including JSON null), or a present string parseHex rejects, is "not
	// valid". parseHex accepts the literal "0" as the all-zero key and otherwise
	// requires exactly 64 hex chars; the empty string fails its length check. An
	// absent marker is a fresh first-page query, signalled to the service by the
	// empty sentinel. Marker stays raw JSON so a present null — which decodes to
	// a nil any, indistinguishable from an absent marker — is still rejected, as
	// rippled's isMember + isString checks do.
	markerStr := ""
	if rawMarker, ok := rawParams["marker"]; ok {
		var m string
		if err := json.Unmarshal(rawMarker, &m); err != nil {
			return nil, types.RPCErrorExpectedField("marker", "valid")
		}
		switch m {
		case "":
			return nil, types.RPCErrorExpectedField("marker", "valid")
		case "0":
			// The all-zero key: iterate from the first entry but, being a
			// present marker, omit the base-ledger header. Normalize to the
			// canonical 64-char form the service already parses to that key.
			markerStr = strings.Repeat("0", 64)
		default:
			markerStr = m
		}
	}

	binary := false
	if raw, ok := rawParams["binary"]; ok {
		if err := json.Unmarshal(raw, &binary); err != nil {
			return nil, types.RPCErrorExpectedField("binary", "boolean")
		}
	}
	limit, limitErr := ledgerDataLimit(rawParams, binary, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
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

	entryType, typeErr := ledgerDataEntryType(rawParams)
	if typeErr != nil {
		return nil, typeErr
	}

	// Build state array based on binary flag
	state := make([]map[string]any, 0, len(result.State))
	for _, item := range result.State {
		if entryType != 0 && ledgerstate.EntryTypeCode(item.Data) != entryType {
			continue
		}
		// Ensure index is uppercase hex (matching rippled's to_string(key))
		upperIndex := strings.ToUpper(item.Index)

		if binary {
			// Binary format: data as uppercase hex and index
			state = append(state, map[string]any{
				"data":  strings.ToUpper(hex.EncodeToString(item.Data)),
				"index": upperIndex,
			})
		} else {
			// JSON format: deserialize the ledger entry
			jsonObj, err := deserializeLedgerEntry(item.Data)
			if err != nil {
				// Fallback to binary format if deserialization fails
				state = append(state, map[string]any{
					"data":  strings.ToUpper(hex.EncodeToString(item.Data)),
					"index": upperIndex,
				})
			} else {
				if objMap, ok := jsonObj.(map[string]any); ok {
					objMap["index"] = upperIndex
					state = append(state, objMap)
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
		"state": state,
	}
	fillLedgerFields(response, ledgerIndex, FormatLedgerHash(result.LedgerHash), result.LedgerIndex, ctx.Services.Ledger.GetCurrentLedgerIndex(), result.Validated)
	response["ledger_hash"] = FormatLedgerHash(result.LedgerHash)
	response["ledger_index"] = result.LedgerIndex

	// Include ledger header info on first query (when no marker was provided)
	if result.LedgerHeader != nil {
		response["ledger"] = ledgerDataHeader(result.LedgerHeader, binary, ctx.ApiVersion)
	}

	if result.Marker != "" {
		response["marker"] = result.Marker
	}

	return response, nil
}

func ledgerDataLimit(params map[string]json.RawMessage, binary, unlimited bool) (uint32, *types.RPCError) {
	maxLimit := uint64(LimitLedgerData.Default)
	if binary {
		maxLimit = uint64(LimitLedgerDataBinary.Default)
	}
	raw, ok := params["limit"]
	if !ok {
		return uint32(maxLimit), nil
	}

	value := string(raw)
	if strings.ContainsAny(value, ".eE") || value == "" || value == "null" || value == "true" || value == "false" {
		return 0, types.RPCErrorExpectedField("limit", "integer")
	}
	if strings.HasPrefix(value, "-") {
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return 0, types.RPCErrorExpectedField("limit", "integer")
		}
		return uint32(maxLimit), nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, types.RPCErrorExpectedField("limit", "integer")
	}
	if !unlimited && parsed > maxLimit {
		parsed = maxLimit
	}
	if parsed > uint64(^uint32(0)) {
		return 0, types.RPCErrorInvalidParams("Invalid parameters.")
	}
	return uint32(parsed), nil
}

type ledgerDataType struct {
	canonical string
	rpc       string
	code      uint16
}

var ledgerDataTypes = [...]ledgerDataType{
	{"NFTokenOffer", "nft_offer", 0x0037},
	{"Check", "check", 0x0043},
	{"DID", "did", 0x0049},
	{"NegativeUNL", "nunl", 0x004e},
	{"NFTokenPage", "nft_page", 0x0050},
	{"SignerList", "signer_list", 0x0053},
	{"Ticket", "ticket", 0x0054},
	{"AccountRoot", "account", 0x0061},
	{"DirectoryNode", "directory", 0x0064},
	{"Amendments", "amendments", 0x0066},
	{"LedgerHashes", "hashes", 0x0068},
	{"Bridge", "bridge", 0x0069},
	{"Offer", "offer", 0x006f},
	{"DepositPreauth", "deposit_preauth", 0x0070},
	{"XChainOwnedClaimID", "xchain_owned_claim_id", 0x0071},
	{"RippleState", "state", 0x0072},
	{"FeeSettings", "fee", 0x0073},
	{"XChainOwnedCreateAccountClaimID", "xchain_owned_create_account_claim_id", 0x0074},
	{"Escrow", "escrow", 0x0075},
	{"PayChannel", "payment_channel", 0x0078},
	{"AMM", "amm", 0x0079},
	{"MPTokenIssuance", "mpt_issuance", 0x007e},
	{"MPToken", "mptoken", 0x007f},
	{"Oracle", "oracle", 0x0080},
	{"Credential", "credential", 0x0081},
	{"PermissionedDomain", "permissioned_domain", 0x0082},
	{"Delegate", "delegate", 0x0083},
	{"Vault", "vault", 0x0084},
	{"LoanBroker", "loan_broker", 0x0088},
	{"Loan", "loan", 0x0089},
}

func ledgerDataEntryType(params map[string]json.RawMessage) (uint16, *types.RPCError) {
	raw, ok := params["type"]
	if !ok {
		return 0, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, types.RPCErrorExpectedField("type", "string")
	}
	for _, candidate := range ledgerDataTypes {
		if strings.EqualFold(value, candidate.canonical) || value == candidate.rpc {
			return candidate.code, nil
		}
	}
	return 0, types.RPCErrorInvalidField("type")
}

func ledgerDataHeader(header *types.LedgerHeaderInfo, binary bool, apiVersion int) map[string]any {
	if binary {
		ledger := map[string]any{"closed": header.Closed}
		if !header.Closed {
			return ledger
		}
		rawHeader := ledgerheader.AddRaw(ledgerheader.LedgerHeader{
			LedgerIndex:         header.LedgerIndex,
			ParentCloseTime:     protocol.FromRippleTime(uint32(max(header.ParentCloseTime, 0))),
			ParentHash:          header.ParentHash,
			TxHash:              header.TransactionHash,
			AccountHash:         header.AccountHash,
			Drops:               header.TotalCoins,
			CloseFlags:          header.CloseFlags,
			CloseTimeResolution: header.CloseTimeResolution,
			CloseTime:           protocol.FromRippleTime(uint32(max(header.CloseTime, 0))),
		}, false)
		ledger["ledger_data"] = strings.ToUpper(hex.EncodeToString(rawHeader))
		return ledger
	}

	var ledgerIndex any = header.LedgerIndex
	if apiVersion <= types.ApiVersion1 {
		ledgerIndex = strconv.FormatUint(uint64(header.LedgerIndex), 10)
	}
	ledger := map[string]any{
		"parent_hash":  FormatLedgerHash(header.ParentHash),
		"ledger_index": ledgerIndex,
		"closed":       header.Closed,
	}
	if !header.Closed {
		return ledger
	}
	ledger["account_hash"] = FormatLedgerHash(header.AccountHash)
	ledger["close_flags"] = header.CloseFlags
	ledger["close_time"] = header.CloseTime
	ledger["close_time_resolution"] = header.CloseTimeResolution
	ledger["ledger_hash"] = FormatLedgerHash(header.LedgerHash)
	ledger["parent_close_time"] = header.ParentCloseTime
	ledger["total_coins"] = fmt.Sprintf("%d", header.TotalCoins)
	ledger["transaction_hash"] = FormatLedgerHash(header.TransactionHash)
	if header.CloseTime != 0 {
		ledger["close_time_human"] = header.CloseTimeHuman
		ledger["close_time_iso"] = header.CloseTimeISO
		if header.CloseFlags&ledgerheader.LCFNoConsensusTime != 0 {
			ledger["close_time_estimated"] = true
		}
	}
	return ledger
}

// deserializeLedgerEntry converts binary ledger entry data to JSON format
func deserializeLedgerEntry(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}

	// Use the binary codec's Decode function to convert binary to JSON
	return binarycodec.Decode(hex.EncodeToString(data))
}
