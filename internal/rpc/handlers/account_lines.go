package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountLinesMethod handles account_lines: it returns the account's trust
// lines, optionally filtered by peer; ignore_default drops lines that are in
// default state on the account's side.
type AccountLinesMethod struct{ BaseHandler }

func (m *AccountLinesMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	fields, account, parseErr := accountPageParams(params)
	if parseErr != nil {
		return nil, parseErr
	}
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	ledgerIndex, selErr := preflightAccountPage(ctx, params, account, "Failed to get account information")
	if selErr != nil {
		return nil, selErr
	}

	var peer string
	if rawPeer, ok := fields["peer"]; ok && !isJSONNull(rawPeer) {
		if json.Unmarshal(rawPeer, &peer) != nil {
			return nil, types.RPCErrorActMalformed("Malformed peer account.")
		}
		if peer != "" && !types.IsValidXRPLAddress(peer) {
			return nil, types.RPCErrorActMalformed("Malformed peer account.")
		}
	}

	limit, limitErr := ReadLimitField(params, LimitAccountLines, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	markerStr, mErr := markerString(fields["marker"])
	if mErr != nil {
		return nil, mErr
	}
	result, err := ctx.Services.Ledger.GetAccountLines(ctx.Context, account, ledgerIndex, peer, limit, markerStr)
	if err != nil {
		return nil, mapAccountQueryErr(err, fmt.Sprintf("Failed to get account lines: %v", err))
	}

	// Filter out default-state trust lines when ignore_default is true
	// In rippled, this checks if the line has the reserve flag set for the account's side.
	// A line is in default state when: balance=0, limit=0, limit_peer=0, quality_in=0, quality_out=0, no flags set.
	lines := result.Lines
	if legacyBoolValue(fields["ignore_default"]) {
		filtered := make([]types.TrustLine, 0, len(lines))
		for _, line := range lines {
			if isDefaultTrustLine(line) {
				continue
			}
			filtered = append(filtered, line)
		}
		lines = filtered
	}

	// Build lines array with quality_in/quality_out always included (rippled always emits them)
	jsonLines := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		entry := map[string]any{
			"account":     line.Account,
			"balance":     line.Balance,
			"currency":    line.Currency,
			"limit":       line.Limit,
			"limit_peer":  line.LimitPeer,
			"quality_in":  line.QualityIn,
			"quality_out": line.QualityOut,
		}
		// Boolean flags are only included when true (rippled: conditional)
		if line.NoRipple {
			entry["no_ripple"] = true
		}
		if line.NoRipplePeer {
			entry["no_ripple_peer"] = true
		}
		if line.Authorized {
			entry["authorized"] = true
		}
		if line.PeerAuthorized {
			entry["peer_authorized"] = true
		}
		if line.Freeze {
			entry["freeze"] = true
		}
		if line.FreezePeer {
			entry["freeze_peer"] = true
		}
		jsonLines = append(jsonLines, entry)
	}

	// Build response
	response := map[string]any{
		"account": result.Account,
		"lines":   jsonLines,
	}
	fillLedgerFields(response, ledgerIndex, FormatLedgerHash(result.LedgerHash), result.LedgerIndex, ctx.Services.Ledger.GetCurrentLedgerIndex(), result.Validated)

	// rippled only includes limit when there is a marker (pagination continues)
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response, nil
}

// isDefaultTrustLine returns true if a trust line is in its default state
// (zero balance, zero limits, no quality, no flags)
func isDefaultTrustLine(line types.TrustLine) bool {
	if line.Balance != "0" && line.Balance != "" {
		return false
	}
	if line.Limit != "0" && line.Limit != "" {
		return false
	}
	if line.LimitPeer != "0" && line.LimitPeer != "" {
		return false
	}
	if line.QualityIn != 0 || line.QualityOut != 0 {
		return false
	}
	if line.NoRipple || line.NoRipplePeer || line.Authorized || line.PeerAuthorized || line.Freeze || line.FreezePeer {
		return false
	}
	return true
}
