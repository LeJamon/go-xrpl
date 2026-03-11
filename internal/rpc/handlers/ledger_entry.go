package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/keylet"
)

// LedgerEntryMethod handles the ledger_entry RPC method
type LedgerEntryMethod struct{}

func (m *LedgerEntryMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	// We need to parse into a generic map first because the fields are polymorphic
	// (some are strings, some are objects)
	var rawParams map[string]json.RawMessage
	if params != nil {
		if err := json.Unmarshal(params, &rawParams); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
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

	// Determine the entry key from the various object type specifiers
	var entryKey [32]byte
	var keySet bool
	var rpcErr *types.RpcError

	// Direct index lookup
	if !keySet {
		if raw, ok := rawParams["index"]; ok {
			entryKey, rpcErr = parseHex256(raw, "index")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// account_root: string (account address)
	if !keySet {
		if raw, ok := rawParams["account_root"]; ok {
			var addr string
			if err := json.Unmarshal(raw, &addr); err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid account_root")
			}
			accountID, err := decodeAccountID(addr)
			if err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid account_root address: " + err.Error())
			}
			entryKey = keylet.Account(accountID).Key
			keySet = true
		}
	}

	// amm: { asset, asset2 }
	if !keySet {
		if raw, ok := rawParams["amm"]; ok {
			entryKey, rpcErr = parseAMMKeylet(raw)
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

	// credential: { subject, issuer, credential_type }
	if !keySet {
		if raw, ok := rawParams["credential"]; ok {
			entryKey, rpcErr = parseCredentialKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// deposit_preauth: { owner, authorized } or { owner, authorized_credentials }
	if !keySet {
		if raw, ok := rawParams["deposit_preauth"]; ok {
			entryKey, rpcErr = parseDepositPreauthKeylet(raw)
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// did: string (account address)
	if !keySet {
		if raw, ok := rawParams["did"]; ok {
			var addr string
			if err := json.Unmarshal(raw, &addr); err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid did")
			}
			accountID, err := decodeAccountID(addr)
			if err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid did address: " + err.Error())
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

	// escrow: { owner, seq }
	if !keySet {
		if raw, ok := rawParams["escrow"]; ok {
			var req struct {
				Owner string `json:"owner"`
				Seq   uint32 `json:"seq"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid escrow params")
			}
			accountID, err := decodeAccountID(req.Owner)
			if err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid escrow owner: " + err.Error())
			}
			entryKey = keylet.Escrow(accountID, req.Seq).Key
			keySet = true
		}
	}

	// mpt_issuance: string (hex issuance ID, 24 bytes / 48 chars)
	if !keySet {
		if raw, ok := rawParams["mpt_issuance"]; ok {
			var idHex string
			if err := json.Unmarshal(raw, &idHex); err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid mpt_issuance")
			}
			decoded, err := hex.DecodeString(idHex)
			if err != nil || len(decoded) != 24 {
				return nil, types.RpcErrorInvalidParams("Invalid mpt_issuance: must be 48-character hex string (24 bytes)")
			}
			var mptID [24]byte
			copy(mptID[:], decoded)
			entryKey = keylet.MPTIssuance(mptID).Key
			keySet = true
		}
	}

	// mptoken: { mpt_issuance_id, account }
	if !keySet {
		if raw, ok := rawParams["mptoken"]; ok {
			var req struct {
				MPTIssuanceID string `json:"mpt_issuance_id"`
				Account       string `json:"account"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid mptoken params")
			}
			idBytes, err := hex.DecodeString(req.MPTIssuanceID)
			if err != nil || len(idBytes) != 24 {
				return nil, types.RpcErrorInvalidParams("Invalid mpt_issuance_id")
			}
			var mptID [24]byte
			copy(mptID[:], idBytes)
			accountID, err := decodeAccountID(req.Account)
			if err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid mptoken account: " + err.Error())
			}
			entryKey = keylet.MPTokenByID(mptID, accountID).Key
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

	// nftoken_offer: string (hex ID)
	if !keySet {
		if raw, ok := rawParams["nftoken_offer"]; ok {
			entryKey, rpcErr = parseHex256(raw, "nftoken_offer")
			if rpcErr != nil {
				return nil, rpcErr
			}
			keySet = true
		}
	}

	// offer: { account, seq }
	if !keySet {
		if raw, ok := rawParams["offer"]; ok {
			var req struct {
				Account string `json:"account"`
				Seq     uint32 `json:"seq"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid offer params")
			}
			accountID, err := decodeAccountID(req.Account)
			if err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid offer account: " + err.Error())
			}
			entryKey = keylet.Offer(accountID, req.Seq).Key
			keySet = true
		}
	}

	// oracle: { account, oracle_document_id }
	if !keySet {
		if raw, ok := rawParams["oracle"]; ok {
			var req struct {
				Account          string `json:"account"`
				OracleDocumentID uint32 `json:"oracle_document_id"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid oracle params")
			}
			accountID, err := decodeAccountID(req.Account)
			if err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid oracle account: " + err.Error())
			}
			entryKey = keylet.Oracle(accountID, req.OracleDocumentID).Key
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

	// permissioned_domain: { account, seq }
	if !keySet {
		if raw, ok := rawParams["permissioned_domain"]; ok {
			var req struct {
				Account string `json:"account"`
				Seq     uint32 `json:"seq"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid permissioned_domain params")
			}
			accountID, err := decodeAccountID(req.Account)
			if err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid permissioned_domain account: " + err.Error())
			}
			entryKey = keylet.PermissionedDomain(accountID, req.Seq).Key
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

	// signer_list: string (account address)
	if !keySet {
		if raw, ok := rawParams["signer_list"]; ok {
			var addr string
			if err := json.Unmarshal(raw, &addr); err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid signer_list")
			}
			accountID, err := decodeAccountID(addr)
			if err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid signer_list address: " + err.Error())
			}
			entryKey = keylet.SignerList(accountID).Key
			keySet = true
		}
	}

	// ticket: { account, ticket_seq }
	if !keySet {
		if raw, ok := rawParams["ticket"]; ok {
			var req struct {
				Account  string `json:"account"`
				TicketID uint32 `json:"ticket_id"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid ticket params")
			}
			accountID, err := decodeAccountID(req.Account)
			if err != nil {
				return nil, types.RpcErrorInvalidParams("Invalid ticket account: " + err.Error())
			}
			entryKey = keylet.Ticket(accountID, req.TicketID).Key
			keySet = true
		}
	}

	if !keySet {
		return nil, types.RpcErrorInvalidParams("Must specify object to look up")
	}

	// Get ledger entry
	result, err := types.Services.Ledger.GetLedgerEntry(entryKey, ledgerIndex)
	if err != nil {
		if err.Error() == "entry not found" {
			return nil, &types.RpcError{
				Code:    21,
				Message: "Requested ledger entry not found.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get ledger entry: " + err.Error())
	}

	response := map[string]interface{}{
		"index":        result.Index,
		"ledger_hash":  hex.EncodeToString(result.LedgerHash[:]),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	if binary {
		response["node_binary"] = result.NodeBinary
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

// parseAMMKeylet parses an AMM specifier: { asset, asset2 }
func parseAMMKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
	var req struct {
		Asset  json.RawMessage `json:"asset"`
		Asset2 json.RawMessage `json:"asset2"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid amm params")
	}

	issue1Currency, issue1Issuer, err := parseCurrencyIssuer(req.Asset)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid amm asset: " + err.Error())
	}
	issue2Currency, issue2Issuer, err := parseCurrencyIssuer(req.Asset2)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid amm asset2: " + err.Error())
	}

	return keylet.AMM(issue1Issuer, issue1Currency, issue2Issuer, issue2Currency).Key, nil
}

// parseCredentialKeylet parses a credential specifier: { subject, issuer, credential_type }
func parseCredentialKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid credential subject: " + err.Error())
	}
	issuerID, err := decodeAccountID(req.Issuer)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid credential issuer: " + err.Error())
	}
	credType, err := hex.DecodeString(req.CredentialType)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid credential_type: must be hex string")
	}
	return keylet.Credential(subjectID, issuerID, credType).Key, nil
}

// parseDepositPreauthKeylet parses a deposit_preauth specifier
func parseDepositPreauthKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
	var req struct {
		Owner      string `json:"owner"`
		Authorized string `json:"authorized"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid deposit_preauth params")
	}
	ownerID, err := decodeAccountID(req.Owner)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid deposit_preauth owner: " + err.Error())
	}
	authID, err := decodeAccountID(req.Authorized)
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid deposit_preauth authorized: " + err.Error())
	}
	return keylet.DepositPreauth(ownerID, authID).Key, nil
}

// parseDirectoryKeylet parses a directory specifier: string (hex) or { owner, sub_index }
func parseDirectoryKeylet(raw json.RawMessage) ([32]byte, *types.RpcError) {
	// Try as string first (direct hex ID)
	var hexStr string
	if err := json.Unmarshal(raw, &hexStr); err == nil {
		decoded, err := hex.DecodeString(hexStr)
		if err != nil || len(decoded) != 32 {
			return [32]byte{}, types.RpcErrorInvalidParams("Invalid directory: must be 64-character hex string")
		}
		var result [32]byte
		copy(result[:], decoded)
		return result, nil
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
			return [32]byte{}, types.RpcErrorInvalidParams("Invalid directory owner: " + err.Error())
		}
		ownerDir := keylet.OwnerDir(accountID)
		return keylet.DirPage(ownerDir.Key, req.SubIndex).Key, nil
	}

	return [32]byte{}, types.RpcErrorInvalidParams("directory requires owner or dir_root")
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
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid ripple_state account[0]: " + err.Error())
	}
	account2, err := decodeAccountID(req.Accounts[1])
	if err != nil {
		return [32]byte{}, types.RpcErrorInvalidParams("Invalid ripple_state account[1]: " + err.Error())
	}
	return keylet.Line(account1, account2, req.Currency).Key, nil
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

	// Convert currency string to 20-byte representation
	// Reuse currencyToBytes from amm_info.go (same package)
	currency = currencyToBytes(req.Currency)

	if req.Issuer != "" {
		issuer, err = decodeAccountID(req.Issuer)
		if err != nil {
			return currency, issuer, err
		}
	}

	return currency, issuer, nil
}

func (m *LedgerEntryMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *LedgerEntryMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *LedgerEntryMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
