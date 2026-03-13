package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/keylet"
)

// AMMInfoMethod handles the amm_info RPC method
type AMMInfoMethod struct{ BaseHandler }

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

	// TODO: amount/amount2 currently return the asset issue definitions from the AMM SLE
	// (sfAsset/sfAsset2), not the actual pool balances. Rippled calls ammPoolHolds() to
	// get real balances from trust lines, which requires service-layer trust line lookups.
	// Similarly, asset_frozen/asset2_frozen require isFrozen() calls on trust lines.
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

	// Resolve parentCloseTime from the ledger for auction slot time_interval computation.
	// rippled: ammAuctionTimeSlot(ledger->info().parentCloseTime, auctionSlot)
	var parentCloseTime uint64
	if ammEntry.LedgerIndex > 0 {
		if lr, lrErr := types.Services.Ledger.GetLedgerBySequence(ammEntry.LedgerIndex); lrErr == nil && lr != nil {
			pct := lr.ParentCloseTime()
			if pct > 0 {
				parentCloseTime = uint64(pct)
			}
		}
	}

	// Handle auction slot
	if auctionSlot, ok := decoded["AuctionSlot"].(map[string]interface{}); ok {
		auction := buildAuctionSlot(auctionSlot, parentCloseTime)
		if auction != nil {
			ammResult["auction_slot"] = auction
		}
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

// Auction slot constants matching rippled's AMMCore.h
const (
	totalTimeSlotSecs           = 24 * 3600                                    // 86400 seconds
	auctionSlotTimeIntervals    = 20                                           // number of intervals
	auctionSlotIntervalDuration = totalTimeSlotSecs / auctionSlotTimeIntervals // 4320 seconds
)

// rippleEpochOffset is the number of seconds between Unix epoch (1970-01-01)
// and Ripple epoch (2000-01-01): 946684800 seconds.
const rippleEpochOffset = 946684800

// rippleEpochToISO8601 converts a Ripple epoch timestamp to an ISO 8601 string.
// Matches rippled's to_iso8601() in AMMInfo.cpp.
func rippleEpochToISO8601(rippleSeconds uint32) string {
	unixTime := int64(rippleSeconds) + rippleEpochOffset
	t := time.Unix(unixTime, 0).UTC()
	return t.Format("2006-01-02T15:04:05+0000")
}

// ammAuctionTimeSlot computes the current time interval for the auction slot.
// Returns the interval index (0..19) or auctionSlotTimeIntervals if expired/not started.
// Matches rippled's ammAuctionTimeSlot() in AMMCore.cpp.
func ammAuctionTimeSlot(currentParentCloseTime uint64, expiration uint32) uint32 {
	if expiration >= totalTimeSlotSecs {
		start := uint64(expiration) - totalTimeSlotSecs
		if currentParentCloseTime >= start {
			diff := currentParentCloseTime - start
			if diff < totalTimeSlotSecs {
				return uint32(diff / auctionSlotIntervalDuration)
			}
		}
	}
	return auctionSlotTimeIntervals
}

// buildAuctionSlot constructs the auction_slot response object from decoded AMM SLE fields.
// Only includes the slot if it has an Account (rippled checks isFieldPresent(sfAccount)).
func buildAuctionSlot(auctionSlot map[string]interface{}, parentCloseTime uint64) map[string]interface{} {
	account, ok := auctionSlot["Account"].(string)
	if !ok || account == "" {
		// rippled: only includes auction_slot if auctionSlot.isFieldPresent(sfAccount)
		return nil
	}

	auction := make(map[string]interface{})
	auction["account"] = account

	if price, ok := auctionSlot["Price"]; ok {
		auction["price"] = price
	}
	if discountedFee, ok := auctionSlot["DiscountedFee"]; ok {
		auction["discounted_fee"] = discountedFee
	}

	// Convert expiration from Ripple epoch uint32 to ISO 8601 string.
	// rippled: auction[jss::expiration] = to_iso8601(NetClock::time_point{...})
	var expirationUint32 uint32
	if exp, ok := auctionSlot["Expiration"]; ok {
		expirationUint32 = toUint32(exp)
		auction["expiration"] = rippleEpochToISO8601(expirationUint32)
	}

	// Compute time_interval.
	// rippled: ammAuctionTimeSlot(parentCloseTime, auctionSlot) → interval or AUCTION_SLOT_TIME_INTERVALS
	auction["time_interval"] = ammAuctionTimeSlot(parentCloseTime, expirationUint32)

	// Handle auth_accounts — each element is wrapped in an AuthAccount inner object:
	// decoded: [{"AuthAccount": {"Account": "rXXX"}}, ...]
	// rippled output: [{"account": "rXXX"}, ...]
	if authAccounts, ok := auctionSlot["AuthAccounts"].([]interface{}); ok {
		auth := make([]map[string]interface{}, 0, len(authAccounts))
		for _, aa := range authAccounts {
			if wrapper, ok := aa.(map[string]interface{}); ok {
				// Unwrap the AuthAccount inner object
				inner, ok := wrapper["AuthAccount"].(map[string]interface{})
				if !ok {
					// Fallback: try direct Account field (in case codec doesn't wrap)
					inner = wrapper
				}
				if acct, ok := inner["Account"].(string); ok {
					auth = append(auth, map[string]interface{}{"account": acct})
				}
			}
		}
		if len(auth) > 0 {
			auction["auth_accounts"] = auth
		}
	}

	return auction
}

// toUint32 extracts a uint32 from a JSON-decoded numeric value.
// The binary codec may return float64 or json.Number depending on decode mode.
func toUint32(v interface{}) uint32 {
	switch n := v.(type) {
	case float64:
		if n >= 0 && n <= math.MaxUint32 {
			return uint32(n)
		}
	case json.Number:
		if i, err := n.Int64(); err == nil && i >= 0 && i <= math.MaxUint32 {
			return uint32(i)
		}
	case int:
		if n >= 0 {
			return uint32(n)
		}
	case int64:
		if n >= 0 && n <= math.MaxUint32 {
			return uint32(n)
		}
	case uint32:
		return n
	case uint64:
		if n <= math.MaxUint32 {
			return uint32(n)
		}
	}
	return 0
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
