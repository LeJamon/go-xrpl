package handlers

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountLinesMethod handles account_lines: it returns the account's trust
// lines, optionally filtered by peer; ignore_default drops lines that are in
// default state on the account's side.
type AccountLinesMethod struct{ BaseHandler }

func (m *AccountLinesMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	fields, fieldsErr := rawJSONFields(params)
	if fieldsErr != nil {
		return nil, fieldsErr
	}
	accountRaw, ok := fields["account"]
	if !ok {
		return nil, types.RPCErrorMissingField("account")
	}
	account, ok := rawJSONString(accountRaw)
	if !ok {
		return nil, types.RPCErrorInvalidField("account")
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	ledgerIndex, selErr := resolveLedgerSelector(parsedLedgerSpec)
	if selErr != nil {
		return nil, selErr
	}
	ledger, validated, lookupErr := LookupLedger(ctx, parsedLedgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if !types.IsValidClassicAddress(account) {
		return nil, types.RPCErrorActMalformed("Account malformed.").WithExtra(ledgerEntryResponseFields(ledger, validated))
	}
	if accountErr := requireAccountExists(ctx, account, ledgerIndex); accountErr != nil {
		return nil, accountErr
	}
	peer := ""
	if peerRaw, ok := fields["peer"]; ok {
		var valid bool
		peer, valid = jsonCppStringRaw(peerRaw)
		if !valid {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
	}
	if peer != "" && !types.IsValidClassicAddress(peer) {
		return nil, types.RPCErrorActMalformed("Account malformed.").WithExtra(ledgerEntryResponseFields(ledger, validated))
	}

	limit, limitErr := ReadLimitField(params, LimitAccountLines, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	ignoreDefault := false
	if ignoreRaw, ok := fields["ignore_default"]; ok {
		ignoreDefault = jsonCppBoolRaw(ignoreRaw)
	}
	marker := ""
	if markerRaw, ok := fields["marker"]; ok {
		var valid bool
		marker, valid = rawJSONString(markerRaw)
		if !valid {
			return nil, types.RPCErrorExpectedField("marker", "string")
		}
		if marker == "" {
			return nil, types.RPCErrorInvalidParams("Invalid parameters.")
		}
	}
	result, err := ctx.Services.Ledger.GetAccountLines(ctx.Context, account, ledgerIndex, peer, limit, marker)
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
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to get account lines: %v", err))
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
	response := ledgerEntryResponseFields(ledger, validated)
	response["account"] = result.Account
	response["lines"] = jsonLines

	// rippled only includes limit when there is a marker (pagination continues)
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response, nil
}
