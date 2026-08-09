package handlers

import (
	"encoding/json"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountLinesMethod handles account_lines: it returns the account's trust
// lines, optionally filtered by peer; ignore_default drops lines that are in
// default state on the account's side.
type AccountLinesMethod struct{ baseHandler }

func (m *AccountLinesMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	fields, account, parseErr := accountPageParams(params)
	if parseErr != nil {
		return nil, parseErr
	}
	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	ledgerIndex, ledgerFields, selErr := preflightAccountPage(ctx, params, account, "Failed to get account information", true)
	if selErr != nil {
		return nil, selErr
	}

	var peer string
	if peerRaw, ok := fields["peer"]; ok {
		var valid bool
		peer, valid = jsonCppStringRaw(peerRaw)
		if !valid {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
	}
	if peer != "" && !types.IsValidClassicAddress(peer) {
		return nil, types.RpcErrorActMalformed("Account malformed.").WithExtra(ledgerFields)
	}

	limit, limitErr := readLimitField(params, limitAccountLines, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	ignoreDefault := false
	if ignoreRaw, ok := fields["ignore_default"]; ok {
		ignoreDefault = jsonCppBoolRaw(ignoreRaw)
	}
	marker, mErr := markerString(fields["marker"])
	if mErr != nil {
		return nil, mErr
	}
	if _, present := fields["marker"]; present {
		if marker == "" {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
	}
	result, err := ctx.Services.Ledger.GetAccountLines(ctx.Context, account, ledgerIndex, peer, limit, marker)
	if err != nil {
		if rerr := mapLedgerLookupErr(err); rerr != nil {
			return nil, rerr
		}
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RpcErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		return nil, rpcInternalError("account_lines: ledger query failed", err)
	}

	lines := result.Lines
	if ignoreDefault {
		filtered := make([]types.TrustLine, 0, len(lines))
		for _, line := range lines {
			if !line.HasReserve {
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
		if line.DeepFreeze {
			entry["deep_freeze"] = true
		}
		if line.DeepFreezePeer {
			entry["deep_freeze_peer"] = true
		}
		jsonLines = append(jsonLines, entry)
	}

	// Build response
	response := map[string]any{
		"account": result.Account,
		"lines":   jsonLines,
	}
	mergeLedgerFields(response, ledgerFields)

	// rippled only includes limit when there is a marker (pagination continues)
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	setLoadMedium(ctx)
	return response, nil
}
