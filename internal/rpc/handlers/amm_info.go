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

	if hasAssets == hasAMMAccount {
		return nil, types.RpcErrorInvalidParams("Must specify either (asset + asset2) or amm_account, but not both or neither")
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	ledgerIndexRequested := request.LedgerIndex != ""
	ledgerHashRequested := request.LedgerHash != ""
	ledgerIndex := "validated"
	if ledgerIndexRequested {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Pin a single ledger snapshot for the duration of this request. Rippled's
	// doAMMInfo resolves one ReadView and passes it to both the SLE lookup and
	// ammPoolHolds (AMMInfo.cpp:81-83, :188); we mirror that to avoid mixing a
	// historical SLE with current trust-line state.
	view, ledgerReader, viewErr := ctx.Services.Ledger.GetLedgerForQuery(ledgerIndex)
	if viewErr != nil {
		return nil, types.RpcErrorActNotFound("ledger not found: " + viewErr.Error())
	}

	var ammKey [32]byte

	if hasAMMAccount {
		_, accountID, decErr := addresscodec.DecodeClassicAddressToAccountID(request.AMMAccount)
		if decErr != nil {
			return nil, types.RpcErrorInvalidParams("Invalid amm_account: " + decErr.Error())
		}

		var accountIDArray [20]byte
		copy(accountIDArray[:], accountID)

		accountData, readErr := view.Read(keylet.Account(accountIDArray))
		if readErr != nil || accountData == nil {
			return nil, &types.RpcError{
				Code:    19,
				Message: "AMM account not found",
			}
		}

		decoded, decodeErr := binarycodec.Decode(hex.EncodeToString(accountData))
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

	ammData, readErr := view.Read(keylet.Keylet{Key: ammKey})
	if readErr != nil || ammData == nil {
		return nil, types.RpcErrorActNotFound("AMM not found")
	}

	decoded, decodeErr := binarycodec.Decode(hex.EncodeToString(ammData))
	if decodeErr != nil {
		return nil, types.RpcErrorInternal("Failed to decode AMM: " + decodeErr.Error())
	}

	ammResult := make(map[string]interface{})

	if account, ok := decoded["Account"].(string); ok {
		ammResult["account"] = account
	}
	if tradingFee, ok := decoded["TradingFee"]; ok {
		ammResult["trading_fee"] = tradingFee
	}

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

	// rippled passes FreezeHandling::fhIGNORE_FREEZE for pool balances
	// (AMMInfo.cpp:193): balances reflect raw pool holdings; the freeze
	// status is reported separately via asset[2]_frozen.
	bal1 := readAMMHolds(view, ammAccountID, asset1)
	bal2 := readAMMHolds(view, ammAccountID, asset2)
	ammResult["amount"] = formatAmountJSON(bal1)
	ammResult["amount2"] = formatAmountJSON(bal2)

	// lp_token: when "account" is supplied, rippled returns that requester's
	// LP balance via ammLPHolds (AMMInfo.cpp:195-197). Otherwise return the
	// pool-wide LPTokenBalance from the SLE.
	lpTokenBalanceField := decoded["LPTokenBalance"]
	if request.Account != "" {
		_, lpAccBytes, accErr := addresscodec.DecodeClassicAddressToAccountID(request.Account)
		if accErr != nil {
			return nil, types.RpcErrorActMalformed("Invalid account: " + accErr.Error())
		}
		var lpAccountID [20]byte
		copy(lpAccountID[:], lpAccBytes)
		if data, err := view.Read(keylet.Account(lpAccountID)); err != nil || data == nil {
			return nil, types.RpcErrorActMalformed("account not found in ledger")
		}
		lpCurrency, lpErr := lpTokenCurrencyFromSLE(lpTokenBalanceField)
		if lpErr != nil {
			return nil, types.RpcErrorInternal("AMM SLE LPTokenBalance: " + lpErr.Error())
		}
		lpBalance := accountLPHolds(view, ammAccountID, lpAccountID, lpCurrency, accountStr)
		ammResult["lp_token"] = formatAmountJSON(lpBalance)
	} else if lpTokenBalanceField != nil {
		ammResult["lp_token"] = lpTokenBalanceField
	}

	if !asset1.IsXRP {
		ammResult["asset_frozen"] = isAssetFrozen(view, ammAccountID, asset1)
	}
	if !asset2.IsXRP {
		ammResult["asset2_frozen"] = isAssetFrozen(view, ammAccountID, asset2)
	}

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

	var parentCloseTime uint64
	if pct := ledgerReader.ParentCloseTime(); pct > 0 {
		parentCloseTime = uint64(pct)
	}

	if auctionSlot, ok := decoded["AuctionSlot"].(map[string]interface{}); ok {
		auction := buildAuctionSlot(auctionSlot, parentCloseTime)
		if auction != nil {
			ammResult["auction_slot"] = auction
		}
	}

	// Match rippled's lookupLedger field shape (AMMInfo.cpp:265-267):
	// emit ledger_index/ledger_hash when the request named them, otherwise
	// ledger_current_index for the default ("current") path.
	response := map[string]interface{}{
		"amm":       ammResult,
		"validated": ledgerReader.IsValidated(),
	}
	switch {
	case ledgerHashRequested:
		response["ledger_hash"] = FormatLedgerHash(ledgerReader.Hash())
	case ledgerIndexRequested:
		response["ledger_index"] = ledgerReader.Sequence()
	default:
		response["ledger_current_index"] = ledgerReader.Sequence()
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

// Matches rippled accountHolds() with FreezeHandling::fhIGNORE_FREEZE. For the
// XRP branch rippled routes through xrpLiquid (View.cpp:394-396, :615-651),
// which subtracts the account reserve — except for AMM accounts, where the
// sfAMMID-present path sets reserve to 0 (View.cpp:631-633). Since this helper
// is only called with an AMM account, returning the raw sfBalance is
// numerically equivalent to xrpLiquid without re-deriving the reserve.
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

// Matches rippled isFrozen() in View.cpp (issuer global freeze, or issuer-side
// trust-line freeze).
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

// lpTokenCurrencyFromSLE extracts the LP token currency code from a decoded
// LPTokenBalance field. The binary codec emits IOU amounts as
// {"currency": ..., "issuer": ..., "value": ...}; the LP token currency is
// canonical (issuer = AMM account) so we only need the currency string.
func lpTokenCurrencyFromSLE(raw interface{}) (string, error) {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("missing or non-object LPTokenBalance")
	}
	currency, ok := m["currency"].(string)
	if !ok || currency == "" {
		return "", fmt.Errorf("LPTokenBalance has no currency")
	}
	return currency, nil
}

// accountLPHolds mirrors rippled ammLPHolds (AMMUtils.cpp:113-160): read the
// requester's trust line against the AMM account in the LP token currency,
// return zero on a missing line or any freeze, otherwise return the signed
// balance with issuer pinned to the AMM account.
func accountLPHolds(
	view types.LedgerStateView,
	ammAccountID, lpAccountID [20]byte,
	lpCurrency string,
	ammAccountStr string,
) state.Amount {
	zero := state.NewIssuedAmountFromValue(0, 0, lpCurrency, ammAccountStr)
	if ammAccountID == lpAccountID {
		return zero
	}
	data, err := view.Read(keylet.Line(lpAccountID, ammAccountID, lpCurrency))
	if err != nil || data == nil {
		return zero
	}
	lpIssue := ammIssue{Currency: lpCurrency, IssuerID: ammAccountID, IssuerStr: ammAccountStr}
	if isAssetFrozen(view, lpAccountID, lpIssue) {
		return zero
	}
	rs, err := state.ParseRippleState(data)
	if err != nil {
		return zero
	}
	balance := rs.Balance
	if state.CompareAccountIDsForLine(lpAccountID, ammAccountID) > 0 {
		balance = balance.Negate()
	}
	iou := balance.IOU()
	return state.NewIssuedAmountFromValue(iou.Mantissa(), iou.Exponent(), lpCurrency, ammAccountStr)
}
