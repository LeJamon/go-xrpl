package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

// AMMInfoMethod handles the amm_info RPC method
type AMMInfoMethod struct{ BaseHandler }

func (m *AMMInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	var request struct {
		types.LedgerSpecifier
		Asset      map[string]any `json:"asset,omitempty"`
		Asset2     map[string]any `json:"asset2,omitempty"`
		AMMAccount string         `json:"amm_account,omitempty"`
		Account    string         `json:"account,omitempty"`
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
	ammResult := make(map[string]any)

	// Copy relevant fields
	var ammAccountID [20]byte
	var haveAMMAccountID bool
	if account, ok := decoded["Account"].(string); ok {
		ammResult["account"] = account
		if _, accID, decErr := addresscodec.DecodeClassicAddressToAccountID(account); decErr == nil {
			copy(ammAccountID[:], accID)
			haveAMMAccountID = true
		}
	}
	if lpToken, ok := decoded["LPTokenBalance"]; ok {
		ammResult["lp_token"] = lpToken
	}
	if tradingFee, ok := decoded["TradingFee"]; ok {
		ammResult["trading_fee"] = tradingFee
	}

	// amount/amount2 mirror rippled's ammPoolHolds(): the AMM account's actual
	// trust-line (or XRP) balance for each issue, not the sfAsset/sfAsset2 issue
	// definitions. asset_frozen/asset2_frozen surface isFrozen() on the same
	// trust lines (non-XRP only). See rippled AMMInfo.cpp:188-262.
	asset1Issue, asset1OK := extractIssue(decoded["Asset"])
	asset2Issue, asset2OK := extractIssue(decoded["Asset2"])

	if haveAMMAccountID && asset1OK {
		ammResult["amount"] = ammPoolBalanceJSON(ctx, ledgerIndex, ammAccountID, asset1Issue)
		if !asset1Issue.IsXRP() {
			ammResult["asset_frozen"] = ammIssueFrozen(ctx, ledgerIndex, ammAccountID, asset1Issue)
		}
	} else if asset, ok := decoded["Asset"]; ok {
		ammResult["amount"] = asset
	}

	if haveAMMAccountID && asset2OK {
		ammResult["amount2"] = ammPoolBalanceJSON(ctx, ledgerIndex, ammAccountID, asset2Issue)
		if !asset2Issue.IsXRP() {
			ammResult["asset2_frozen"] = ammIssueFrozen(ctx, ledgerIndex, ammAccountID, asset2Issue)
		}
	} else if asset2, ok := decoded["Asset2"]; ok {
		ammResult["amount2"] = asset2
	}

	// Handle vote slots
	if voteSlots, ok := decoded["VoteSlots"].([]any); ok && len(voteSlots) > 0 {
		votes := make([]map[string]any, 0, len(voteSlots))
		for _, vs := range voteSlots {
			if voteEntry, ok := vs.(map[string]any); ok {
				if voteSlot, ok := voteEntry["VoteEntry"].(map[string]any); ok {
					vote := make(map[string]any)
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
	if auctionSlot, ok := decoded["AuctionSlot"].(map[string]any); ok {
		auction := buildAuctionSlot(auctionSlot, parentCloseTime)
		if auction != nil {
			ammResult["auction_slot"] = auction
		}
	}

	// Build final response
	response := map[string]any{
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
func buildAuctionSlot(auctionSlot map[string]any, parentCloseTime uint64) map[string]any {
	account, ok := auctionSlot["Account"].(string)
	if !ok || account == "" {
		// rippled: only includes auction_slot if auctionSlot.isFieldPresent(sfAccount)
		return nil
	}

	auction := make(map[string]any)
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
	if authAccounts, ok := auctionSlot["AuthAccounts"].([]any); ok {
		auth := make([]map[string]any, 0, len(authAccounts))
		for _, aa := range authAccounts {
			if wrapper, ok := aa.(map[string]any); ok {
				// Unwrap the AuthAccount inner object
				inner, ok := wrapper["AuthAccount"].(map[string]any)
				if !ok {
					// Fallback: try direct Account field (in case codec doesn't wrap)
					inner = wrapper
				}
				if acct, ok := inner["Account"].(string); ok {
					auth = append(auth, map[string]any{"account": acct})
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
func toUint32(v any) uint32 {
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
func parseIssue(issue map[string]any) ([20]byte, [20]byte, error) {
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

	// state.GetCurrencyBytes is the canonical write-path encoder used by
	// AMMCreate; routing the lookup through it keeps the keying symmetric.
	currency = state.GetCurrencyBytes(currencyStr)

	return issuer, currency, nil
}

// ammIssue carries the asset definition decoded from the AMM SLE's
// sfAsset/sfAsset2 fields. Currency stays in its codec form (3-char ISO or
// 40-char hex) so it can be passed straight to keylet.Line and re-emitted
// in the response unchanged.
type ammIssue struct {
	Currency string
	Issuer   [20]byte
	IssuerR  string // r-address form of Issuer; empty for XRP
}

// IsXRP reports whether this issue is native XRP.
func (i ammIssue) IsXRP() bool {
	return i.Currency == "XRP" || i.Currency == ""
}

// extractIssue pulls an ammIssue out of a decoded sfAsset/sfAsset2 field.
// Matches the {"currency": "XRP"} / {"currency": ..., "issuer": ...} shape
// produced by binarycodec.types.Issue.ToJSON.
func extractIssue(raw any) (ammIssue, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return ammIssue{}, false
	}
	currencyStr, ok := m["currency"].(string)
	if !ok {
		return ammIssue{}, false
	}
	issue := ammIssue{Currency: currencyStr}
	if issue.IsXRP() {
		return issue, true
	}
	issuerStr, ok := m["issuer"].(string)
	if !ok {
		return ammIssue{}, false
	}
	_, issuerBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuerStr)
	if err != nil || len(issuerBytes) != 20 {
		return ammIssue{}, false
	}
	copy(issue.Issuer[:], issuerBytes)
	issue.IssuerR = issuerStr
	return issue, true
}

// ammPoolBalanceJSON returns the AMM account's holding of an issue, formatted
// as an STAmount-style JSON value (drops string for XRP, {currency, issuer,
// value} for IOU). Missing AccountRoot/RippleState yields a zero amount,
// matching rippled's accountHolds() fallback (View.cpp:385-465).
// Reference: rippled AMMInfo.cpp:188-194 + AMMUtils.cpp ammPoolHolds (which
// calls accountHolds with fhIGNORE_FREEZE — i.e. the balance is reported even
// when the trust line is frozen).
func ammPoolBalanceJSON(ctx *types.RpcContext, ledgerIndex string, ammAccountID [20]byte, issue ammIssue) any {
	if issue.IsXRP() {
		drops := readAMMXRPBalance(ctx, ledgerIndex, ammAccountID)
		return strconv.FormatUint(drops, 10)
	}

	// IOU: read the trust line between the AMM account and the issuer.
	value := readAMMIOUBalance(ctx, ledgerIndex, ammAccountID, issue)
	return map[string]any{
		"currency": issue.Currency,
		"issuer":   issue.IssuerR,
		"value":    value,
	}
}

// readAMMXRPBalance returns the AMM account's XRP balance in drops, or 0 when
// the AccountRoot can't be read.
func readAMMXRPBalance(ctx *types.RpcContext, ledgerIndex string, ammAccountID [20]byte) uint64 {
	entry, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, keylet.Account(ammAccountID).Key, ledgerIndex)
	if err != nil || entry == nil || len(entry.Node) == 0 {
		return 0
	}
	root, err := state.ParseAccountRoot(entry.Node)
	if err != nil || root == nil {
		return 0
	}
	return root.Balance
}

// readAMMIOUBalance returns the AMM account's trust-line balance for the
// given issue as a decimal string, in the AMM account's terms (negated when
// the AMM is the high account). Returns "0" if the trust line is missing.
//
// Reference: rippled View.cpp accountHolds() lines 432-455 — balance from the
// AMM's perspective, with .setIssuer(issuer) applied to the result.
func readAMMIOUBalance(ctx *types.RpcContext, ledgerIndex string, ammAccountID [20]byte, issue ammIssue) string {
	entry, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, keylet.Line(ammAccountID, issue.Issuer, issue.Currency).Key, ledgerIndex)
	if err != nil || entry == nil || len(entry.Node) == 0 {
		return "0"
	}
	rs, err := state.ParseRippleState(entry.Node)
	if err != nil || rs == nil {
		return "0"
	}
	balance := rs.Balance
	// RippleState raw balance: positive means low account holds IOUs from high
	// account. Flip when AMM is the high account to put it in AMM terms.
	if bytes.Compare(ammAccountID[:], issue.Issuer[:]) > 0 {
		balance = balance.Negate()
	}
	return balance.Value()
}

// ammIssueFrozen reports whether the AMM account's view of an IOU issue is
// frozen, mirroring rippled's isFrozen(view, account, currency, issuer) at
// View.cpp:227-269. Only valid for IOU issues; XRP is never frozen.
//
// A balance is considered frozen if either:
//   - the issuer's AccountRoot has the GlobalFreeze flag, or
//   - the trust line's freeze flag is set on the issuer's side (HighFreeze
//     when the issuer is the high account, LowFreeze otherwise).
func ammIssueFrozen(ctx *types.RpcContext, ledgerIndex string, ammAccountID [20]byte, issue ammIssue) bool {
	if issue.IsXRP() {
		return false
	}

	// Global freeze on the issuer.
	if issuerEntry, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, keylet.Account(issue.Issuer).Key, ledgerIndex); err == nil && issuerEntry != nil && len(issuerEntry.Node) > 0 {
		if issuerRoot, perr := state.ParseAccountRoot(issuerEntry.Node); perr == nil && issuerRoot != nil {
			if (issuerRoot.Flags & state.LsfGlobalFreeze) != 0 {
				return true
			}
		}
	}

	// Individual freeze on the trust line — checked on the issuer's side.
	lineEntry, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, keylet.Line(ammAccountID, issue.Issuer, issue.Currency).Key, ledgerIndex)
	if err != nil || lineEntry == nil || len(lineEntry.Node) == 0 {
		return false
	}
	rs, err := state.ParseRippleState(lineEntry.Node)
	if err != nil || rs == nil {
		return false
	}
	// The issuer's freeze flag lives on the issuer's side of the line:
	// HighFreeze if issuer is high (issuer > AMM), LowFreeze otherwise.
	if bytes.Compare(issue.Issuer[:], ammAccountID[:]) > 0 {
		return (rs.Flags & state.LsfHighFreeze) != 0
	}
	return (rs.Flags & state.LsfLowFreeze) != 0
}
