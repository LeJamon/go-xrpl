package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
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

	if err := RequireLedgerService(ctx.Services); err != nil {
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

		var accountIDArray [20]byte
		copy(accountIDArray[:], accountID)
		accountKey := keylet.Account(accountIDArray)

		accountEntry, lookupErr := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, accountKey.Key, ledgerIndex)
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
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid asset: %v", err))
		}

		issue2Issuer, issue2Currency, err := parseIssue(request.Asset2)
		if err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid asset2: %v", err))
		}

		ammKeylet := keylet.AMM(issue1Issuer, issue1Currency, issue2Issuer, issue2Currency)
		ammKey = ammKeylet.Key
	}

	ammEntry, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, ammKey, ledgerIndex)
	if err != nil {
		return nil, types.RpcErrorActNotFound("AMM not found")
	}

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

	// Asset and Asset2 on the AMM SLE carry only the issue definitions; pool
	// balances must be read from the AMM account's trust lines / XRP balance.
	// Matches rippled ammPoolHolds() in AMMUtils.cpp.
	asset1, asset1Err := parseSLEIssue(decoded["Asset"])
	if asset1Err != nil {
		return nil, types.RpcErrorInternal("AMM SLE Asset: " + asset1Err.Error())
	}
	asset2, asset2Err := parseSLEIssue(decoded["Asset2"])
	if asset2Err != nil {
		return nil, types.RpcErrorInternal("AMM SLE Asset2: " + asset2Err.Error())
	}
	accountStr, _ := decoded["Account"].(string)
	if accountStr == "" {
		return nil, types.RpcErrorInternal("AMM SLE missing Account field")
	}
	_, accBytes, decErr := addresscodec.DecodeClassicAddressToAccountID(accountStr)
	if decErr != nil {
		return nil, types.RpcErrorInternal("AMM SLE has invalid Account: " + decErr.Error())
	}
	var ammAccountID [20]byte
	copy(ammAccountID[:], accBytes)

	// Read balances and freeze flags from the same ledger the AMM SLE was
	// resolved against. Rippled's doAMMInfo passes a single ReadView to both
	// the SLE lookup and ammPoolHolds (AMMInfo.cpp:188); using a different
	// view here would mix a historical SLE with current trust-line state.
	view, viewErr := ctx.Services.Ledger.GetLedgerViewBySequence(ammEntry.LedgerIndex)
	if viewErr != nil {
		return nil, types.RpcErrorInternal("ledger view unavailable: " + viewErr.Error())
	}

	// rippled passes FreezeHandling::fhIGNORE_FREEZE here: balances reflect
	// raw pool holdings; the freeze status is reported separately.
	bal1 := readAMMHolds(view, ammAccountID, asset1)
	bal2 := readAMMHolds(view, ammAccountID, asset2)
	ammResult["amount"] = formatAmountJSON(bal1)
	ammResult["amount2"] = formatAmountJSON(bal2)

	if !asset1.IsXRP {
		ammResult["asset_frozen"] = isAssetFrozen(view, ammAccountID, asset1)
	}
	if !asset2.IsXRP {
		ammResult["asset2_frozen"] = isAssetFrozen(view, ammAccountID, asset2)
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
		if lr, lrErr := ctx.Services.Ledger.GetLedgerBySequence(ammEntry.LedgerIndex); lrErr == nil && lr != nil {
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
		response["ledger_hash"] = FormatLedgerHash(ammEntry.LedgerHash)
	}

	return response, nil
}

// Auction slot constants matching rippled's AMMCore.h
const (
	totalTimeSlotSecs           = 24 * 3600                                    // 86400 seconds
	auctionSlotTimeIntervals    = 20                                           // number of intervals
	auctionSlotIntervalDuration = totalTimeSlotSecs / auctionSlotTimeIntervals // 4320 seconds
)

// rippleEpochToISO8601 converts a Ripple epoch timestamp to an ISO 8601 string.
// Matches rippled's to_iso8601() in AMMInfo.cpp.
func rippleEpochToISO8601(rippleSeconds uint32) string {
	unixTime := int64(rippleSeconds) + protocol.RippleEpochUnix
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

type ammIssue struct {
	Currency  string
	IssuerStr string
	IssuerID  [20]byte
	IsXRP     bool
}

// The binary codec emits {"currency":"XRP"} for XRP, {"currency":..,"issuer":..}
// for IOUs, and {"mpt_issuance_id":..} for MPTs (codec/binarycodec/types/issue.go).
// AMMs only support XRP+IOU and IOU+IOU pairs today; an MPT in either slot is
// surfaced as an explicit error rather than masquerading as a missing field.
func parseSLEIssue(raw interface{}) (ammIssue, error) {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return ammIssue{}, fmt.Errorf("not an object")
	}
	if _, isMPT := m["mpt_issuance_id"]; isMPT {
		return ammIssue{}, fmt.Errorf("MPT assets are not supported in AMM pools")
	}
	currency, ok := m["currency"].(string)
	if !ok {
		return ammIssue{}, fmt.Errorf("missing currency field")
	}
	if currency == "XRP" {
		return ammIssue{Currency: "XRP", IsXRP: true}, nil
	}
	issuerStr, ok := m["issuer"].(string)
	if !ok {
		return ammIssue{}, fmt.Errorf("missing issuer field for non-XRP currency")
	}
	_, issuerBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuerStr)
	if err != nil {
		return ammIssue{}, fmt.Errorf("invalid issuer %q: %w", issuerStr, err)
	}
	out := ammIssue{Currency: currency, IssuerStr: issuerStr}
	copy(out.IssuerID[:], issuerBytes)
	return out, nil
}

// Matches rippled accountHolds() with FreezeHandling::fhIGNORE_FREEZE.
func readAMMHolds(view types.LedgerStateView, ammAccountID [20]byte, issue ammIssue) state.Amount {
	if issue.IsXRP {
		data, err := view.Read(keylet.Account(ammAccountID))
		if err != nil || data == nil {
			return state.NewXRPAmountFromInt(0)
		}
		account, err := state.ParseAccountRoot(data)
		if err != nil {
			return state.NewXRPAmountFromInt(0)
		}
		return state.NewXRPAmountFromInt(int64(account.Balance))
	}

	zero := state.NewIssuedAmountFromValue(0, 0, issue.Currency, issue.IssuerStr)

	// AMM-account-as-issuer is not a meaningful balance; rippled never gets
	// here because AMM asset issuers cannot be the AMM account itself.
	if ammAccountID == issue.IssuerID {
		return zero
	}

	data, err := view.Read(keylet.Line(ammAccountID, issue.IssuerID, issue.Currency))
	if err != nil || data == nil {
		return zero
	}
	rs, err := state.ParseRippleState(data)
	if err != nil {
		return zero
	}

	// Trust-line balance is stored from the low account's perspective; flip
	// the sign if the AMM is the high account. Matches rippled accountHolds()
	// (View.cpp:448): in fhIGNORE_FREEZE mode the signed value is returned
	// without clamping at zero.
	balance := rs.Balance
	if state.CompareAccountIDsForLine(ammAccountID, issue.IssuerID) > 0 {
		balance = balance.Negate()
	}
	iou := balance.IOU()
	return state.NewIssuedAmountFromValue(iou.Mantissa(), iou.Exponent(), issue.Currency, issue.IssuerStr)
}

// Frozen if the issuer has global freeze set, or has frozen its side of the
// trust line. Matches rippled isFrozen() in View.cpp.
func isAssetFrozen(view types.LedgerStateView, ammAccountID [20]byte, issue ammIssue) bool {
	if issue.IsXRP {
		return false
	}
	if data, err := view.Read(keylet.Account(issue.IssuerID)); err == nil && data != nil {
		if issuerRoot, perr := state.ParseAccountRoot(data); perr == nil {
			if issuerRoot.Flags&state.LsfGlobalFreeze != 0 {
				return true
			}
		}
	}
	if ammAccountID == issue.IssuerID {
		return false
	}
	data, err := view.Read(keylet.Line(ammAccountID, issue.IssuerID, issue.Currency))
	if err != nil || data == nil {
		return false
	}
	rs, err := state.ParseRippleState(data)
	if err != nil {
		return false
	}
	// The freeze flag lives on the issuer's side of the line.
	issuerIsHigh := state.CompareAccountIDsForLine(issue.IssuerID, ammAccountID) > 0
	if issuerIsHigh {
		return rs.Flags&state.LsfHighFreeze != 0
	}
	return rs.Flags&state.LsfLowFreeze != 0
}
