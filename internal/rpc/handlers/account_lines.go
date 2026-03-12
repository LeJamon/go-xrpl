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

	if err := RequireAccount(request.Account); err != nil {
		return nil, err
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
	result, err := types.Services.Ledger.GetAccountLines(request.Account, ledgerIndex, request.Peer, request.Limit)
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

	// Build response
	response := map[string]interface{}{
		"account":      result.Account,
		"lines":        lines,
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	if result.Marker != "" {
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

