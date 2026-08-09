package handlers

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
)

// LedgerEntryMethod handles the ledger_entry RPC method
type LedgerEntryMethod struct{ baseHandler }

func (m *LedgerEntryMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	// We need to parse into a generic map first because the fields are polymorphic
	// (some are strings, some are objects)
	var rawParams map[string]json.RawMessage
	if err := parseParams(params, &rawParams); err != nil {
		return nil, err
	}

	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	if hasMultipleLedgerEntrySelectors(rawParams) {
		return nil, rpcerrors.RpcErrorInvalidParams("Too many fields provided.")
	}

	ledgerSpec, _, selectorErr := parseLedgerSpecifier(params)
	if selectorErr != nil {
		return nil, selectorErr
	}
	targetLedger, validated, lookupErr := lookupLedger(ctx, ledgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}
	response := ledgerEntryResponseFields(targetLedger, validated)
	ledgerIndex, selectorErr := resolveLedgerSelector(ledgerSpec)
	if selectorErr != nil {
		return nil, selectorErr
	}

	var binary bool
	if b, ok := rawParams["binary"]; ok {
		binary = jsonCppAsBool(b)
	}

	// Determine the entry key from the various object type specifiers
	var entryKey [32]byte
	var keySet bool
	var rpcErr *rpcerrors.RpcError

	// Direct index lookup
	if !keySet {
		if raw, ok := rawParams["index"]; ok {
			// API v3 accepts string shortcuts for fixed-location objects
			// whose index needs no parameters (rippled LedgerEntry.cpp
			// parseIndex, apiVersion > 2): amendments/fee/nunl/hashes.
			if ctx.ApiVersion >= types.ApiVersion3 {
				var s string
				if err := json.Unmarshal(raw, &s); err == nil {
					if k, ok := fixedIndexShortcut(s); ok {
						entryKey = k
						keySet = true
					}
				}
			}
			if !keySet {
				entryKey, rpcErr = parseHex256(raw, "index")
				if rpcErr != nil {
					return nil, rpcErr
				}
				keySet = true
			}
		}
	}

	// account / account_root: string (account address) — rippled only accepts an
	// address, no hex fallback. `account` is rippled's canonical AccountRoot
	// selector (the ledger_entries.macro rpcName); `account_root` is the
	// appended alias. Both resolve to keylet::account.
	if !keySet {
		for _, field := range []string{"account", "account_root"} {
			raw, ok := rawParams[field]
			if !ok {
				continue
			}
			var addr string
			if err := json.Unmarshal(raw, &addr); err != nil {
				return nil, rpcerrors.RpcErrorInvalidParams("Invalid " + field)
			}
			accountID, err := decodeAccountID(addr)
			if err != nil {
				return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid %s address: %v", field, err))
			}
			entryKey = keylet.Account(accountID).Key
			keySet = true
			break
		}
	}

	if !keySet {
		if raw, ok := rawParams["amendments"]; ok {
			entryKey, rpcErr = parseFixedLedgerEntry(raw, "amendments", keylet.Amendments().Key)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// amm: string (hex) or { asset, asset2 }
	if !keySet {
		if raw, ok := rawParams["amm"]; ok {
			entryKey, rpcErr = parseAMMKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// bridge: string (hex) or XChain bridge object plus bridge_account
	if !keySet {
		if raw, ok := rawParams["bridge"]; ok {
			entryKey, rpcErr = parseBridgeKeylet(rawParams, raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// check: string (hex ID)
	if !keySet {
		if raw, ok := rawParams["check"]; ok {
			entryKey, rpcErr = parseHex256(raw, "check")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// credential: string (hex) or { subject, issuer, credential_type }
	if !keySet {
		if raw, ok := rawParams["credential"]; ok {
			entryKey, rpcErr = parseCredentialKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// delegate: string (hex) or { account, authorize }
	if !keySet {
		if raw, ok := rawParams["delegate"]; ok {
			entryKey, rpcErr = parseDelegateKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// deposit_preauth: string (hex) or { owner, authorized } or { owner, authorized_credentials }
	if !keySet {
		if raw, ok := rawParams["deposit_preauth"]; ok {
			entryKey, rpcErr = parseDepositPreauthKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// did: string (account address) — rippled only accepts address, no hex fallback
	if !keySet {
		if raw, ok := rawParams["did"]; ok {
			var addr string
			if err := json.Unmarshal(raw, &addr); err != nil {
				return nil, rpcerrors.RpcErrorInvalidParams("Invalid did")
			}
			accountID, err := decodeAccountID(addr)
			if err != nil {
				return nil, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid did address: %v", err))
			}
			entryKey = keylet.DID(accountID).Key
			keySet = true
		}
	}

	// directory: string (hex) or { owner, sub_index } or { dir_root, sub_index }
	if !keySet {
		if raw, ok := rawParams["directory"]; ok {
			entryKey, rpcErr = parseDirectoryKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// escrow: string (hex) or { owner, seq }
	if !keySet {
		if raw, ok := rawParams["escrow"]; ok {
			entryKey, rpcErr = parseEscrowKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	if !keySet {
		if raw, ok := rawParams["fee"]; ok {
			entryKey, rpcErr = parseFixedLedgerEntry(raw, "fee", keylet.Fees().Key)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	if !keySet {
		if raw, ok := rawParams["hashes"]; ok {
			if negativeJSONInteger(raw) {
				if ctx.ApiVersion >= types.ApiVersion2 {
					return nil, rpcerrors.RpcErrorInvalidParams("")
				}
				return nil, rpcerrors.RpcErrorInternal()
			}
			if sequence, ok := parseJSONUInt32(raw); ok {
				entryKey = keylet.LedgerHashesForSeq(sequence).Key
			} else {
				entryKey, rpcErr = parseFixedLedgerEntry(raw, "hashes", keylet.LedgerHashes().Key)
				if rpcErr != nil {
					return nil, rpcErr
				}
			}
			keySet = true
		}
	}

	// loan: string (hex object index) or { loan_broker_id, loan_seq }
	if !keySet {
		if raw, ok := rawParams["loan"]; ok {
			entryKey, rpcErr = parseLoanKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// loan_broker: string (hex object index) or { owner, seq }
	if !keySet {
		if raw, ok := rawParams["loan_broker"]; ok {
			entryKey, rpcErr = parseLoanBrokerKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// mpt_issuance: string (hex issuance ID, 24 bytes / 48 chars) — rippled only accepts string
	if !keySet {
		if raw, ok := rawParams["mpt_issuance"]; ok {
			var idHex string
			if err := json.Unmarshal(raw, &idHex); err != nil {
				return nil, rpcerrors.RpcErrorInvalidParams("Invalid mpt_issuance")
			}
			decoded, err := hex.DecodeString(idHex)
			if err != nil || len(decoded) != 24 {
				return nil, rpcerrors.RpcErrorInvalidParams("Invalid mpt_issuance: must be 48-character hex string (24 bytes)")
			}
			var mptID [24]byte
			copy(mptID[:], decoded)
			entryKey = keylet.MPTIssuance(mptID).Key
			keySet = true
		}
	}

	// mptoken: string (hex) or { mpt_issuance_id, account }
	if !keySet {
		if raw, ok := rawParams["mptoken"]; ok {
			entryKey, rpcErr = parseMPTokenKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// nft_page: string (hex ID)
	if !keySet {
		if raw, ok := rawParams["nft_page"]; ok {
			entryKey, rpcErr = parseHex256(raw, "nft_page")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	if !keySet {
		if raw, ok := rawParams["nunl"]; ok {
			entryKey, rpcErr = parseFixedLedgerEntry(raw, "nunl", keylet.NegativeUNL().Key)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// nft_offer: string (hex ID)
	if !keySet {
		if raw, ok := rawParams["nft_offer"]; ok {
			entryKey, rpcErr = parseHex256(raw, "nft_offer")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// offer: string (hex) or { account, seq }
	if !keySet {
		if raw, ok := rawParams["offer"]; ok {
			entryKey, rpcErr = parseOfferKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// oracle: string (hex) or { account, oracle_document_id }
	if !keySet {
		if raw, ok := rawParams["oracle"]; ok {
			entryKey, rpcErr = parseOracleKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// payment_channel: string (hex ID)
	if !keySet {
		if raw, ok := rawParams["payment_channel"]; ok {
			entryKey, rpcErr = parseHex256(raw, "payment_channel")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// permissioned_domain: string (hex) or { account, seq }
	if !keySet {
		if raw, ok := rawParams["permissioned_domain"]; ok {
			entryKey, rpcErr = parsePermissionedDomainKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// ripple_state: { accounts, currency }
	if !keySet {
		if raw, ok := rawParams["ripple_state"]; ok {
			entryKey, rpcErr = parseRippleStateKeylet(raw, "ripple_state")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// state: alias for ripple_state
	if !keySet {
		if raw, ok := rawParams["state"]; ok {
			entryKey, rpcErr = parseRippleStateKeylet(raw, "state")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// signer_list: string (hex object index)
	if !keySet {
		if raw, ok := rawParams["signer_list"]; ok {
			entryKey, rpcErr = parseHex256(raw, "signer_list")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// ticket: string (hex) or { account, ticket_seq }
	if !keySet {
		if raw, ok := rawParams["ticket"]; ok {
			entryKey, rpcErr = parseTicketKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// vault: string (hex) or { owner, seq }
	if !keySet {
		if raw, ok := rawParams["vault"]; ok {
			entryKey, rpcErr = parseVaultKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// xchain_owned_claim_id: string (hex) or bridge fields plus claim sequence
	if !keySet {
		if raw, ok := rawParams["xchain_owned_claim_id"]; ok {
			entryKey, rpcErr = parseXChainClaimKeylet(raw, false)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// xchain_owned_create_account_claim_id: string (hex) or bridge fields plus claim sequence
	if !keySet {
		if raw, ok := rawParams["xchain_owned_create_account_claim_id"]; ok {
			entryKey, rpcErr = parseXChainClaimKeylet(raw, true)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	if !keySet {
		if ctx.ApiVersion >= types.ApiVersion2 {
			return nil, rpcerrors.RpcErrorInvalidParams("No ledger_entry params provided.")
		}
		return nil, rpcerrors.RpcErrorUnknownOption("").WithExtra(response)
	}

	// The type the matched filter selects (rippled's per-filter expectedType).
	// "" means the `index` alias (ltANY), which accepts any entry type.
	expectedType := expectedLedgerEntryType(rawParams)

	computedIndex := strings.ToUpper(hex.EncodeToString(entryKey[:]))
	response["index"] = computedIndex

	result, err := ctx.Services.Ledger().GetLedgerEntry(ctx.Context, entryKey, ledgerIndex)
	if err != nil {
		if errors.Is(err, svcerr.ErrLedgerEntryNotFound) {
			return nil, rpcerrors.RpcErrorEntryNotFound("").WithExtra(response)
		}
		if errors.Is(err, svcerr.ErrLedgerNotFound) {
			return nil, rpcerrors.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return nil, rpcInternalError("ledger_entry: ledger query failed", err)
	}

	// Decode the entry once — needed for the type check (in both output modes)
	// and for the JSON node body.
	decoded, decodeErr := decodeLedgerEntryNode(result.Node)

	// unexpectedLedgerType: the entry found at the requested index must match
	// the filter's type (rippled LedgerEntry.cpp:853-856). The `index` alias
	// opts out via expectedType == "".
	if expectedType != "" && decodeErr == nil {
		if actual, _ := decoded["LedgerEntryType"].(string); actual != "" && actual != expectedType {
			return nil, rpcerrors.RpcErrorUnexpectedLedgerType().WithExtra(response)
		}
	}

	if binary {
		response["node_binary"] = result.NodeBinary
	} else if decodeErr == nil {
		addLedgerEntryJSONFields(decoded, computedIndex)
		response["node"] = decoded
	} else {
		response["node"] = strings.ToUpper(hex.EncodeToString(result.Node))
	}

	return response, nil
}

func addLedgerEntryJSONFields(node map[string]any, index string) {
	node["index"] = index
	if node["LedgerEntryType"] != "MPTokenIssuance" {
		return
	}
	if _, ok := node["mpt_issuance_id"]; ok {
		return
	}

	sequenceJSON, err := json.Marshal(node["Sequence"])
	if err != nil {
		return
	}
	sequence, ok := parseJSONUInt32(sequenceJSON)
	if !ok {
		return
	}
	issuer, ok := node["Issuer"].(string)
	if !ok {
		return
	}
	issuerID, err := decodeAccountID(issuer)
	if err != nil {
		return
	}

	var issuanceID [24]byte
	binary.BigEndian.PutUint32(issuanceID[:4], sequence)
	copy(issuanceID[4:], issuerID[:])
	node["mpt_issuance_id"] = strings.ToUpper(hex.EncodeToString(issuanceID[:]))
}

func ledgerEntryResponseFields(ledger types.LedgerReader, validated bool) map[string]any {
	response := map[string]any{"validated": validated}
	if ledger.IsClosed() {
		response["ledger_hash"] = FormatLedgerHash(ledger.Hash())
		response["ledger_index"] = ledger.Sequence()
	} else {
		response["ledger_current_index"] = ledger.Sequence()
	}
	return response
}

func jsonCppAsBool(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case json.Number:
		number := value.String()
		if strings.ContainsAny(number, ".eE") {
			parsed, _ := strconv.ParseFloat(number, 64)
			return parsed != 0
		}
		return strings.ContainsAny(number, "123456789")
	case string:
		return len(value) != 0 && value[0] != 0
	case []any:
		return len(value) != 0
	case map[string]any:
		return len(value) != 0
	default:
		return false
	}
}

// decodeLedgerEntryNode decodes an SLE to its JSON object form. Production
// nodes are binary (binarycodec); the test path supplies JSON directly, so a
// JSON fallback keeps both working. Returns an error only when neither decodes.
func decodeLedgerEntryNode(node []byte) (map[string]any, error) {
	if decoded, err := binarycodec.Decode(hex.EncodeToString(node)); err == nil {
		return decoded, nil
	}
	var m map[string]any
	if err := json.Unmarshal(node, &m); err == nil {
		return m, nil
	}
	return nil, errors.New("ledger_entry: undecodable node")
}

// ledgerEntryFilterTypes maps each ledger_entry filter key to the
// LedgerEntryType name it selects, in the handler's resolution priority order.
// It mirrors rippled's per-filter expectedType (LedgerEntry.cpp dispatch
// table). `index` is absent: it is the ltANY alias and skips the type check.
var ledgerEntryFilterTypes = []struct {
	key      string
	typeName string
}{
	{"account", "AccountRoot"},
	{"account_root", "AccountRoot"},
	{"amendments", "Amendments"},
	{"amm", "AMM"},
	{"bridge", "Bridge"},
	{"check", "Check"},
	{"credential", "Credential"},
	{"delegate", "Delegate"},
	{"deposit_preauth", "DepositPreauth"},
	{"did", "DID"},
	{"directory", "DirectoryNode"},
	{"escrow", "Escrow"},
	{"fee", "FeeSettings"},
	{"hashes", "LedgerHashes"},
	{"loan", "Loan"},
	{"loan_broker", "LoanBroker"},
	{"mpt_issuance", "MPTokenIssuance"},
	{"mptoken", "MPToken"},
	{"nft_page", "NFTokenPage"},
	{"nft_offer", "NFTokenOffer"},
	{"nunl", "NegativeUNL"},
	{"offer", "Offer"},
	{"oracle", "Oracle"},
	{"payment_channel", "PayChannel"},
	{"permissioned_domain", "PermissionedDomain"},
	{"ripple_state", "RippleState"},
	{"state", "RippleState"},
	{"signer_list", "SignerList"},
	{"ticket", "Ticket"},
	{"vault", "Vault"},
	{"xchain_owned_claim_id", "XChainOwnedClaimID"},
	{"xchain_owned_create_account_claim_id", "XChainOwnedCreateAccountClaimID"},
}

var ledgerEntrySelectorFields = []string{
	"nft_offer",
	"check",
	"did",
	"nunl",
	"nft_page",
	"signer_list",
	"ticket",
	"account",
	"directory",
	"amendments",
	"hashes",
	"bridge",
	"offer",
	"deposit_preauth",
	"xchain_owned_claim_id",
	"state",
	"fee",
	"xchain_owned_create_account_claim_id",
	"escrow",
	"payment_channel",
	"amm",
	"mpt_issuance",
	"mptoken",
	"oracle",
	"credential",
	"permissioned_domain",
	"delegate",
	"vault",
	"loan_broker",
	"loan",
	"index",
	"account_root",
	"ripple_state",
}

func hasMultipleLedgerEntrySelectors(rawParams map[string]json.RawMessage) bool {
	count := 0
	for _, field := range ledgerEntrySelectorFields {
		if _, ok := rawParams[field]; !ok {
			continue
		}
		count++
		if count > 1 {
			return true
		}
	}
	return false
}

func parseFixedLedgerEntry(raw json.RawMessage, field string, fixedKey [32]byte) ([32]byte, *rpcerrors.RpcError) {
	var enabled *bool
	if err := json.Unmarshal(raw, &enabled); err == nil && enabled != nil {
		if *enabled {
			return fixedKey, nil
		}
		return [32]byte{}, rpcerrors.RpcErrorMalformedField("invalidParams", field, "true")
	}
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}
	return [32]byte{}, rpcerrors.RpcErrorMalformedField("malformedRequest", field, "hex string")
}

func parseJSONUInt32(raw json.RawMessage) (uint32, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	if number.String() == "-0" {
		return 0, true
	}
	parsed, err := strconv.ParseUint(number.String(), 10, 32)
	return uint32(parsed), err == nil
}

func parseBridgeKeylet(rawParams map[string]json.RawMessage, raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	if isJSONString(raw) {
		return [32]byte{}, rpcerrors.RpcErrorMalformedField("malformedRequest", "bridge", "hex string or object")
	}

	bridge, _, rpcErr := parseXChainBridge(raw)
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}

	bridgeAccount, rpcErr := requiredAccountIDField(rawParams, "bridge_account", "malformedBridgeAccount")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}

	lockingDoor, _ := decodeAccountID(bridge.LockingChainDoor)
	issuingDoor, _ := decodeAccountID(bridge.IssuingChainDoor)
	var issue map[string]any
	switch bridgeAccount {
	case lockingDoor:
		issue = bridge.LockingChainIssue
	case issuingDoor:
		issue = bridge.IssuingChainIssue
	default:
		return [32]byte{}, rpcerrors.NewRpcError(rpcerrors.RpcINVALID_PARAMS, "malformedRequest", "malformedRequest", "")
	}
	currency, _ := issue["currency"].(string)
	return keylet.Bridge(bridgeAccount, keylet.CurrencyBytes(currency)).Key, nil
}

type parsedXChainBridge struct {
	LockingChainDoor  string
	LockingChainIssue map[string]any
	IssuingChainDoor  string
	IssuingChainIssue map[string]any
}

func parseXChainBridge(raw json.RawMessage) (parsedXChainBridge, keylet.XChainBridge, *rpcerrors.RpcError) {
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawFields); err != nil || rawFields == nil {
		rawFields = make(map[string]json.RawMessage)
	}
	for _, field := range []string{"LockingChainDoor", "LockingChainIssue", "IssuingChainDoor", "IssuingChainIssue"} {
		value, ok := rawFields[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return parsedXChainBridge{}, keylet.XChainBridge{}, rpcerrors.RpcErrorMalformedRequestMissingField(field)
		}
	}
	for _, door := range []struct {
		field string
		token string
	}{
		{"LockingChainDoor", "malformedLockingChainDoor"},
		{"IssuingChainDoor", "malformedIssuingChainDoor"},
	} {
		var account string
		if err := json.Unmarshal(rawFields[door.field], &account); err != nil {
			return parsedXChainBridge{}, keylet.XChainBridge{}, rpcerrors.RpcErrorMalformedField(door.token, door.field, "AccountID")
		}
		accountID, err := decodeAccountID(account)
		if err != nil || accountID == ([20]byte{}) {
			return parsedXChainBridge{}, keylet.XChainBridge{}, rpcerrors.RpcErrorMalformedField(door.token, door.field, "AccountID")
		}
	}

	var bridgeKey keylet.XChainBridge
	var err error
	bridgeKey.LockingDoor, err = decodeAccountID(stringField(rawFields["LockingChainDoor"]))
	if err != nil {
		return parsedXChainBridge{}, keylet.XChainBridge{}, rpcerrors.RpcErrorMalformedField("malformedLockingChainDoor", "LockingChainDoor", "AccountID")
	}
	bridgeKey.IssuingDoor, err = decodeAccountID(stringField(rawFields["IssuingChainDoor"]))
	if err != nil {
		return parsedXChainBridge{}, keylet.XChainBridge{}, rpcerrors.RpcErrorMalformedField("malformedIssuingChainDoor", "IssuingChainDoor", "AccountID")
	}
	for _, field := range []string{"LockingChainIssue", "IssuingChainIssue"} {
		currency, issuer, rpcErr := parseBridgeIssue(rawFields[field], field)
		if rpcErr != nil {
			return parsedXChainBridge{}, keylet.XChainBridge{}, rpcErr
		}
		if field == "LockingChainIssue" {
			bridgeKey.LockingCurrency = currency
			bridgeKey.LockingIssuer = issuer
		} else {
			bridgeKey.IssuingCurrency = currency
			bridgeKey.IssuingIssuer = issuer
		}
	}

	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	lockingDoor, _ := value["LockingChainDoor"].(string)
	lockingIssue, _ := value["LockingChainIssue"].(map[string]any)
	issuingDoor, _ := value["IssuingChainDoor"].(string)
	issuingIssue, _ := value["IssuingChainIssue"].(map[string]any)
	return parsedXChainBridge{
		LockingChainDoor:  lockingDoor,
		LockingChainIssue: lockingIssue,
		IssuingChainDoor:  issuingDoor,
		IssuingChainIssue: issuingIssue,
	}, bridgeKey, nil
}

func parseXChainClaimKeylet(raw json.RawMessage, createAccount bool) ([32]byte, *rpcerrors.RpcError) {
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}
	field := "xchain_owned_claim_id"
	errorName := "malformedXChainOwnedClaimID"
	if createAccount {
		field = "xchain_owned_create_account_claim_id"
		errorName = "malformedXChainOwnedCreateAccountClaimID"
	}
	if !isJSONObject(raw) {
		return [32]byte{}, rpcerrors.RpcErrorMalformedField("malformedRequest", field, "hex string or object")
	}
	_, bridge, rpcErr := parseXChainBridge(raw)
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}
	sequence, rpcErr := requiredUInt32Field(object, field, errorName)
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	if createAccount {
		return keylet.XChainCreateAccountClaimID(bridge, uint64(sequence)).Key, nil
	}
	return keylet.XChainClaimID(bridge, uint64(sequence)).Key, nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) != 0 && trimmed[0] == '{'
}

func stringField(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func parseBridgeIssue(raw json.RawMessage, field string) ([20]byte, [20]byte, *rpcerrors.RpcError) {
	var issue map[string]json.RawMessage
	if err := json.Unmarshal(raw, &issue); err != nil || issue == nil {
		return [20]byte{}, [20]byte{}, rpcerrors.RpcErrorMalformedField("malformedIssue", field, "Issue")
	}
	if _, ok := issue["mpt_issuance_id"]; ok {
		return [20]byte{}, [20]byte{}, rpcerrors.RpcErrorMalformedField("malformedIssue", field, "Issue")
	}
	currencyRaw, ok := issue["currency"]
	if !ok {
		return [20]byte{}, [20]byte{}, rpcerrors.RpcErrorMalformedField("malformedIssue", field, "Issue")
	}
	var currencyText string
	if err := json.Unmarshal(currencyRaw, &currencyText); err != nil {
		return [20]byte{}, [20]byte{}, rpcerrors.RpcErrorMalformedField("malformedIssue", field, "Issue")
	}
	currency, err := keylet.ParseCurrency(currencyText)
	if err != nil {
		return [20]byte{}, [20]byte{}, rpcerrors.RpcErrorMalformedField("malformedIssue", field, "Issue")
	}
	issuerRaw, hasIssuer := issue["issuer"]
	if currency == ([20]byte{}) {
		if hasIssuer && !isJSONNull(issuerRaw) {
			return [20]byte{}, [20]byte{}, rpcerrors.RpcErrorMalformedField("malformedIssue", field, "Issue")
		}
		return currency, [20]byte{}, nil
	}
	if !hasIssuer {
		return [20]byte{}, [20]byte{}, rpcerrors.RpcErrorMalformedField("malformedIssue", field, "Issue")
	}
	issuerText := stringField(issuerRaw)
	issuer, err := decodeAccountID(issuerText)
	if err != nil || issuer == ([20]byte{}) || issuer == noAccountID {
		return [20]byte{}, [20]byte{}, rpcerrors.RpcErrorMalformedField("malformedIssue", field, "Issue")
	}
	return currency, issuer, nil
}

func negativeJSONInteger(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return false
	}
	number, ok := value.(json.Number)
	if !ok || strings.ContainsAny(number.String(), ".eE") {
		return false
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 64)
	return err == nil && parsed < 0
}

// expectedLedgerEntryType returns the LedgerEntryType the matched filter
// selects, or "" for the `index` alias (which accepts any type) and when no
// typed filter is present. The `index` key is checked first because it has
// resolution priority in the handler.
func expectedLedgerEntryType(rawParams map[string]json.RawMessage) string {
	if _, ok := rawParams["index"]; ok {
		return ""
	}
	for _, f := range ledgerEntryFilterTypes {
		if _, ok := rawParams[f.key]; ok {
			return f.typeName
		}
	}
	return ""
}

// decodeAccountID decodes a base58 account address to a 20-byte account ID
func decodeAccountID(address string) ([20]byte, error) {
	var accountID [20]byte
	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		return accountID, err
	}
	copy(accountID[:], idBytes)
	return accountID, nil
}

// fixedIndexShortcut resolves an API v3 `index` string shortcut to the key of
// the corresponding fixed-location ledger object (rippled LedgerEntry.cpp
// parseIndex). "hashes" is the short skip list (keylet::skip()).
func fixedIndexShortcut(s string) ([32]byte, bool) {
	switch s {
	case "amendments":
		return keylet.Amendments().Key, true
	case "fee":
		return keylet.Fees().Key, true
	case "nunl":
		return keylet.NegativeUNL().Key, true
	case "hashes":
		return keylet.LedgerHashes().Key, true
	}
	return [32]byte{}, false
}

// parseHex256 parses a JSON value as a 64-character hex string (32 bytes)
func parseHex256(raw json.RawMessage, fieldName string) ([32]byte, *rpcerrors.RpcError) {
	var result [32]byte
	var hexStr string
	if err := json.Unmarshal(raw, &hexStr); err != nil {
		return result, rpcerrors.RpcErrorInvalidParams("Invalid " + fieldName + ": must be hex string")
	}
	decoded, err := hex.DecodeString(hexStr)
	if err != nil || len(decoded) != 32 {
		return result, rpcerrors.RpcErrorInvalidParams("Invalid " + fieldName + ": must be 64-character hex string")
	}
	copy(result[:], decoded)
	return result, nil
}

// tryParseHex256 attempts to parse raw JSON as a 64-char hex string.
// Returns the parsed key and true on success, or zero-value and false if the
// raw value is not a string or not valid 32-byte hex (caller should try object form).
func tryParseHex256(raw json.RawMessage) ([32]byte, bool) {
	var hexStr string
	if err := json.Unmarshal(raw, &hexStr); err != nil {
		return [32]byte{}, false
	}
	decoded, err := hex.DecodeString(hexStr)
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, false
	}
	var result [32]byte
	copy(result[:], decoded)
	return result, true
}

// parseAMMKeylet parses an AMM specifier: string (hex) or { asset, asset2 }
// Reference: rippled LedgerEntry.cpp parseAMM()
func parseAMMKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		Asset  json.RawMessage `json:"asset"`
		Asset2 json.RawMessage `json:"asset2"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid amm params")
	}

	if req.Asset == nil || req.Asset2 == nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid amm params: asset and asset2 required")
	}

	issue1Currency, issue1Issuer, err := parseCurrencyIssuer(req.Asset)
	if err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid amm asset: %v", err))
	}
	issue2Currency, issue2Issuer, err := parseCurrencyIssuer(req.Asset2)
	if err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid amm asset2: %v", err))
	}

	return keylet.AMM(issue1Issuer, issue1Currency, issue2Issuer, issue2Currency).Key, nil
}

// parseCredentialKeylet parses a credential specifier: string (hex) or { subject, issuer, credential_type }
// Reference: rippled LedgerEntry.cpp parseCredential()
func parseCredentialKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		Subject        string `json:"subject"`
		Issuer         string `json:"issuer"`
		CredentialType string `json:"credential_type"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid credential params")
	}
	subjectID, err := decodeAccountID(req.Subject)
	if err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid credential subject: %v", err))
	}
	issuerID, err := decodeAccountID(req.Issuer)
	if err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid credential issuer: %v", err))
	}
	credType, err := hex.DecodeString(req.CredentialType)
	if err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid credential_type: must be hex string")
	}
	return keylet.Credential(subjectID, issuerID, credType).Key, nil
}

// parseDelegateKeylet parses a delegate specifier: string (hex) or { account, authorize }
// Reference: rippled LedgerEntry.cpp parseDelegate()
func parseDelegateKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		Account   string `json:"account"`
		Authorize string `json:"authorize"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid delegate params")
	}
	if req.Account == "" || req.Authorize == "" {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid delegate params: account and authorize required")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid delegate account: %v", err))
	}
	authorizeID, err := decodeAccountID(req.Authorize)
	if err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid delegate authorize: %v", err))
	}
	return keylet.Delegate(accountID, authorizeID).Key, nil
}

// parseDepositPreauthKeylet parses a deposit_preauth specifier:
// string (hex) or { owner, authorized } or { owner, authorized_credentials }
// Reference: rippled LedgerEntry.cpp parseDepositPreauth()
func parseDepositPreauthKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	obj, ok := asJSONObject(raw)
	if !ok {
		return parseSelectorHexOrObjectID(raw, "deposit_preauth")
	}

	_, hasAuthorized := obj["authorized"]
	_, hasCredentials := obj["authorized_credentials"]
	if hasAuthorized == hasCredentials {
		return [32]byte{}, rpcerrors.NewRpcError(
			rpcerrors.RpcINVALID_PARAMS,
			"malformedRequest",
			"malformedRequest",
			"Must have exactly one of `authorized` and `authorized_credentials`.",
		)
	}

	owner, rpcErr := requiredAccountIDField(obj, "owner", "malformedOwner")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	if hasAuthorized {
		var authorized string
		if err := json.Unmarshal(obj["authorized"], &authorized); err == nil {
			if account, err := decodeAccountID(authorized); err == nil && account != ([20]byte{}) {
				return keylet.DepositPreauth(owner, account).Key, nil
			}
		}
		return [32]byte{}, rpcerrors.RpcErrorMalformedField("malformedAuthorized", "authorized", "AccountID")
	}

	credentials, rpcErr := parseAuthorizedCredentials(obj["authorized_credentials"])
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	return keylet.DepositPreauthCredentials(owner, credentials).Key, nil
}

func parseAuthorizedCredentials(raw json.RawMessage) ([]keylet.CredentialPair, *rpcerrors.RpcError) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, rpcerrors.RpcErrorMalformedField(
			"malformedAuthorizedCredentials", "authorized_credentials", "array",
		)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, rpcerrors.RpcErrorMalformedField(
			"malformedAuthorizedCredentials", "authorized_credentials", "array",
		)
	}
	if len(entries) > 8 {
		return nil, rpcerrors.NewRpcError(
			rpcerrors.RpcINVALID_PARAMS,
			"malformedAuthorizedCredentials",
			"malformedAuthorizedCredentials",
			"Invalid field 'authorized_credentials', array too long.",
		)
	}
	if len(entries) == 0 {
		return nil, rpcerrors.NewRpcError(
			rpcerrors.RpcINVALID_PARAMS,
			"malformedAuthorizedCredentials",
			"malformedAuthorizedCredentials",
			"Invalid field 'authorized_credentials', array empty.",
		)
	}

	credentials := make([]keylet.CredentialPair, 0, len(entries))
	for _, rawEntry := range entries {
		entry, ok := asJSONObject(rawEntry)
		if !ok {
			return nil, rpcerrors.RpcErrorMalformedField(
				"malformedAuthorizedCredentials", "authorized_credentials", "array of objects",
			)
		}
		for _, field := range []string{"issuer", "credential_type"} {
			if isJSONFieldAbsent(entry, field) {
				return nil, rpcerrors.NewRpcError(
					rpcerrors.RpcINVALID_PARAMS,
					"malformedAuthorizedCredentials",
					"malformedAuthorizedCredentials",
					"Missing field '"+field+"'.",
				)
			}
		}

		issuer, rpcErr := requiredAccountIDField(entry, "issuer", "malformedAuthorizedCredentials")
		if rpcErr != nil {
			return nil, rpcErr
		}
		var credentialTypeText string
		if err := json.Unmarshal(entry["credential_type"], &credentialTypeText); err != nil {
			return nil, rpcerrors.RpcErrorMalformedField(
				"malformedAuthorizedCredentials", "credential_type", "hex string",
			)
		}
		credentialType, err := hex.DecodeString(credentialTypeText)
		if err != nil || len(credentialType) == 0 || len(credentialType) > 64 {
			return nil, rpcerrors.RpcErrorMalformedField(
				"malformedAuthorizedCredentials", "credential_type", "hex string",
			)
		}
		credentials = append(credentials, keylet.CredentialPair{
			Issuer:         issuer,
			CredentialType: credentialType,
		})
	}

	sort.Slice(credentials, func(i, j int) bool {
		if cmp := bytes.Compare(credentials[i].Issuer[:], credentials[j].Issuer[:]); cmp != 0 {
			return cmp < 0
		}
		return bytes.Compare(credentials[i].CredentialType, credentials[j].CredentialType) < 0
	})
	for i := 1; i < len(credentials); i++ {
		if credentials[i].Issuer == credentials[i-1].Issuer &&
			bytes.Equal(credentials[i].CredentialType, credentials[i-1].CredentialType) {
			return nil, rpcerrors.RpcErrorMalformedField(
				"malformedAuthorizedCredentials", "authorized_credentials", "array",
			)
		}
	}
	return credentials, nil
}

// parseDirectoryKeylet parses a directory specifier: string (hex) or { owner, sub_index }
// Reference: rippled LedgerEntry.cpp parseDirectory()
func parseDirectoryKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	if raw == nil || string(raw) == "null" {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid directory params")
	}

	// Try as string first (direct hex ID)
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try as object { owner, sub_index } or { dir_root, sub_index }
	var req struct {
		Owner    string `json:"owner"`
		DirRoot  string `json:"dir_root"`
		SubIndex uint64 `json:"sub_index"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid directory params")
	}

	if req.DirRoot != "" {
		if req.Owner != "" {
			// May not specify both dir_root and owner
			return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid directory: may not specify both dir_root and owner")
		}
		decoded, err := hex.DecodeString(req.DirRoot)
		if err != nil || len(decoded) != 32 {
			return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid dir_root")
		}
		var rootKey [32]byte
		copy(rootKey[:], decoded)
		return keylet.DirPage(rootKey, req.SubIndex).Key, nil
	}

	if req.Owner != "" {
		accountID, err := decodeAccountID(req.Owner)
		if err != nil {
			return [32]byte{}, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid directory owner: %v", err))
		}
		ownerDir := keylet.OwnerDir(accountID)
		return keylet.DirPage(ownerDir.Key, req.SubIndex).Key, nil
	}

	return [32]byte{}, rpcerrors.RpcErrorInvalidParams("directory requires owner or dir_root")
}

// parseEscrowKeylet parses an escrow specifier: string (hex) or { owner, seq }
// Reference: rippled LedgerEntry.cpp parseEscrow()
func parseEscrowKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	obj, ok := asJSONObject(raw)
	if !ok {
		return parseSelectorHexOrObjectID(raw, "escrow")
	}
	owner, rpcErr := requiredAccountIDField(obj, "owner", "malformedOwner")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	seq, rpcErr := requiredUInt32Field(obj, "seq", "malformedSeq")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	return keylet.Escrow(owner, seq).Key, nil
}

// parseLoanKeylet parses a loan specifier: a hex object index or
// { loan_broker_id, loan_seq }, mirroring rippled LedgerEntry.cpp parseLoan().
func parseLoanKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	obj, ok := asJSONObject(raw)
	if !ok {
		return parseSelectorHexID(raw, "loan")
	}
	brokerID, rpcErr := requiredHash256Field(obj, "loan_broker_id", "malformedBroker")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	loanSeq, rpcErr := requiredUInt32Field(obj, "loan_seq", "malformedSeq")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	return keylet.Loan(brokerID, loanSeq).Key, nil
}

// parseLoanBrokerKeylet parses a loan_broker specifier: a hex object index or
// { owner, seq }, mirroring rippled LedgerEntry.cpp parseLoanBroker().
func parseLoanBrokerKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	obj, ok := asJSONObject(raw)
	if !ok {
		return parseSelectorHexID(raw, "loan_broker")
	}
	owner, rpcErr := requiredAccountIDField(obj, "owner", "malformedOwner")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	seq, rpcErr := requiredUInt32Field(obj, "seq", "malformedSeq")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	return keylet.LoanBroker(owner, seq).Key, nil
}

// asJSONObject reports whether raw is a JSON object and, if so, unmarshals it.
// It mirrors rippled's params.isObject() branch: only objects take the field
// path; strings/arrays/numbers/null fall through to the hex-index form.
func asJSONObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	return obj, true
}

// parseSelectorHexID resolves a non-object selector as a 64-char hex object
// index, mirroring rippled's parseObjectID: an unparseable value yields the
// "malformedRequest" token with an expected-hex-string message.
func parseSelectorHexID(raw json.RawMessage, field string) ([32]byte, *rpcerrors.RpcError) {
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}
	return [32]byte{}, rpcerrors.RpcErrorMalformedField("malformedRequest", field, "hex string")
}

func parseSelectorHexOrObjectID(raw json.RawMessage, field string) ([32]byte, *rpcerrors.RpcError) {
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}
	return [32]byte{}, rpcerrors.RpcErrorMalformedField("malformedRequest", field, "hex string or object")
}

// isJSONFieldAbsent reports whether a required subfield is missing or explicitly
// null, which rippled treats identically.
func isJSONFieldAbsent(obj map[string]json.RawMessage, field string) bool {
	raw, ok := obj[field]
	return !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// requiredHash256Field mirrors rippled requiredUInt256: a required 64-char hex
// field. Absent → malformedRequest; present but unparseable → field token.
func requiredHash256Field(obj map[string]json.RawMessage, field, token string) ([32]byte, *rpcerrors.RpcError) {
	if isJSONFieldAbsent(obj, field) {
		return [32]byte{}, rpcerrors.RpcErrorMalformedRequestMissingField(field)
	}
	if key, ok := tryParseHex256(obj[field]); ok {
		return key, nil
	}
	return [32]byte{}, rpcerrors.RpcErrorMalformedField(token, field, "Hash256")
}

// requiredAccountIDField mirrors rippled requiredAccountID: a required, non-zero
// base58 account. Absent → malformedRequest; present but unparseable → token.
func requiredAccountIDField(obj map[string]json.RawMessage, field, token string) ([20]byte, *rpcerrors.RpcError) {
	if isJSONFieldAbsent(obj, field) {
		return [20]byte{}, rpcerrors.RpcErrorMalformedRequestMissingField(field)
	}
	if id, ok := parseAccountIDRaw(obj[field]); ok {
		return id, nil
	}
	return [20]byte{}, rpcerrors.RpcErrorMalformedField(token, field, "AccountID")
}

func parseAccountIDRaw(raw json.RawMessage) ([20]byte, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return [20]byte{}, false
	}
	account, err := decodeAccountID(value)
	return account, err == nil && account != ([20]byte{})
}

// requiredUInt32Field mirrors rippled requiredUInt32: a required uint32 accepted
// as a non-negative JSON integer that fits 32 bits, or a numeric string.
func requiredUInt32Field(obj map[string]json.RawMessage, field, token string) (uint32, *rpcerrors.RpcError) {
	if isJSONFieldAbsent(obj, field) {
		return 0, rpcerrors.RpcErrorMalformedRequestMissingField(field)
	}
	if v, ok := parseUInt32(obj[field]); ok {
		return v, nil
	}
	return 0, rpcerrors.RpcErrorMalformedField(token, field, "number")
}

// parseUInt32 accepts a JSON number (non-negative, fits uint32) or a numeric
// string, rejecting fractional, negative, out-of-range, and non-numeric values.
func parseUInt32(raw json.RawMessage) (uint32, bool) {
	if string(bytes.TrimSpace(raw)) == "-0" {
		return 0, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		n, err := strconv.ParseUint(s, 10, 32)
		return uint32(n), err == nil
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		n, err := strconv.ParseUint(num.String(), 10, 32)
		return uint32(n), err == nil
	}
	return 0, false
}

// parseMPTokenKeylet parses an mptoken specifier: string (hex) or { mpt_issuance_id, account }
// Reference: rippled LedgerEntry.cpp parseMPToken()
func parseMPTokenKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		MPTIssuanceID string `json:"mpt_issuance_id"`
		Account       string `json:"account"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid mptoken params")
	}
	idBytes, err := hex.DecodeString(req.MPTIssuanceID)
	if err != nil || len(idBytes) != 24 {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams("Invalid mpt_issuance_id")
	}
	var mptID [24]byte
	copy(mptID[:], idBytes)
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, rpcerrors.RpcErrorInvalidParams(fmt.Sprintf("Invalid mptoken account: %v", err))
	}
	return keylet.MPTokenByID(mptID, accountID).Key, nil
}

// parseOfferKeylet parses an offer specifier: string (hex) or { account, seq }
// Reference: rippled LedgerEntry.cpp parseOffer()
func parseOfferKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	obj, ok := asJSONObject(raw)
	if !ok {
		return parseSelectorHexOrObjectID(raw, "offer")
	}
	account, rpcErr := requiredAccountIDField(obj, "account", "malformedAddress")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	seq, rpcErr := requiredUInt32Field(obj, "seq", "malformedRequest")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	return keylet.Offer(account, seq).Key, nil
}

// parseOracleKeylet parses an oracle specifier: string (hex) or { account, oracle_document_id }
// Reference: rippled LedgerEntry.cpp parseOracle()
func parseOracleKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	obj, ok := asJSONObject(raw)
	if !ok {
		return parseSelectorHexOrObjectID(raw, "oracle")
	}
	account, rpcErr := requiredAccountIDField(obj, "account", "malformedAccount")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	documentID, rpcErr := requiredUInt32Field(obj, "oracle_document_id", "malformedDocumentID")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	return keylet.Oracle(account, documentID).Key, nil
}

// parsePermissionedDomainKeylet parses a permissioned_domain specifier: string (hex) or { account, seq }
// Reference: rippled LedgerEntry.cpp parsePermissionedDomains()
func parsePermissionedDomainKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	obj, ok := asJSONObject(raw)
	if !ok {
		return parseSelectorHexOrObjectID(raw, "permissioned_domain")
	}
	account, rpcErr := requiredAccountIDField(obj, "account", "malformedAddress")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	seq, rpcErr := requiredUInt32Field(obj, "seq", "malformedRequest")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	return keylet.PermissionedDomain(account, seq).Key, nil
}

// parseRippleStateKeylet parses a ripple_state/state specifier: { accounts, currency }
func parseRippleStateKeylet(raw json.RawMessage, selector string) ([32]byte, *rpcerrors.RpcError) {
	obj, ok := asJSONObject(raw)
	if !ok {
		return parseSelectorHexOrObjectID(raw, selector)
	}
	for _, field := range []string{"currency", "accounts"} {
		if isJSONFieldAbsent(obj, field) {
			return [32]byte{}, rpcerrors.RpcErrorMalformedRequestMissingField(field)
		}
	}

	var accounts []json.RawMessage
	if err := json.Unmarshal(obj["accounts"], &accounts); err != nil || len(accounts) != 2 {
		return [32]byte{}, rpcerrors.RpcErrorMalformedField(
			"malformedRequest", "accounts", "length-2 array of Accounts",
		)
	}
	account1, ok1 := parseAccountIDRaw(accounts[0])
	account2, ok2 := parseAccountIDRaw(accounts[1])
	if !ok1 || !ok2 {
		return [32]byte{}, rpcerrors.RpcErrorMalformedField("malformedAddress", "accounts", "array of Accounts")
	}
	if account1 == account2 {
		return [32]byte{}, rpcerrors.NewRpcError(
			rpcerrors.RpcINVALID_PARAMS, "malformedRequest", "malformedRequest", "Cannot have a trustline to self.",
		)
	}

	var currency string
	if err := json.Unmarshal(obj["currency"], &currency); err != nil ||
		currency == "" || !keylet.IsValidCurrencyCode(currency) {
		return [32]byte{}, rpcerrors.RpcErrorMalformedField("malformedCurrency", "currency", "Currency")
	}
	return keylet.Line(account1, account2, currency).Key, nil
}

// parseTicketKeylet parses a ticket specifier: string (hex) or { account, ticket_seq }
// Reference: rippled LedgerEntry.cpp parseTicket()
func parseTicketKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	obj, ok := asJSONObject(raw)
	if !ok {
		return parseSelectorHexOrObjectID(raw, "ticket")
	}
	account, rpcErr := requiredAccountIDField(obj, "account", "malformedAddress")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	ticketSeq, rpcErr := requiredUInt32Field(obj, "ticket_seq", "malformedRequest")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	return keylet.Ticket(account, ticketSeq).Key, nil
}

// parseVaultKeylet parses a vault specifier: string (hex) or { owner, seq }
// Reference: rippled LedgerEntry.cpp parseVault()
func parseVaultKeylet(raw json.RawMessage) ([32]byte, *rpcerrors.RpcError) {
	obj, ok := asJSONObject(raw)
	if !ok {
		return parseSelectorHexOrObjectID(raw, "vault")
	}
	owner, rpcErr := requiredAccountIDField(obj, "owner", "malformedOwner")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	seq, rpcErr := requiredUInt32Field(obj, "seq", "malformedRequest")
	if rpcErr != nil {
		return [32]byte{}, rpcErr
	}
	return keylet.Vault(owner, seq).Key, nil
}

// parseCurrencyIssuer parses a currency specifier (e.g., {"currency":"USD","issuer":"rXXX"} or {"currency":"XRP"})
func parseCurrencyIssuer(raw json.RawMessage) (currency [20]byte, issuer [20]byte, err error) {
	var req struct {
		Currency string `json:"currency"`
		Issuer   string `json:"issuer,omitempty"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return currency, issuer, err
	}

	// Canonical write-path encoder; matches AMMCreate's keying.
	currency = keylet.CurrencyBytes(req.Currency)

	if req.Issuer != "" {
		issuer, err = decodeAccountID(req.Issuer)
		if err != nil {
			return currency, issuer, err
		}
		// rippled issueFromJson (Issue.cpp) rejects the two reserved AccountIDs —
		// xrpAccount() (ACCOUNT_ZERO) and noAccount() (ACCOUNT_ONE) — as an issuer.
		if issuer == noAccountID || issuer == xrpAccountID {
			return currency, issuer, errors.New("issuer must be a valid account")
		}
	}

	return currency, issuer, nil
}
