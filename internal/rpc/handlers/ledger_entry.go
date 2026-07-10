package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
)

// LedgerEntryMethod handles the ledger_entry RPC method
type LedgerEntryMethod struct{ BaseHandler }

func (m *LedgerEntryMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	// We need to parse into a generic map first because the fields are polymorphic
	// (some are strings, some are objects)
	var rawParams map[string]json.RawMessage
	if err := ParseParams(params, &rawParams); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// ledger_hash takes precedence over ledger_index, matching rippled's
	// RPC::ledgerFromRequest. A JSON null in either field is treated as absent
	// (rippled's isNull() / isConvertibleTo checks), a present non-string
	// ledger_hash is ledgerHashNotString, and an unparseable ledger_index is
	// ledgerIndexMalformed.
	ledgerIndex := "validated"
	if lh, ok := rawParams["ledger_hash"]; ok && !isJSONNull(lh) {
		var lhStr string
		if err := json.Unmarshal(lh, &lhStr); err != nil {
			return nil, types.RPCErrorInvalidParams("ledgerHashNotString")
		}
		if raw, err := hex.DecodeString(lhStr); err != nil || len(raw) != 32 {
			return nil, types.RPCErrorInvalidParams("ledgerHashMalformed")
		}
		ledgerIndex = lhStr
	} else if li, ok := rawParams["ledger_index"]; ok && !isJSONNull(li) {
		tok := strings.TrimSpace(string(li))
		if len(tok) > 0 && tok[0] == '"' {
			var liStr string
			if err := json.Unmarshal(li, &liStr); err != nil {
				return nil, types.RPCErrorInvalidParams("ledgerIndexMalformed")
			}
			ledgerIndex = liStr
		} else {
			// A numeric ledger_index must be an integral, in-range uint32;
			// objects/arrays/booleans/non-integral doubles are malformed.
			var liNum uint32
			if err := json.Unmarshal(li, &liNum); err != nil {
				return nil, types.RPCErrorInvalidParams("ledgerIndexMalformed")
			}
			ledgerIndex = tok
		}
	}

	// Parse binary flag
	var binary bool
	if b, ok := rawParams["binary"]; ok {
		json.Unmarshal(b, &binary)
	}

	// Determine the entry key from the various object type specifiers
	var entryKey [32]byte
	var keySet bool
	var rpcErr *types.RPCError

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
				return nil, types.RPCErrorInvalidParams("Invalid " + field)
			}
			accountID, err := decodeAccountID(addr)
			if err != nil {
				return nil, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid %s address: %v", field, err))
			}
			entryKey = keylet.Account(accountID).Key
			keySet = true
			break
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

	// bridge: hex string only (object form requires xchain bridge keylet not yet implemented)
	if !keySet {
		if raw, ok := rawParams["bridge"]; ok {
			entryKey, rpcErr = parseHex256(raw, "bridge")
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
				return nil, types.RPCErrorInvalidParams("Invalid did")
			}
			accountID, err := decodeAccountID(addr)
			if err != nil {
				return nil, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid did address: %v", err))
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
				return nil, types.RPCErrorInvalidParams("Invalid mpt_issuance")
			}
			decoded, err := hex.DecodeString(idHex)
			if err != nil || len(decoded) != 24 {
				return nil, types.RPCErrorInvalidParams("Invalid mpt_issuance: must be 48-character hex string (24 bytes)")
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

	// nft_offer: string (hex ID) — rippled's canonical selector key
	// (ledger_entries.macro rpcName). nftoken_offer is a go-xrpl alias.
	if !keySet {
		if raw, ok := rawParams["nft_offer"]; ok {
			entryKey, rpcErr = parseHex256(raw, "nft_offer")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// nftoken_offer: string (hex ID) — go-xrpl alias for nft_offer
	if !keySet {
		if raw, ok := rawParams["nftoken_offer"]; ok {
			entryKey, rpcErr = parseHex256(raw, "nftoken_offer")
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
			entryKey, rpcErr = parseRippleStateKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// state: alias for ripple_state
	if !keySet {
		if raw, ok := rawParams["state"]; ok {
			entryKey, rpcErr = parseRippleStateKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// signer_list: string (account address) — rippled only accepts address, no hex fallback
	if !keySet {
		if raw, ok := rawParams["signer_list"]; ok {
			var addr string
			if err := json.Unmarshal(raw, &addr); err != nil {
				return nil, types.RPCErrorInvalidParams("Invalid signer_list")
			}
			accountID, err := decodeAccountID(addr)
			if err != nil {
				return nil, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid signer_list address: %v", err))
			}
			entryKey = keylet.SignerList(accountID).Key
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

	// xchain_owned_claim_id: string (hex) — object form requires bridge keylet (not yet implemented)
	if !keySet {
		if raw, ok := rawParams["xchain_owned_claim_id"]; ok {
			entryKey, rpcErr = parseHex256(raw, "xchain_owned_claim_id")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// xchain_owned_create_account_claim_id: string (hex) — object form requires bridge keylet
	if !keySet {
		if raw, ok := rawParams["xchain_owned_create_account_claim_id"]; ok {
			entryKey, rpcErr = parseHex256(raw, "xchain_owned_create_account_claim_id")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	if !keySet {
		if ctx.ApiVersion >= types.ApiVersion2 {
			return nil, types.RPCErrorInvalidParams("")
		}
		return nil, types.RPCErrorUnknownOption("")
	}

	// The type the matched filter selects (rippled's per-filter expectedType).
	// "" means the `index` alias (ltANY), which accepts any entry type.
	expectedType := expectedLedgerEntryType(rawParams)

	// rippled 3.2.0 returns the computed index regardless of whether the object
	// exists (LedgerEntry.cpp: jss::index set before the read, then injectError).
	indexExtra := map[string]any{"index": strings.ToUpper(hex.EncodeToString(entryKey[:]))}

	result, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, entryKey, ledgerIndex)
	if err != nil {
		if errors.Is(err, svcerr.ErrLedgerEntryNotFound) {
			return nil, types.RPCErrorEntryNotFound("").WithExtra(indexExtra)
		}
		if errors.Is(err, svcerr.ErrLedgerNotFound) {
			return nil, types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to get ledger entry: %v", err))
	}

	// Decode the entry once — needed for the type check (in both output modes)
	// and for the JSON node body.
	decoded, decodeErr := decodeLedgerEntryNode(result.Node)

	// unexpectedLedgerType: the entry found at the requested index must match
	// the filter's type (rippled LedgerEntry.cpp:853-856). The `index` alias
	// opts out via expectedType == "".
	if expectedType != "" && decodeErr == nil {
		if actual, _ := decoded["LedgerEntryType"].(string); actual != "" && actual != expectedType {
			return nil, types.RPCErrorUnexpectedLedgerType().WithExtra(indexExtra)
		}
	}

	response := map[string]any{
		"index":        result.Index,
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	if binary {
		response["node_binary"] = result.NodeBinary
	} else if decodeErr == nil {
		decoded["index"] = strings.ToUpper(result.Index)
		response["node"] = decoded
	} else {
		response["node"] = strings.ToUpper(hex.EncodeToString(result.Node))
	}

	return response, nil
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
	{"account_root", "AccountRoot"},
	{"amm", "AMM"},
	{"bridge", "Bridge"},
	{"check", "Check"},
	{"credential", "Credential"},
	{"delegate", "Delegate"},
	{"deposit_preauth", "DepositPreauth"},
	{"did", "DID"},
	{"directory", "DirectoryNode"},
	{"escrow", "Escrow"},
	{"loan", "Loan"},
	{"loan_broker", "LoanBroker"},
	{"mpt_issuance", "MPTokenIssuance"},
	{"mptoken", "MPToken"},
	{"nft_page", "NFTokenPage"},
	{"nft_offer", "NFTokenOffer"},
	{"nftoken_offer", "NFTokenOffer"},
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
func parseHex256(raw json.RawMessage, fieldName string) ([32]byte, *types.RPCError) {
	var result [32]byte
	var hexStr string
	if err := json.Unmarshal(raw, &hexStr); err != nil {
		return result, types.RPCErrorInvalidParams("Invalid " + fieldName + ": must be hex string")
	}
	decoded, err := hex.DecodeString(hexStr)
	if err != nil || len(decoded) != 32 {
		return result, types.RPCErrorInvalidParams("Invalid " + fieldName + ": must be 64-character hex string")
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
func parseAMMKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
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
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid amm params")
	}

	if req.Asset == nil || req.Asset2 == nil {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid amm params: asset and asset2 required")
	}

	issue1Currency, issue1Issuer, err := parseCurrencyIssuer(req.Asset)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid amm asset: %v", err))
	}
	issue2Currency, issue2Issuer, err := parseCurrencyIssuer(req.Asset2)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid amm asset2: %v", err))
	}

	return keylet.AMM(issue1Issuer, issue1Currency, issue2Issuer, issue2Currency).Key, nil
}

// parseCredentialKeylet parses a credential specifier: string (hex) or { subject, issuer, credential_type }
// Reference: rippled LedgerEntry.cpp parseCredential()
func parseCredentialKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
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
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid credential params")
	}
	subjectID, err := decodeAccountID(req.Subject)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid credential subject: %v", err))
	}
	issuerID, err := decodeAccountID(req.Issuer)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid credential issuer: %v", err))
	}
	credType, err := hex.DecodeString(req.CredentialType)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid credential_type: must be hex string")
	}
	return keylet.Credential(subjectID, issuerID, credType).Key, nil
}

// parseDelegateKeylet parses a delegate specifier: string (hex) or { account, authorize }
// Reference: rippled LedgerEntry.cpp parseDelegate()
func parseDelegateKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
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
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid delegate params")
	}
	if req.Account == "" || req.Authorize == "" {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid delegate params: account and authorize required")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid delegate account: %v", err))
	}
	authorizeID, err := decodeAccountID(req.Authorize)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid delegate authorize: %v", err))
	}
	return keylet.Delegate(accountID, authorizeID).Key, nil
}

// parseDepositPreauthKeylet parses a deposit_preauth specifier:
// string (hex) or { owner, authorized } or { owner, authorized_credentials }
// Reference: rippled LedgerEntry.cpp parseDepositPreauth()
func parseDepositPreauthKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		Owner      string `json:"owner"`
		Authorized string `json:"authorized"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid deposit_preauth params")
	}
	ownerID, err := decodeAccountID(req.Owner)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid deposit_preauth owner: %v", err))
	}
	authID, err := decodeAccountID(req.Authorized)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid deposit_preauth authorized: %v", err))
	}
	return keylet.DepositPreauth(ownerID, authID).Key, nil
}

// parseDirectoryKeylet parses a directory specifier: string (hex) or { owner, sub_index }
// Reference: rippled LedgerEntry.cpp parseDirectory()
func parseDirectoryKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
	if raw == nil || string(raw) == "null" {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid directory params")
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
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid directory params")
	}

	if req.DirRoot != "" {
		if req.Owner != "" {
			// May not specify both dir_root and owner
			return [32]byte{}, types.RPCErrorInvalidParams("Invalid directory: may not specify both dir_root and owner")
		}
		decoded, err := hex.DecodeString(req.DirRoot)
		if err != nil || len(decoded) != 32 {
			return [32]byte{}, types.RPCErrorInvalidParams("Invalid dir_root")
		}
		var rootKey [32]byte
		copy(rootKey[:], decoded)
		return keylet.DirPage(rootKey, req.SubIndex).Key, nil
	}

	if req.Owner != "" {
		accountID, err := decodeAccountID(req.Owner)
		if err != nil {
			return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid directory owner: %v", err))
		}
		ownerDir := keylet.OwnerDir(accountID)
		return keylet.DirPage(ownerDir.Key, req.SubIndex).Key, nil
	}

	return [32]byte{}, types.RPCErrorInvalidParams("directory requires owner or dir_root")
}

// parseEscrowKeylet parses an escrow specifier: string (hex) or { owner, seq }
// Reference: rippled LedgerEntry.cpp parseEscrow()
func parseEscrowKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		Owner string `json:"owner"`
		Seq   uint32 `json:"seq"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid escrow params")
	}
	accountID, err := decodeAccountID(req.Owner)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid escrow owner: %v", err))
	}
	return keylet.Escrow(accountID, req.Seq).Key, nil
}

// parseLoanKeylet parses a loan specifier: a hex object index or
// { loan_broker_id, loan_seq }, mirroring rippled LedgerEntry.cpp parseLoan().
func parseLoanKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
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
func parseLoanBrokerKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
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
func parseSelectorHexID(raw json.RawMessage, field string) ([32]byte, *types.RPCError) {
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}
	return [32]byte{}, types.RPCErrorMalformedField("malformedRequest", field, "hex string")
}

// isJSONFieldAbsent reports whether a required subfield is missing or explicitly
// null, which rippled treats identically.
func isJSONFieldAbsent(obj map[string]json.RawMessage, field string) bool {
	raw, ok := obj[field]
	return !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// requiredHash256Field mirrors rippled requiredUInt256: a required 64-char hex
// field. Absent → malformedRequest; present but unparseable → field token.
func requiredHash256Field(obj map[string]json.RawMessage, field, token string) ([32]byte, *types.RPCError) {
	if isJSONFieldAbsent(obj, field) {
		return [32]byte{}, types.RPCErrorMalformedRequestMissingField(field)
	}
	if key, ok := tryParseHex256(obj[field]); ok {
		return key, nil
	}
	return [32]byte{}, types.RPCErrorMalformedField(token, field, "Hash256")
}

// requiredAccountIDField mirrors rippled requiredAccountID: a required, non-zero
// base58 account. Absent → malformedRequest; present but unparseable → token.
func requiredAccountIDField(obj map[string]json.RawMessage, field, token string) ([20]byte, *types.RPCError) {
	if isJSONFieldAbsent(obj, field) {
		return [20]byte{}, types.RPCErrorMalformedRequestMissingField(field)
	}
	var s string
	if err := json.Unmarshal(obj[field], &s); err == nil {
		if id, err := decodeAccountID(s); err == nil && id != [20]byte{} {
			return id, nil
		}
	}
	return [20]byte{}, types.RPCErrorMalformedField(token, field, "AccountID")
}

// requiredUInt32Field mirrors rippled requiredUInt32: a required uint32 accepted
// as a non-negative JSON integer that fits 32 bits, or a numeric string.
func requiredUInt32Field(obj map[string]json.RawMessage, field, token string) (uint32, *types.RPCError) {
	if isJSONFieldAbsent(obj, field) {
		return 0, types.RPCErrorMalformedRequestMissingField(field)
	}
	if v, ok := parseUInt32(obj[field]); ok {
		return v, nil
	}
	return 0, types.RPCErrorMalformedField(token, field, "number")
}

// parseUInt32 accepts a JSON number (non-negative, fits uint32) or a numeric
// string, rejecting fractional, negative, out-of-range, and non-numeric values.
func parseUInt32(raw json.RawMessage) (uint32, bool) {
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
func parseMPTokenKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
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
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid mptoken params")
	}
	idBytes, err := hex.DecodeString(req.MPTIssuanceID)
	if err != nil || len(idBytes) != 24 {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid mpt_issuance_id")
	}
	var mptID [24]byte
	copy(mptID[:], idBytes)
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid mptoken account: %v", err))
	}
	return keylet.MPTokenByID(mptID, accountID).Key, nil
}

// parseOfferKeylet parses an offer specifier: string (hex) or { account, seq }
// Reference: rippled LedgerEntry.cpp parseOffer()
func parseOfferKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		Account string `json:"account"`
		Seq     uint32 `json:"seq"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid offer params")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid offer account: %v", err))
	}
	return keylet.Offer(accountID, req.Seq).Key, nil
}

// parseOracleKeylet parses an oracle specifier: string (hex) or { account, oracle_document_id }
// Reference: rippled LedgerEntry.cpp parseOracle()
func parseOracleKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		Account          string `json:"account"`
		OracleDocumentID uint32 `json:"oracle_document_id"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid oracle params")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid oracle account: %v", err))
	}
	return keylet.Oracle(accountID, req.OracleDocumentID).Key, nil
}

// parsePermissionedDomainKeylet parses a permissioned_domain specifier: string (hex) or { account, seq }
// Reference: rippled LedgerEntry.cpp parsePermissionedDomains()
func parsePermissionedDomainKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		Account string `json:"account"`
		Seq     uint32 `json:"seq"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid permissioned_domain params")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid permissioned_domain account: %v", err))
	}
	return keylet.PermissionedDomain(accountID, req.Seq).Key, nil
}

// parseRippleStateKeylet parses a ripple_state/state specifier: { accounts, currency }
func parseRippleStateKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
	var req struct {
		Accounts []string `json:"accounts"`
		Currency string   `json:"currency"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid ripple_state params")
	}
	if len(req.Accounts) != 2 {
		return [32]byte{}, types.RPCErrorInvalidParams("ripple_state requires exactly 2 accounts")
	}
	account1, err := decodeAccountID(req.Accounts[0])
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid ripple_state account[0]: %v", err))
	}
	account2, err := decodeAccountID(req.Accounts[1])
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid ripple_state account[1]: %v", err))
	}
	return keylet.Line(account1, account2, req.Currency).Key, nil
}

// parseTicketKeylet parses a ticket specifier: string (hex) or { account, ticket_seq }
// Reference: rippled LedgerEntry.cpp parseTicket()
func parseTicketKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		Account   string `json:"account"`
		TicketSeq uint32 `json:"ticket_seq"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid ticket params")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid ticket account: %v", err))
	}
	return keylet.Ticket(accountID, req.TicketSeq).Key, nil
}

// parseVaultKeylet parses a vault specifier: string (hex) or { owner, seq }
// Reference: rippled LedgerEntry.cpp parseVault()
func parseVaultKeylet(raw json.RawMessage) ([32]byte, *types.RPCError) {
	// Try hex string first
	if key, ok := tryParseHex256(raw); ok {
		return key, nil
	}

	// Try object form
	var req struct {
		Owner string `json:"owner"`
		Seq   uint32 `json:"seq"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams("Invalid vault params")
	}
	accountID, err := decodeAccountID(req.Owner)
	if err != nil {
		return [32]byte{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid vault owner: %v", err))
	}
	return keylet.Vault(accountID, req.Seq).Key, nil
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
