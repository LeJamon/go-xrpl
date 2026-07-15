package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountNftsMethod handles account_nfts: it enumerates the NFTs the account
// owns, read from its NFTokenPage entries.
type AccountNftsMethod struct{ BaseHandler }

func (m *AccountNftsMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
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
	if !types.IsValidClassicAddress(account) {
		return nil, types.RPCErrorActMalformed("Account malformed.")
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
	ledger, _, lookupErr := LookupLedger(ctx, parsedLedgerSpec)
	if lookupErr != nil {
		return nil, lookupErr
	}
	if accountErr := requireAccountExists(ctx, account, ledgerIndex); accountErr != nil {
		return nil, accountErr
	}

	limit, limitErr := ReadLimitField(params, LimitAccountNFTokens, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	marker := ""
	if markerRaw, ok := fields["marker"]; ok {
		var valid bool
		marker, valid = rawJSONString(markerRaw)
		if !valid {
			return nil, types.RPCErrorExpectedField("marker", "string")
		}
		if marker != "0" {
			if len(marker) != 64 {
				return nil, types.RPCErrorInvalidField("marker")
			}
			if _, err := hex.DecodeString(marker); err != nil {
				return nil, types.RPCErrorInvalidField("marker")
			}
		}
	}
	result, err := ctx.Services.Ledger.GetAccountNFTs(
		ctx.Context,
		account,
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
		if errors.Is(err, svcerr.ErrAccountMalformed) {
			return nil, types.RPCErrorActMalformed("Account malformed.")
		}
		if errors.Is(err, svcerr.ErrInvalidMarker) {
			return nil, types.RPCErrorInvalidField("marker")
		}
		return nil, types.RPCErrorInternal(fmt.Sprintf("Failed to get account NFTs: %v", err))
	}

	// Build NFTs array with proper field handling
	nfts := make([]map[string]any, len(result.AccountNFTs))
	for i, nft := range result.AccountNFTs {
		nftObj := map[string]any{
			"Flags":        nft.Flags,
			"Issuer":       nft.Issuer,
			"NFTokenID":    nft.NFTokenID,
			"NFTokenTaxon": nft.NFTokenTaxon,
			"nft_serial":   nft.NFTSerial,
		}

		// Add optional fields only if they have values
		if nft.URI != "" {
			nftObj["URI"] = nft.URI
		}
		if nft.TransferFee > 0 {
			nftObj["TransferFee"] = nft.TransferFee
		}

		nfts[i] = nftObj
	}

	response := map[string]any{
		"account_nfts": nfts,
		"validated":    result.Validated,
	}
	if ledger.IsClosed() {
		response["ledger_hash"] = FormatLedgerHash(result.LedgerHash)
		response["ledger_index"] = result.LedgerIndex
	} else {
		response["ledger_current_index"] = result.LedgerIndex
	}
	if result.Marker == "" {
		response["account"] = result.Account
	} else {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response, nil
}
