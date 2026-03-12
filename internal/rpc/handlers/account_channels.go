package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountChannelsMethod handles the account_channels RPC method
type AccountChannelsMethod struct{ BaseHandler }

func (m *AccountChannelsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
		DestinationAccount string `json:"destination_account,omitempty"`
		types.PaginationParams
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if err := ValidateAccount(request.Account); err != nil {
		return nil, err
	}

	// Validate destination_account parameter if provided (rippled: rpcACT_MALFORMED)
	if request.DestinationAccount != "" {
		if !types.IsValidXRPLAddress(request.DestinationAccount) {
			return nil, types.RpcErrorActMalformed("Destination account malformed.")
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

	// Get account channels from the ledger service
	limit := ClampLimit(request.Limit, LimitAccountChannels, ctx.IsAdmin)
	result, err := types.Services.Ledger.GetAccountChannels(
		request.Account,
		request.DestinationAccount,
		ledgerIndex,
		limit,
	)
	if err != nil {
		if err.Error() == "account not found" {
			return nil, &types.RpcError{
				Code:    types.RpcACT_NOT_FOUND,
				Message: "Account not found.",
			}
		}
		// Handle malformed destination_account address
		if len(err.Error()) > 32 && err.Error()[:32] == "invalid destination_account addr" {
			return nil, types.RpcErrorInvalidParams("Destination account malformed.")
		}
		return nil, types.RpcErrorInternal("Failed to get account channels: " + err.Error())
	}

	// Build channels array with proper field handling
	channels := make([]map[string]interface{}, len(result.Channels))
	for i, ch := range result.Channels {
		channel := map[string]interface{}{
			"channel_id":          ch.ChannelID,
			"account":             ch.Account,
			"destination_account": ch.DestinationAccount,
			"amount":              ch.Amount,
			"balance":             ch.Balance,
			"settle_delay":        ch.SettleDelay,
		}

		// Add optional fields only if they have values
		if ch.PublicKey != "" {
			channel["public_key"] = ch.PublicKey
		}
		if ch.PublicKeyHex != "" {
			channel["public_key_hex"] = ch.PublicKeyHex
		}
		if ch.Expiration > 0 {
			channel["expiration"] = ch.Expiration
		}
		if ch.CancelAfter > 0 {
			channel["cancel_after"] = ch.CancelAfter
		}
		if ch.HasSourceTag {
			channel["source_tag"] = ch.SourceTag
		}
		if ch.HasDestTag {
			channel["destination_tag"] = ch.DestinationTag
		}

		channels[i] = channel
	}

	// Build response
	response := map[string]interface{}{
		"account":      result.Account,
		"channels":     channels,
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

