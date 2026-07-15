package handlers

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountChannelsMethod handles account_channels: it lists the payment
// channels where the account is the source, optionally filtered by
// destination_account.
type AccountChannelsMethod struct{ BaseHandler }

func (m *AccountChannelsMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	fields, account, parseErr := accountPageParams(params)
	if parseErr != nil {
		return nil, parseErr
	}
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	ledgerIndex, ledgerFields, selErr := preflightAccountPage(ctx, params, account, "Failed to get account information", false)
	if selErr != nil {
		return nil, selErr
	}

	destinationAccount := ""
	if rawDestination, ok := fields["destination_account"]; ok {
		var valid bool
		destinationAccount, valid = rawJSONString(rawDestination)
		if !valid {
			return nil, types.RPCErrorInvalidField("destination_account")
		}
	}
	if destinationAccount != "" && !types.IsValidClassicAddress(destinationAccount) {
		return nil, types.RPCErrorActMalformed("Account malformed.")
	}

	limit, limitErr := ReadLimitField(params, LimitAccountChannels, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	marker, mErr := markerString(fields["marker"])
	if mErr != nil {
		return nil, mErr
	}
	if _, present := fields["marker"]; present {
		if marker == "" {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
	}
	result, err := ctx.Services.Ledger.GetAccountChannels(
		ctx.Context,
		account,
		destinationAccount,
		ledgerIndex,
		limit,
		marker,
	)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RPCErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to get account channels: %v", err))
	}

	// Build channels array with proper field handling
	channels := make([]map[string]any, len(result.Channels))
	for i, ch := range result.Channels {
		channel := map[string]any{
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
	response := map[string]any{
		"account":  result.Account,
		"channels": channels,
	}
	mergeLedgerFields(response, ledgerFields)

	// rippled only includes limit when there is a marker (pagination continues)
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response, nil
}
