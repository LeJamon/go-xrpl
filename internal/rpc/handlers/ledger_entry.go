package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	binarycodecdefs "github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
)

// LedgerEntryMethod handles the ledger_entry RPC method
type LedgerEntryMethod struct{ BaseHandler }

// ledgerEntrySelector binds an object-type request field to the parser that
// turns it into a ledger key and the LedgerEntryType the looked-up object must
// have. expectedType is the canonical type name (e.g. "AccountRoot"); an empty
// string means any type is acceptable (rippled's ltANY, used by `index`).
// Reference: rippled LedgerEntry.cpp ledgerEntryParsers (LedgerEntry.cpp:743).
type ledgerEntrySelector struct {
	field        string
	parse        func(json.RawMessage) ([32]byte, *types.RpcError)
	expectedType string
}

// hexSelector adapts parseHex256 (which needs the field name for its error
// message) to the parser signature used by the selector table.
func hexSelector(field string) func(json.RawMessage) ([32]byte, *types.RpcError) {
	return func(raw json.RawMessage) ([32]byte, *types.RpcError) {
		return parseHex256(raw, field)
	}
}

// ledgerEntrySelectors mirrors rippled's ledgerEntryParsers table
// (LedgerEntry.cpp:743-758) plus the `index`/`account_root`/`ripple_state`
// aliases. Each entry pins the LedgerEntryType so a selector that names a typed
// object rejects a key that resolves to a different type.
func ledgerEntrySelectors() []ledgerEntrySelector {
	return []ledgerEntrySelector{
		{"index", hexSelector("index"), ""},
		{"account_root", parseAccountRootKeylet, "AccountRoot"},
		{"amendments", hexSelector("amendments"), "Amendments"},
		{"amm", parseAMMKeylet, "AMM"},
		{"bridge", hexSelector("bridge"), "Bridge"},
		{"check", hexSelector("check"), "Check"},
		{"credential", parseCredentialKeylet, "Credential"},
		{"delegate", parseDelegateKeylet, "Delegate"},
		{"deposit_preauth", parseDepositPreauthKeylet, "DepositPreauth"},
		{"did", parseDIDKeylet, "DID"},
		{"directory", parseDirectoryKeylet, "DirectoryNode"},
		{"escrow", parseEscrowKeylet, "Escrow"},
		{"fee", hexSelector("fee"), "FeeSettings"},
		{"hashes", hexSelector("hashes"), "LedgerHashes"},
		{"mpt_issuance", parseMPTIssuanceKeylet, "MPTokenIssuance"},
		{"mptoken", parseMPTokenKeylet, "MPToken"},
		{"nft_page", hexSelector("nft_page"), "NFTokenPage"},
		{"nftoken_offer", hexSelector("nftoken_offer"), "NFTokenOffer"},
		{"nunl", hexSelector("nunl"), "NegativeUNL"},
		{"offer", parseOfferKeylet, "Offer"},
		{"oracle", parseOracleKeylet, "Oracle"},
		{"payment_channel", hexSelector("payment_channel"), "PayChannel"},
		{"permissioned_domain", parsePermissionedDomainKeylet, "PermissionedDomain"},
		{"ripple_state", parseRippleStateKeylet, "RippleState"},
		{"state", parseRippleStateKeylet, "RippleState"},
		{"signer_list", parseSignerListKeylet, "SignerList"},
		{"ticket", parseTicketKeylet, "Ticket"},
		{"vault", parseVaultKeylet, "Vault"},
		{"xchain_owned_claim_id", hexSelector("xchain_owned_claim_id"), "XChainOwnedClaimID"},
		{"xchain_owned_create_account_claim_id", hexSelector("xchain_owned_create_account_claim_id"), "XChainOwnedCreateAccountClaimID"},
	}
}

func (m *LedgerEntryMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// We need to parse into a generic map first because the fields are polymorphic
	// (some are strings, some are objects)
	var rawParams map[string]json.RawMessage
	if err := ParseParams(params, &rawParams); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Parse ledger specifier
	ledgerIndex := "validated"
	if li, ok := rawParams["ledger_index"]; ok {
		var liStr string
		if err := json.Unmarshal(li, &liStr); err == nil {
			ledgerIndex = liStr
		} else {
			var liNum uint32
			if err := json.Unmarshal(li, &liNum); err == nil {
				ledgerIndex = strings.TrimSpace(string(li))
			}
		}
	}

	// Parse binary flag
	var binary bool
	if b, ok := rawParams["binary"]; ok {
		json.Unmarshal(b, &binary)
	}

	selectors := ledgerEntrySelectors()

	// rippled rejects requests that name more than one selector before any
	// lookup happens (LedgerEntry.cpp:760-778).
	matches := 0
	for _, s := range selectors {
		if _, ok := rawParams[s.field]; ok {
			matches++
		}
	}
	if matches > 1 {
		return nil, types.RpcErrorInvalidParams("Too many fields provided.")
	}

	var entryKey [32]byte
	var expectedType string
	found := false
	for _, s := range selectors {
		raw, ok := rawParams[s.field]
		if !ok {
			continue
		}
		key, rpcErr := s.parse(raw)
		if rpcErr != nil {
			return nil, rpcErr
		}
		entryKey = key
		expectedType = s.expectedType
		found = true
		break
	}

	if !found {
		// rippled LedgerEntry.cpp:814-822: apiVersion >= 2 returns invalidParams
		// with a message; earlier versions emit a bare "unknownOption" token.
		if ctx.ApiVersion >= types.ApiVersion2 {
			return nil, types.RpcErrorInvalidParams("No ledger_entry params provided.")
		}
		return nil, types.RpcErrorUnknownOption("")
	}

	result, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, entryKey, ledgerIndex)
	if err != nil {
		if errors.Is(err, svcerr.ErrLedgerEntryNotFound) {
			return nil, types.RpcErrorEntryNotFound("")
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get ledger entry: %v", err))
	}

	// rippled LedgerEntry.cpp:853-856: a selector that names a concrete type
	// rejects an object of a different type.
	if expectedType != "" {
		if actual, ok := ledgerEntryTypeName(result.Node); ok && actual != expectedType {
			return nil, types.RpcErrorUnexpectedLedgerType()
		}
	}

	response := map[string]interface{}{
		"index":        result.Index,
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	if binary {
		// rippled emits the hex of the serialized object (LedgerEntry.cpp:864).
		nodeBinary := result.NodeBinary
		if nodeBinary == "" {
			nodeBinary = strings.ToUpper(hex.EncodeToString(result.Node))
		}
		response["node_binary"] = nodeBinary
	} else {
		// Decode to JSON
		hexData := hex.EncodeToString(result.Node)
		decoded, err := binarycodec.Decode(hexData)
		if err != nil {
			// Fallback to hex string
			response["node"] = strings.ToUpper(hexData)
		} else {
			decoded["index"] = strings.ToUpper(result.Index)
			response["node"] = decoded
		}
	}

	return response, nil
}

// ledgerEntryTypeName extracts the LedgerEntryType name from a serialized SLE.
// sfLedgerEntryType (UInt16, field 1) is canonically the first serialized field,
// encoded as the one-byte header 0x11 followed by the big-endian type code
// (e.g. 0x11 0x00 0x61 -> AccountRoot). Returns false when the buffer is not a
// recognisable serialized entry, in which case the caller skips the type check.
func ledgerEntryTypeName(node []byte) (string, bool) {
	if len(node) < 3 || node[0] != 0x11 {
		return "", false
	}
	code := int32(node[1])<<8 | int32(node[2])
	name, err := binarycodecdefs.Get().GetLedgerEntryTypeNameByLedgerEntryTypeCode(code)
	if err != nil {
		return "", false
	}
	return name, true
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

// parseHex256 parses a JSON value as a 64-character hex string (32 bytes)
func parseHex256(raw json.RawMessage, fieldName string) ([32]byte, *types.RpcError) {
	var result [32]byte
	var hexStr string
	if err := json.Unmarshal(raw, &hexStr); err != nil {
		return result, types.RpcErrorInvalidParams("Invalid " + fieldName + ": must be hex string")
	}
	decoded, err := hex.DecodeString(hexStr)
	if err != nil || len(decoded) != 32 {
		return result, types.RpcErrorInvalidParams("Invalid " + fieldName + ": must be 64-character hex string")
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

// parseAccountRootKeylet parses an account_root specifier: an account address
// only (rippled parseAccountRoot uses parse<AccountID>, no hex fallback).
func parseAccountRootKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
	var addr string
	if err := json.Unmarshal(raw, &addr); err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid account_root")
	}
	accountID, err := decodeAccountID(addr)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid account_root address: %v", err))
	}
	return keylet.Account(accountID).Key, nil
}

// parseDIDKeylet parses a did specifier: an account address only (rippled
// parseDID uses parse<AccountID>, no hex fallback).
func parseDIDKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
	var addr string
	if err := json.Unmarshal(raw, &addr); err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid did")
	}
	accountID, err := decodeAccountID(addr)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid did address: %v", err))
	}
	return keylet.DID(accountID).Key, nil
}

// parseSignerListKeylet parses a signer_list specifier: an account address only
// (rippled only accepts an address, no hex fallback).
func parseSignerListKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
	var addr string
	if err := json.Unmarshal(raw, &addr); err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid signer_list")
	}
	accountID, err := decodeAccountID(addr)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid signer_list address: %v", err))
	}
	return keylet.SignerList(accountID).Key, nil
}

// parseMPTIssuanceKeylet parses an mpt_issuance specifier: a hex issuance ID
// (24 bytes / 48 chars). rippled only accepts the string form.
func parseMPTIssuanceKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
	var idHex string
	if err := json.Unmarshal(raw, &idHex); err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid mpt_issuance")
	}
	decoded, err := hex.DecodeString(idHex)
	if err != nil || len(decoded) != 24 {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid mpt_issuance: must be 48-character hex string (24 bytes)")
	}
	var mptID [24]byte
	copy(mptID[:], decoded)
	return keylet.MPTIssuance(mptID).Key, nil
}

// parseAMMKeylet parses an AMM specifier: string (hex) or { asset, asset2 }
// Reference: rippled LedgerEntry.cpp parseAMM()
func parseAMMKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid amm params")
	}

	if req.Asset == nil || req.Asset2 == nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid amm params: asset and asset2 required")
	}

	issue1Currency, issue1Issuer, err := parseCurrencyIssuer(req.Asset)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid amm asset: %v", err))
	}
	issue2Currency, issue2Issuer, err := parseCurrencyIssuer(req.Asset2)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid amm asset2: %v", err))
	}

	return keylet.AMM(issue1Issuer, issue1Currency, issue2Issuer, issue2Currency).Key, nil
}

// parseCredentialKeylet parses a credential specifier: string (hex) or { subject, issuer, credential_type }
// Reference: rippled LedgerEntry.cpp parseCredential()
func parseCredentialKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid credential params")
	}
	subjectID, err := decodeAccountID(req.Subject)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid credential subject: %v", err))
	}
	issuerID, err := decodeAccountID(req.Issuer)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid credential issuer: %v", err))
	}
	credType, err := hex.DecodeString(req.CredentialType)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid credential_type: must be hex string")
	}
	return keylet.Credential(subjectID, issuerID, credType).Key, nil
}

// parseDelegateKeylet parses a delegate specifier: string (hex) or { account, authorize }
// Reference: rippled LedgerEntry.cpp parseDelegate()
func parseDelegateKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid delegate params")
	}
	if req.Account == "" || req.Authorize == "" {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid delegate params: account and authorize required")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid delegate account: %v", err))
	}
	authorizeID, err := decodeAccountID(req.Authorize)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid delegate authorize: %v", err))
	}
	return keylet.DelegateKeylet(accountID, authorizeID).Key, nil
}

// parseDepositPreauthKeylet parses a deposit_preauth specifier:
// string (hex) or { owner, authorized } or { owner, authorized_credentials }
// Reference: rippled LedgerEntry.cpp parseDepositPreauth()
func parseDepositPreauthKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid deposit_preauth params")
	}
	ownerID, err := decodeAccountID(req.Owner)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid deposit_preauth owner: %v", err))
	}
	authID, err := decodeAccountID(req.Authorized)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid deposit_preauth authorized: %v", err))
	}
	return keylet.DepositPreauth(ownerID, authID).Key, nil
}

// parseDirectoryKeylet parses a directory specifier: string (hex) or { owner, sub_index }
// Reference: rippled LedgerEntry.cpp parseDirectory()
func parseDirectoryKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
	if raw == nil || string(raw) == "null" {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid directory params")
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid directory params")
	}

	if req.DirRoot != "" {
		if req.Owner != "" {
			// May not specify both dir_root and owner
			return [32]byte{}, types.RpcErrorInvalidParams("Invalid directory: may not specify both dir_root and owner")
		}
		decoded, err := hex.DecodeString(req.DirRoot)
		if err != nil || len(decoded) != 32 {
			return [32]byte{}, types.RpcErrorInvalidParams("Invalid dir_root")
		}
		var rootKey [32]byte
		copy(rootKey[:], decoded)
		return keylet.DirPage(rootKey, req.SubIndex).Key, nil
	}

	if req.Owner != "" {
		accountID, err := decodeAccountID(req.Owner)
		if err != nil {
			return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid directory owner: %v", err))
		}
		ownerDir := keylet.OwnerDir(accountID)
		return keylet.DirPage(ownerDir.Key, req.SubIndex).Key, nil
	}

	return [32]byte{}, types.RpcErrorInvalidParams("directory requires owner or dir_root")
}

// parseEscrowKeylet parses an escrow specifier: string (hex) or { owner, seq }
// Reference: rippled LedgerEntry.cpp parseEscrow()
func parseEscrowKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid escrow params")
	}
	accountID, err := decodeAccountID(req.Owner)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid escrow owner: %v", err))
	}
	return keylet.Escrow(accountID, req.Seq).Key, nil
}

// parseMPTokenKeylet parses an mptoken specifier: string (hex) or { mpt_issuance_id, account }
// Reference: rippled LedgerEntry.cpp parseMPToken()
func parseMPTokenKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid mptoken params")
	}
	idBytes, err := hex.DecodeString(req.MPTIssuanceID)
	if err != nil || len(idBytes) != 24 {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid mpt_issuance_id")
	}
	var mptID [24]byte
	copy(mptID[:], idBytes)
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid mptoken account: %v", err))
	}
	return keylet.MPTokenByID(mptID, accountID).Key, nil
}

// parseOfferKeylet parses an offer specifier: string (hex) or { account, seq }
// Reference: rippled LedgerEntry.cpp parseOffer()
func parseOfferKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid offer params")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid offer account: %v", err))
	}
	return keylet.Offer(accountID, req.Seq).Key, nil
}

// parseOracleKeylet parses an oracle specifier: string (hex) or { account, oracle_document_id }
// Reference: rippled LedgerEntry.cpp parseOracle()
func parseOracleKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid oracle params")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid oracle account: %v", err))
	}
	return keylet.Oracle(accountID, req.OracleDocumentID).Key, nil
}

// parsePermissionedDomainKeylet parses a permissioned_domain specifier: string (hex) or { account, seq }
// Reference: rippled LedgerEntry.cpp parsePermissionedDomains()
func parsePermissionedDomainKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid permissioned_domain params")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid permissioned_domain account: %v", err))
	}
	return keylet.PermissionedDomain(accountID, req.Seq).Key, nil
}

// parseRippleStateKeylet parses a ripple_state/state specifier: { accounts, currency }
func parseRippleStateKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
	var req struct {
		Accounts []string `json:"accounts"`
		Currency string   `json:"currency"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid ripple_state params")
	}
	if len(req.Accounts) != 2 {
		return [32]byte{}, types.RpcErrorInvalidParams("ripple_state requires exactly 2 accounts")
	}
	account1, err := decodeAccountID(req.Accounts[0])
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid ripple_state account[0]: %v", err))
	}
	account2, err := decodeAccountID(req.Accounts[1])
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid ripple_state account[1]: %v", err))
	}
	return keylet.Line(account1, account2, req.Currency).Key, nil
}

// parseTicketKeylet parses a ticket specifier: string (hex) or { account, ticket_seq }
// Reference: rippled LedgerEntry.cpp parseTicket()
func parseTicketKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid ticket params")
	}
	accountID, err := decodeAccountID(req.Account)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid ticket account: %v", err))
	}
	return keylet.Ticket(accountID, req.TicketSeq).Key, nil
}

// parseVaultKeylet parses a vault specifier: string (hex) or { owner, seq }
// Reference: rippled LedgerEntry.cpp parseVault()
func parseVaultKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid vault params")
	}
	accountID, err := decodeAccountID(req.Owner)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid vault owner: %v", err))
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
	currency = state.GetCurrencyBytes(req.Currency)

	if req.Issuer != "" {
		issuer, err = decodeAccountID(req.Issuer)
		if err != nil {
			return currency, issuer, err
		}
	}

	return currency, issuer, nil
}
