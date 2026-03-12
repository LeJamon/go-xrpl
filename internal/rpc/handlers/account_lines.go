package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountLinesMethod handles the account_lines RPC method
type AccountLinesMethod struct{ BaseHandler }

func (m *AccountLinesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		Peer          string `json:"peer,omitempty"`
		IgnoreDefault bool   `json:"ignore_default,omitempty"`
		types.PaginationParams
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if err := ValidateAccount(request.Account); err != nil {
		return nil, err
	}

	// Validate peer parameter if provided (rippled: rpcACT_MALFORMED)
	if request.Peer != "" {
		if !types.IsValidXRPLAddress(request.Peer) {
			return nil, types.RpcErrorActMalformed("Malformed peer account.")
		}
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Get account lines from the ledger service
	limit := ClampLimit(request.Limit, LimitAccountLines, ctx.IsAdmin)
	result, err := types.Services.Ledger.GetAccountLines(request.Account, ledgerIndex, request.Peer, limit)
	if err != nil {
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    19, // actNotFound
				Message: "Account not found.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get account lines: " + err.Error())
	}

	// Filter out default-state trust lines when ignore_default is true
	// In rippled, this checks if the line has the reserve flag set for the account's side.
	// A line is in default state when: balance=0, limit=0, limit_peer=0, quality_in=0, quality_out=0, no flags set.
	lines := result.Lines
	if request.IgnoreDefault {
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
	jsonLines := make([]map[string]interface{}, 0, len(lines))
	for _, line := range lines {
		entry := map[string]interface{}{
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
	response := map[string]interface{}{
		"account":      result.Account,
		"lines":        jsonLines,
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

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

