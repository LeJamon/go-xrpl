package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AMMInfoMethod handles the amm_info RPC method
type AMMInfoMethod struct{}

func (m *AMMInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.LedgerSpecifier
		Asset      map[string]interface{} `json:"asset,omitempty"`
		Asset2     map[string]interface{} `json:"asset2,omitempty"`
		AMMAccount string                 `json:"amm_account,omitempty"`
		Account    string                 `json:"account,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	hasAssets := request.Asset != nil && request.Asset2 != nil
	hasAMMAccount := request.AMMAccount != ""

	// Validate parameter combinations
	if hasAssets == hasAMMAccount {
		return nil, types.RpcErrorInvalidParams("Must specify either (asset + asset2) or amm_account, but not both or neither")
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Determine ledger index to use
	ledgerIndex := "validated"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	var ammKey [32]byte
	var err error

	if hasAMMAccount {
		// Look up AMM by account
		_, accountID, decErr := addresscodec.DecodeClassicAddressToAccountID(request.AMMAccount)
		if decErr != nil {
			return nil, types.RpcErrorInvalidParams("Invalid amm_account: " + decErr.Error())
		}

		// Get the account to find its AMMID
		var accountIDArray [20]byte
		copy(accountIDArray[:], accountID)
		accountKey := keylet.Account(accountIDArray)

		accountEntry, lookupErr := types.Services.Ledger.GetLedgerEntry(accountKey.Key, ledgerIndex)
		if lookupErr != nil {
			return nil, &types.RpcError{
				Code:    19,
				Message: "AMM account not found",
			}
		}

		// Decode the account to get AMMID
		decoded, decodeErr := binarycodec.Decode(hex.EncodeToString(accountEntry.Node))
		if decodeErr != nil {
			return nil, types.RpcErrorInternal("Failed to decode account: " + decodeErr.Error())
		}

		ammIDHex, ok := decoded["AMMID"].(string)
		if !ok || ammIDHex == "" {
			return nil, &types.RpcError{
				Code:    19,
				Message: "Account is not an AMM account",
			}
		}

		ammIDBytes, hexErr := hex.DecodeString(ammIDHex)
		if hexErr != nil || len(ammIDBytes) != 32 {
			return nil, types.RpcErrorInternal("Invalid AMMID in account")
		}
		copy(ammKey[:], ammIDBytes)
	} else {
		// Look up AMM by asset pair
		issue1Issuer, issue1Currency, err := parseIssue(request.Asset)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid asset: " + err.Error())
		}

		issue2Issuer, issue2Currency, err := parseIssue(request.Asset2)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid asset2: " + err.Error())
		}

		ammKeylet := keylet.AMM(issue1Issuer, issue1Currency, issue2Issuer, issue2Currency)
		ammKey = ammKeylet.Key
	}

	// Get the AMM entry
	ammEntry, err := types.Services.Ledger.GetLedgerEntry(ammKey, ledgerIndex)
	if err != nil {
		return nil, &types.RpcError{
			Code:    19,
			Message: "AMM not found",
		}
	}

	// Decode the AMM entry
	decoded, decodeErr := binarycodec.Decode(hex.EncodeToString(ammEntry.Node))
	if decodeErr != nil {
		return nil, types.RpcErrorInternal("Failed to decode AMM: " + decodeErr.Error())
	}

	// Build the response
	ammResult := make(map[string]interface{})

	// Copy relevant fields
	if account, ok := decoded["Account"].(string); ok {
		ammResult["account"] = account
	}
	if lpToken, ok := decoded["LPTokenBalance"]; ok {
		ammResult["lp_token"] = lpToken
	}
	if tradingFee, ok := decoded["TradingFee"]; ok {
		ammResult["trading_fee"] = tradingFee
	}
	if asset, ok := decoded["Asset"]; ok {
		ammResult["amount"] = asset
	}
	if asset2, ok := decoded["Asset2"]; ok {
		ammResult["amount2"] = asset2
	}

	// Handle vote slots
	if voteSlots, ok := decoded["VoteSlots"].([]interface{}); ok && len(voteSlots) > 0 {
		votes := make([]map[string]interface{}, 0, len(voteSlots))
		for _, vs := range voteSlots {
			if voteEntry, ok := vs.(map[string]interface{}); ok {
				if voteSlot, ok := voteEntry["VoteEntry"].(map[string]interface{}); ok {
					vote := make(map[string]interface{})
					if account, ok := voteSlot["Account"].(string); ok {
						vote["account"] = account
					}
					if tradingFee, ok := voteSlot["TradingFee"]; ok {
						vote["trading_fee"] = tradingFee
					}
					if voteWeight, ok := voteSlot["VoteWeight"]; ok {
						vote["vote_weight"] = voteWeight
					}
					votes = append(votes, vote)
				}
			}
		}
		if len(votes) > 0 {
			ammResult["vote_slots"] = votes
		}
	}

	// Handle auction slot
	if auctionSlot, ok := decoded["AuctionSlot"].(map[string]interface{}); ok {
		auction := make(map[string]interface{})
		if account, ok := auctionSlot["Account"].(string); ok {
			auction["account"] = account
		}
		if price, ok := auctionSlot["Price"]; ok {
			auction["price"] = price
		}
		if discountedFee, ok := auctionSlot["DiscountedFee"]; ok {
			auction["discounted_fee"] = discountedFee
		}
		if expiration, ok := auctionSlot["Expiration"]; ok {
			auction["expiration"] = expiration
		}
		if authAccounts, ok := auctionSlot["AuthAccounts"].([]interface{}); ok {
			auth := make([]map[string]interface{}, 0, len(authAccounts))
			for _, aa := range authAccounts {
				if authAccount, ok := aa.(map[string]interface{}); ok {
					if account, ok := authAccount["Account"].(string); ok {
						auth = append(auth, map[string]interface{}{"account": account})
					}
				}
			}
			if len(auth) > 0 {
				auction["auth_accounts"] = auth
			}
		}
		ammResult["auction_slot"] = auction
	}

	// Build final response
	response := map[string]interface{}{
		"amm":          ammResult,
		"ledger_index": ammEntry.LedgerIndex,
		"validated":    ammEntry.Validated,
	}

	if ammEntry.LedgerHash != [32]byte{} {
		response["ledger_hash"] = hex.EncodeToString(ammEntry.LedgerHash[:])
	}

	return response, nil
}

// parseIssue parses an asset/issue from the JSON representation
// Returns issuer (20 bytes), currency (20 bytes), and error
func parseIssue(issue map[string]interface{}) ([20]byte, [20]byte, error) {
	var issuer [20]byte
	var currency [20]byte

	currencyStr, ok := issue["currency"].(string)
	if !ok {
		return issuer, currency, fmt.Errorf("missing currency field")
	}

	// Handle XRP (native currency)
	if currencyStr == "XRP" {
		// For XRP, issuer is all zeros, currency is all zeros
		return issuer, currency, nil
	}

	// Handle IOU
	issuerStr, ok := issue["issuer"].(string)
	if !ok {
		return issuer, currency, fmt.Errorf("missing issuer field for non-XRP currency")
	}

	_, issuerBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuerStr)
	if err != nil {
		return issuer, currency, fmt.Errorf("invalid issuer: %w", err)
	}
	copy(issuer[:], issuerBytes)

	// Convert currency code to 20-byte format
	currency = currencyToBytes(currencyStr)

	return issuer, currency, nil
}

// currencyToBytes converts a currency code to its 20-byte representation
func currencyToBytes(currency string) [20]byte {
	var result [20]byte

	if len(currency) == 3 {
		// Standard currency code - ASCII in bytes 12-14
		result[12] = currency[0]
		result[13] = currency[1]
		result[14] = currency[2]
	} else if len(currency) == 40 {
		// Hex-encoded currency (non-standard)
		decoded, _ := hex.DecodeString(currency)
		if len(decoded) == 20 {
			copy(result[:], decoded)
		}
	}

	return result
}

func (m *AMMInfoMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *AMMInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *AMMInfoMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
