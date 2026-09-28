package handlers

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
)

// AMMInfoMethod handles the amm_info RPC method
type AMMInfoMethod struct{ baseHandler }

func (m *AMMInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	var request struct {
		Asset      json.RawMessage `json:"asset,omitempty"`
		Asset2     json.RawMessage `json:"asset2,omitempty"`
		AMMAccount json.RawMessage `json:"amm_account,omitempty"`
		Account    json.RawMessage `json:"account,omitempty"`
	}

	if err := parseParams(params, &request); err != nil {
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

	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	selection, selErr := parseLedgerSelectorParams(params, ledgerselector.Current())
	if selErr != nil {
		return nil, selErr
	}
	resolvedLedger, ledgerErr := resolveLedgerSelection(ctx, selection)
	if ledgerErr != nil {
		return nil, ledgerErr
	}
	ledgerIndex := strconv.FormatUint(uint64(resolvedLedger.Sequence), 10)

	// For api_version < 3 the combination check runs before the per-field
	// checks; for api_version >= 3 it runs after them, so a malformed
	// asset/account/amm_account takes precedence (rippled AMMInfo.cpp:108-150).
	if ctx.ApiVersion < types.ApiVersion3 && invalidCombination {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}

	var requestAsset1, requestAsset2 ammIssue
	if hasAsset {
		var parseErr error
		requestAsset1, parseErr = parseAMMAsset(request.Asset)
		if parseErr != nil {
			return nil, rpcerrors.RpcErrorIssueMalformed()
		}
	}
	if hasAsset2 {
		var parseErr error
		requestAsset2, parseErr = parseAMMAsset(request.Asset2)
		if parseErr != nil {
			return nil, rpcerrors.RpcErrorIssueMalformed()
		}
	}

	var ammKey [32]byte
	if hasAMMAccount {
		ammAccount, ok := accountIdent(request.AMMAccount)
		if !ok {
			return nil, rpcerrors.RpcErrorActMalformed("Account malformed.")
		}
		// rippled AMMInfo returns actMalformed (not invalidParams or
		// actNotFound) both when the amm_account does not parse and when it
		// does not exist in the ledger.
		_, accountEntry, rpcErr := readAccountRoot(ctx, ledgerIndex, ammAccount)
		if rpcErr != nil {
			return nil, rpcErr
		}

		var decoded ledgerfields.AccountRoot
		if decodeErr := decoded.Decode(accountEntry.Node); decodeErr != nil {
			return nil, rpcInternalError("amm_info: account decoding failed", decodeErr)
		}
		if !decoded.HasAMMID() {
			return nil, rpcerrors.RpcErrorActNotFound("Account not found.")
		}
		var decodeErr error
		ammKey, decodeErr = decoded.GetAMMID()
		if decodeErr != nil {
			return nil, rpcInternalError("amm_info: AMMID decoding failed", decodeErr)
		}
		if ammKey == ([32]byte{}) {
			return nil, rpcerrors.RpcErrorActNotFound("Account not found.")
		}
	}

	var lpAccountID [20]byte
	if hasLPAccount {
		account, ok := accountIdent(request.Account)
		if !ok {
			return nil, rpcerrors.RpcErrorActMalformed("Account malformed.")
		}
		var rpcErr *rpcerrors.RpcError
		lpAccountID, _, rpcErr = readAccountRoot(ctx, ledgerIndex, account)
		if rpcErr != nil {
			return nil, rpcErr
		}
	}

	if ctx.ApiVersion >= types.ApiVersion3 && invalidCombination {
		return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters.")
	}

	if !hasAMMAccount {
		ammKey = keylet.AMMAsset(requestAsset1.bookSide(), requestAsset2.bookSide()).Key
	}

	ammEntry, err := ctx.Services.Ledger().GetLedgerEntry(ctx.Context, ammKey, ledgerIndex)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		return nil, rpcerrors.RpcErrorActNotFound("Account not found.")
	}

	var decoded ledgerfields.AMM
	if decodeErr := decoded.Decode(ammEntry.Node); decodeErr != nil {
		return nil, rpcInternalError("amm_info: AMM decoding failed", decodeErr)
	}
	ammAccountID, decodeErr := decoded.GetAccount()
	if decodeErr != nil {
		return nil, rpcInternalError("amm_info: AMM account decoding failed", decodeErr)
	}
	lpToken, decodeErr := decoded.GetLPTokenBalance()
	if decodeErr != nil {
		return nil, rpcInternalError("amm_info: LP token decoding failed", decodeErr)
	}
	ammResult := map[string]any{
		"account":  decoded.Account,
		"lp_token": decoded.LPTokenBalance,
	}
	if hasLPAccount {
		ammResult["lp_token"] = ammLPHoldsJSON(ctx, ledgerIndex, ammAccountID, lpAccountID, lpToken)
	}
	ammResult["trading_fee"] = decoded.TradingFee

	asset1Issue, asset2Issue := requestAsset1, requestAsset2
	if hasAMMAccount {
		asset1, err := decoded.GetAsset()
		if err != nil {
			return nil, rpcInternalError("amm_info: asset decoding failed", err)
		}
		asset2, err := decoded.GetAsset2()
		if err != nil {
			return nil, rpcInternalError("amm_info: asset2 decoding failed", err)
		}
		var asset1OK, asset2OK bool
		asset1Issue, asset1OK = extractIssue(asset1)
		asset2Issue, asset2OK = extractIssue(asset2)
		if !asset1OK || !asset2OK {
			return nil, rpcInternalInvariantError("amm_info: invalid pool asset")
		}
	}
	var assetView types.LedgerStateView
	if asset1Issue.MPTID != nil || asset2Issue.MPTID != nil {
		var err error
		assetView, err = resolvedLedgerStateView(ctx, resolvedLedger.Value)
		if err != nil {
			return nil, rpcInternalError("amm_info: asset state lookup failed", err)
		}
	}
	for _, field := range []struct {
		amount, frozen string
		issue          ammIssue
	}{
		{"amount", "asset_frozen", asset1Issue},
		{"amount2", "asset2_frozen", asset2Issue},
	} {
		ammResult[field.amount] = ammPoolBalanceJSON(ctx, ledgerIndex, ammAccountID, field.issue)
		if field.issue.MPTID != nil {
			ammResult[field.frozen] = mptutil.IsFrozen(assetView, *field.issue.MPTID, ammAccountID)
		} else if !field.issue.IsXRP() {
			ammResult[field.frozen] = ammIssueFrozen(ctx, ledgerIndex, ammAccountID, field.issue)
		}
	}

	voteSlots, decodeErr := decoded.GetVoteSlots()
	if decodeErr != nil {
		return nil, rpcInternalError("amm_info: vote slots decoding failed", decodeErr)
	}
	if len(voteSlots) > 0 {
		votes := make([]map[string]any, 0, len(voteSlots))
		for _, slot := range voteSlots {
			account, err := slot.GetAccount()
			if err != nil {
				return nil, rpcInternalError("amm_info: vote account decoding failed", err)
			}
			weight, err := slot.GetVoteWeight()
			if err != nil {
				return nil, rpcInternalError("amm_info: vote weight decoding failed", err)
			}
			vote := map[string]any{"account": state.EncodeAccountIDSafe(account), "vote_weight": weight}
			fee, err := slot.GetTradingFee()
			if err != nil {
				return nil, rpcInternalError("amm_info: vote fee decoding failed", err)
			}
			vote["trading_fee"] = fee
			votes = append(votes, vote)
		}
		ammResult["vote_slots"] = votes
	}

	// Resolve parentCloseTime from the ledger for auction slot time_interval computation.
	// rippled: ammAuctionTimeSlot(ledger->info().parentCloseTime, auctionSlot)
	var parentCloseTime uint64
	if closeTime := resolvedLedger.Value.ParentCloseTime(); closeTime > 0 {
		parentCloseTime = uint64(closeTime)
	}

	if decoded.HasAuctionSlot() {
		slot, err := decoded.GetAuctionSlot()
		if err != nil {
			return nil, rpcInternalError("amm_info: auction decoding failed", err)
		}
		auction, err := buildAuctionSlot(slot, parentCloseTime)
		if err != nil {
			return nil, rpcInternalError("amm_info: auction decoding failed", err)
		}
		if auction != nil {
			ammResult["auction_slot"] = auction
		}
	}

	// Build final response
	response := map[string]any{
		"amm": ammResult,
	}
	fillResolvedLedgerFields(response, resolvedLedger.Value, resolvedLedger.Validated)

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
func buildAuctionSlot(slot ledgerfields.AuctionSlotValue, parentCloseTime uint64) (map[string]any, error) {
	if !slot.HasAccount() {
		return nil, nil
	}
	account, err := slot.GetAccount()
	if err != nil {
		return nil, err
	}
	auction := map[string]any{"account": state.EncodeAccountIDSafe(account)}
	if slot.HasPrice() {
		fields, err := slot.ToMap()
		if err != nil {
			return nil, err
		}
		auction["price"] = fields["Price"]
	}
	fee, err := slot.GetDiscountedFee()
	if err != nil {
		return nil, err
	}
	auction["discounted_fee"] = fee
	expiration, err := slot.GetExpiration()
	if err != nil {
		return nil, err
	}
	if slot.HasExpiration() {
		auction["expiration"] = rippleEpochToISO8601(expiration)
	}
	auction["time_interval"] = ammAuctionTimeSlot(parentCloseTime, expiration)
	authAccounts, err := slot.GetAuthAccounts()
	if err != nil {
		return nil, err
	}
	if slot.HasAuthAccounts() {
		var auth []map[string]any
		for _, account := range authAccounts {
			id, err := account.GetAccount()
			if err != nil {
				return nil, err
			}
			auth = append(auth, map[string]any{"account": state.EncodeAccountIDSafe(id)})
		}
		auction["auth_accounts"] = auth
	}
	return auction, nil
}

func parseAMMAsset(raw json.RawMessage) (ammIssue, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ammIssue{}, err
	}
	if _, hasCurrency := fields["currency"]; hasCurrency {
		issuer, currency, err := parseIssue(raw)
		if err != nil {
			return ammIssue{}, err
		}
		if currency == [20]byte{} {
			return ammIssue{Currency: "XRP"}, nil
		}
		return ammIssue{Currency: stringField(fields["currency"]), Issuer: issuer, IssuerR: state.EncodeAccountIDSafe(issuer)}, nil
	}
	if _, hasIssuer := fields["issuer"]; hasIssuer {
		return ammIssue{}, errors.New("MPT asset cannot carry issuer")
	}
	var text string
	if err := json.Unmarshal(fields["mpt_issuance_id"], &text); err != nil {
		return ammIssue{}, err
	}
	id, ok := parseBookMPTID(text)
	if !ok {
		return ammIssue{}, errors.New("invalid MPT issuance ID")
	}
	return ammIssue{MPTID: &id}, nil
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

	// rippled issueFromJson (Issue.cpp) rejects the two reserved AccountIDs —
	// xrpAccount() (ACCOUNT_ZERO) and noAccount() (ACCOUNT_ONE) — as an issuer.
	if issuer == noAccountID || issuer == xrpAccountID {
		return issuer, currency, errors.New("issuer must be a valid account")
	}

	return issuer, currency, nil
}

// currencyFromString validates a currency code against to_currency's rules and
// encodes it, treating empty/"XRP" as native XRP. It delegates to
// keylet.ParseCurrency, which both validates the form and rejects the reserved
// noCurrency/badCurrency sentinels.
func currencyFromString(code string) ([20]byte, error) {
	if code == "" || code == "XRP" {
		return [20]byte{}, nil
	}
	return keylet.ParseCurrency(code)
}

// ammIssue carries the asset definition decoded from the AMM SLE's
// sfAsset/sfAsset2 fields. Currency stays in its codec form (3-char ISO or
// 40-char hex) so it can be passed straight to keylet.Line and re-emitted
// in the response unchanged.
type ammIssue struct {
	MPTID    *[24]byte
	Currency string
	Issuer   [20]byte
	IssuerR  string // r-address form of Issuer; empty for XRP
}

// IsXRP reports whether this issue is native XRP.
func (i ammIssue) IsXRP() bool {
	return i.MPTID == nil && (i.Currency == "XRP" || i.Currency == "")
}

func (i ammIssue) bookSide() keylet.BookSide {
	if i.MPTID != nil {
		return keylet.MPTSide(*i.MPTID)
	}
	return keylet.IssueSide(keylet.CurrencyBytes(i.Currency), i.Issuer)
}

func extractIssue(value ledgerfields.IssueValue) (ammIssue, bool) {
	if value.MPTIssuanceID != "" {
		id, ok := parseBookMPTID(value.MPTIssuanceID)
		return ammIssue{MPTID: &id}, ok
	}
	if value.Currency == "" {
		return ammIssue{}, false
	}
	issue := ammIssue{Currency: value.Currency, IssuerR: value.Issuer}
	if issue.IsXRP() {
		return issue, true
	}
	issuer, err := state.DecodeAccountID(value.Issuer)
	if err != nil {
		return ammIssue{}, false
	}
	issue.Issuer = issuer
	return issue, true
}

func accountIdent(raw json.RawMessage) (string, bool) {
	var ident string
	if err := json.Unmarshal(raw, &ident); err != nil {
		return "", false
	}
	return ident, true
}

func accountFromString(ident string) ([20]byte, bool) {
	var accountID [20]byte
	_, raw, err := addresscodec.DecodeClassicAddressToAccountID(ident)
	if err != nil || len(raw) != len(accountID) {
		return accountID, false
	}
	copy(accountID[:], raw)
	return accountID, true
}

// readAccountRoot resolves an account identifier to its AccountRoot entry.
// Both an unresolvable identifier and a missing account map to actMalformed,
// matching rippled's handling of amm_info account parameters; a missing
// ledger keeps its own error.
func readAccountRoot(ctx *types.RpcContext, ledgerIndex, ident string) ([20]byte, *types.LedgerEntryResult, *rpcerrors.RpcError) {
	accountID, ok := accountFromString(ident)
	if !ok {
		return accountID, nil, rpcerrors.RpcErrorActMalformed("Account malformed.")
	}

	entry, err := ctx.Services.Ledger().GetLedgerEntry(ctx.Context, keylet.Account(accountID).Key, ledgerIndex)
	if err != nil {
		if errors.Is(err, svcerr.ErrLedgerNotFound) {
			return accountID, nil, rpcerrors.RpcErrorLgrNotFound("Ledger not found.")
		}
		return accountID, nil, rpcerrors.RpcErrorActMalformed("Account malformed.")
	}
	return accountID, entry, nil
}

// ammLPHoldsJSON returns the LP account's holding of the AMM's LP token as
// an STAmount-style JSON value: the balance of the LP's trust line with the
// AMM account, zero when the line is missing or frozen. total supplies the
// LP token currency and issuer from the AMM SLE's LPTokenBalance.
func ammLPHoldsJSON(ctx *types.RpcContext, ledgerIndex string, ammAccountID, lpAccountID [20]byte, total ledgerfields.AmountValue) map[string]any {
	currency := total.Currency
	issuer := total.Issuer
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
	if issue.MPTID != nil {
		var amount uint64
		result, err := ctx.Services.Ledger().GetLedgerEntry(ctx.Context, keylet.MPTokenByID(*issue.MPTID, ammAccountID).Key, ledgerIndex)
		if err == nil && result != nil {
			var holding ledgerfields.MPToken
			if err := holding.Decode(result.Node); err == nil {
				amount, _ = holding.GetMPTAmount()
			}
		}
		return map[string]any{"mpt_issuance_id": strings.ToUpper(hex.EncodeToString(issue.MPTID[:])), "value": strconv.FormatUint(amount, 10)}
	}
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
	entry, err := ctx.Services.Ledger().GetLedgerEntry(ctx.Context, keylet.Account(ammAccountID).Key, ledgerIndex)
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
	entry, err := ctx.Services.Ledger().GetLedgerEntry(ctx.Context, keylet.Line(ammAccountID, issue.Issuer, issue.Currency).Key, ledgerIndex)
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
	if issuerEntry, err := ctx.Services.Ledger().GetLedgerEntry(ctx.Context, keylet.Account(issue.Issuer).Key, ledgerIndex); err == nil && issuerEntry != nil && len(issuerEntry.Node) > 0 {
		if issuerRoot, perr := state.ParseAccountRoot(issuerEntry.Node); perr == nil && issuerRoot != nil {
			if (issuerRoot.Flags & state.LsfGlobalFreeze) != 0 {
				return true
			}
		}
	}

	// Individual freeze on the trust line — checked on the issuer's side.
	lineEntry, err := ctx.Services.Ledger().GetLedgerEntry(ctx.Context, keylet.Line(ammAccountID, issue.Issuer, issue.Currency).Key, ledgerIndex)
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
