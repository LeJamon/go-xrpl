package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
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
		Asset      json.RawMessage `json:"asset,omitempty"`
		Asset2     json.RawMessage `json:"asset2,omitempty"`
		AMMAccount json.RawMessage `json:"amm_account,omitempty"`
		Account    json.RawMessage `json:"account,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// Key presence decides which checks run (rippled goes through isMember),
	// so null or empty values still count as supplied.
	hasAsset := len(request.Asset) > 0
	hasAsset2 := len(request.Asset2) > 0
	hasAMMAccount := len(request.AMMAccount) > 0
	hasLPAccount := len(request.Account) > 0

	// asset and asset2 must come together, and exactly one of (asset pair,
	// amm_account) must be given.
	invalidCombination := hasAsset != hasAsset2 || hasAsset == hasAMMAccount

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	ledgerIndex, selErr := resolveLedgerSelector(request.LedgerSpecifier)
	if selErr != nil {
		return nil, selErr
	}

	// rippled resolves the ledger before validating any parameter
	// (AMMInfo.cpp:81-84), so an explicitly named missing/malformed ledger
	// outranks every param error; the validated/current/closed shortcuts are
	// always available from the service.
	switch ledgerIndex {
	case "current", "closed", "validated":
	default:
		if _, _, lerr := LookupLedger(ctx, request.LedgerSpecifier); lerr != nil {
			return nil, lerr
		}
	}

	// For api_version < 3 the combination check runs before the per-field
	// checks; for api_version >= 3 it runs after them, so a malformed
	// asset/account/amm_account takes precedence (rippled AMMInfo.cpp:108-150).
	if ctx.ApiVersion < types.ApiVersion3 && invalidCombination {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}

	var issue1Issuer, issue1Currency, issue2Issuer, issue2Currency [20]byte
	if hasAsset {
		var parseErr error
		issue1Issuer, issue1Currency, parseErr = parseIssue(request.Asset)
		if parseErr != nil {
			return nil, types.RpcErrorIssueMalformed()
		}
	}
	if hasAsset2 {
		var parseErr error
		issue2Issuer, issue2Currency, parseErr = parseIssue(request.Asset2)
		if parseErr != nil {
			return nil, types.RpcErrorIssueMalformed()
		}
	}

	var ammKey [32]byte
	if hasAMMAccount {
		// rippled AMMInfo returns actMalformed (not invalidParams or
		// actNotFound) both when the amm_account does not parse and when it
		// does not exist in the ledger.
		_, accountEntry, rpcErr := readAccountRoot(ctx, ledgerIndex, accountIdent(request.AMMAccount))
		if rpcErr != nil {
			return nil, rpcErr
		}

		// Decode the account to get AMMID
		decoded, decodeErr := binarycodec.Decode(hex.EncodeToString(accountEntry.Node))
		if decodeErr != nil {
			return nil, types.RpcErrorInternal("Failed to decode account: " + decodeErr.Error())
		}

		ammIDHex, ok := decoded["AMMID"].(string)
		if !ok || ammIDHex == "" {
			return nil, types.RpcErrorActNotFound("Account not found.")
		}

		ammIDBytes, hexErr := hex.DecodeString(ammIDHex)
		if hexErr != nil || len(ammIDBytes) != 32 {
			return nil, types.RpcErrorInternal("Invalid AMMID in account")
		}
		copy(ammKey[:], ammIDBytes)
		if ammKey == ([32]byte{}) {
			return nil, types.RpcErrorActNotFound("Account not found.")
		}
	}

	var lpAccountID [20]byte
	if hasLPAccount {
		var rpcErr *types.RpcError
		lpAccountID, _, rpcErr = readAccountRoot(ctx, ledgerIndex, accountIdent(request.Account))
		if rpcErr != nil {
			return nil, rpcErr
		}
	}

	if ctx.ApiVersion >= types.ApiVersion3 && invalidCombination {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}

	if !hasAMMAccount {
		ammKey = keylet.AMM(issue1Issuer, issue1Currency, issue2Issuer, issue2Currency).Key
	}

	ammEntry, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, ammKey, ledgerIndex)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		return nil, types.RpcErrorActNotFound("Account not found.")
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
		// With the account param, lp_token reports that account's LP token
		// balance instead of the pool total (rippled AMMInfo.cpp:195-197).
		if hasLPAccount && haveAMMAccountID {
			if total, ok := lpToken.(map[string]any); ok {
				ammResult["lp_token"] = ammLPHoldsJSON(ctx, ledgerIndex, ammAccountID, lpAccountID, total)
			}
		}
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

// parseIssue parses an asset/issue object, enforcing rippled's issueFromJson
// rules (Issue.cpp:94-145): a JSON object with a valid currency code, an
// issuer exactly when the currency is not XRP, and no mpt_issuance_id.
// Returns issuer (20 bytes), currency (20 bytes), and error.
func parseIssue(raw json.RawMessage) ([20]byte, [20]byte, error) {
	var issuer, currency [20]byte

	var issue map[string]any
	if err := json.Unmarshal(raw, &issue); err != nil {
		return issuer, currency, errors.New("issue must be a JSON object")
	}
	if _, ok := issue["mpt_issuance_id"]; ok {
		return issuer, currency, errors.New("issue must not have mpt_issuance_id")
	}

	currencyStr, ok := issue["currency"].(string)
	if !ok {
		return issuer, currency, errors.New("missing or non-string currency field")
	}
	currency, err := currencyFromString(currencyStr)
	if err != nil {
		return issuer, [20]byte{}, err
	}

	// XRP: no issuer allowed (an explicit null is tolerated, like rippled).
	if currency == ([20]byte{}) {
		if issuerVal, ok := issue["issuer"]; ok && issuerVal != nil {
			return issuer, currency, errors.New("XRP must not have an issuer")
		}
		return issuer, currency, nil
	}

	issuerStr, ok := issue["issuer"].(string)
	if !ok {
		return issuer, currency, errors.New("missing or non-string issuer field")
	}
	_, issuerBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuerStr)
	if err != nil {
		return issuer, currency, fmt.Errorf("invalid issuer: %w", err)
	}
	copy(issuer[:], issuerBytes)

	return issuer, currency, nil
}

// isoCurrencyChars is rippled to_currency's character set for 3-letter
// ISO-style codes (UintTypes.cpp:39-43).
const isoCurrencyChars = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ" +
	"0123456789" +
	"<>(){}[]|?!@#$%^&*"

// Reserved 160-bit currency values issueFromJson rejects: noCurrency ("1")
// and badCurrency (ISO-style "XRP" spelled out in hex).
var (
	noCurrencyBytes  = [20]byte{19: 0x01}
	badCurrencyBytes = [20]byte{12: 'X', 13: 'R', 14: 'P'}
)

// currencyFromString validates a currency code with to_currency's rules —
// empty/"XRP" mean native XRP, otherwise 3 characters from the ISO set or
// 40 hex digits — then encodes it through keylet.CurrencyBytes, the
// canonical write-path encoder used by AMMCreate, so the keying stays
// symmetric.
func currencyFromString(code string) ([20]byte, error) {
	if code == "" || code == "XRP" {
		return [20]byte{}, nil
	}
	switch len(code) {
	case 3:
		for i := 0; i < len(code); i++ {
			if strings.IndexByte(isoCurrencyChars, code[i]) < 0 {
				return [20]byte{}, errors.New("invalid character in currency code")
			}
		}
	case 40:
		if _, err := hex.DecodeString(code); err != nil {
			return [20]byte{}, errors.New("invalid hex currency code")
		}
	default:
		return [20]byte{}, errors.New("invalid currency code length")
	}

	currency := keylet.CurrencyBytes(code)
	if currency == noCurrencyBytes || currency == badCurrencyBytes {
		return currency, errors.New("reserved currency code")
	}
	return currency, nil
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

// accountIdent extracts the string form of an account parameter; any
// non-string value yields "", which never resolves to an account.
func accountIdent(raw json.RawMessage) string {
	var ident string
	if err := json.Unmarshal(raw, &ident); err != nil {
		return ""
	}
	return ident
}

// accountFromString resolves an account identifier the way rippled's
// RPC::accountFromString does in non-strict mode (RPCHelpers.cpp:43-85): a
// base58 account public key, a classic address, or — as a debugging
// convenience — anything that parses as a generic seed, whose secp256k1
// keypair identifies the account.
func accountFromString(ident string) ([20]byte, bool) {
	var accountID [20]byte
	if pubKey, err := addresscodec.DecodeAccountPublicKey(ident); err == nil {
		copy(accountID[:], addresscodec.Sha256RipeMD160(pubKey))
		return accountID, true
	}
	if _, raw, err := addresscodec.DecodeClassicAddressToAccountID(ident); err == nil {
		copy(accountID[:], raw)
		return accountID, true
	}

	seed, ok := parseGenericSeed(ident)
	if !ok {
		return accountID, false
	}
	_, pubKeyHex, err := secp256k1.SECP256K1().DeriveKeypair(seed, false)
	if err != nil {
		return accountID, false
	}
	pubKey, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return accountID, false
	}
	copy(accountID[:], addresscodec.Sha256RipeMD160(pubKey))
	return accountID, true
}

// readAccountRoot resolves an account identifier to its AccountRoot entry.
// Both an unresolvable identifier and a missing account map to actMalformed,
// matching rippled's handling of amm_info account parameters; a missing
// ledger keeps its own error.
func readAccountRoot(ctx *types.RpcContext, ledgerIndex, ident string) ([20]byte, *types.LedgerEntryResult, *types.RpcError) {
	accountID, ok := accountFromString(ident)
	if !ok {
		return accountID, nil, types.RpcErrorActMalformed("Account malformed.")
	}

	entry, err := ctx.Services.Ledger.GetLedgerEntry(ctx.Context, keylet.Account(accountID).Key, ledgerIndex)
	if err != nil {
		if errors.Is(err, svcerr.ErrLedgerNotFound) {
			return accountID, nil, types.RpcErrorLgrNotFound("Ledger not found.")
		}
		return accountID, nil, types.RpcErrorActMalformed("Account malformed.")
	}
	return accountID, entry, nil
}

// ammLPHoldsJSON returns the LP account's holding of the AMM's LP token as
// an STAmount-style JSON value: the balance of the LP's trust line with the
// AMM account, zero when the line is missing or frozen. total supplies the
// LP token currency and issuer from the AMM SLE's LPTokenBalance.
func ammLPHoldsJSON(ctx *types.RpcContext, ledgerIndex string, ammAccountID, lpAccountID [20]byte, total map[string]any) map[string]any {
	currency, _ := total["currency"].(string)
	issuer, _ := total["issuer"].(string)
	issue := ammIssue{Currency: currency, Issuer: ammAccountID, IssuerR: issuer}

	value := "0"
	if !ammIssueFrozen(ctx, ledgerIndex, lpAccountID, issue) {
		value = readAMMIOUBalance(ctx, ledgerIndex, lpAccountID, issue)
	}
	return map[string]any{"currency": currency, "issuer": issuer, "value": value}
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
