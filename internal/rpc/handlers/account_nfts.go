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
	fields, account, parseErr := accountPageParams(params)
	if parseErr != nil {
		return nil, parseErr
	}
	if !types.IsValidClassicAddress(account) {
		return nil, types.RPCErrorActMalformed("Account malformed.")
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	ledgerIndex, selErr := preflightAccountPage(ctx, params, account, "Failed to get account information")
	if selErr != nil {
		return nil, selErr
	}

	limit, limitErr := ReadLimitField(params, LimitAccountNFTokens, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	marker, markerErr := markerString(fields["marker"])
	if markerErr != nil {
		return nil, markerErr
	}
	if _, present := fields["marker"]; present {
		if marker != "0" && len(marker) != 64 {
			return nil, types.RPCErrorInvalidField("marker")
		}
		if marker != "0" {
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
		if errors.Is(err, svcerr.ErrAccountMalformed) {
			return nil, types.RPCErrorActMalformed("Account malformed.")
		}
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return nil, types.RPCErrorActNotFound("Account not found.")
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
	}
	fillLedgerFields(response, ledgerIndex, FormatLedgerHash(result.LedgerHash), result.LedgerIndex, ctx.Services.Ledger.GetCurrentLedgerIndex(), result.Validated)

	if result.Marker != "" {
		response["marker"] = result.Marker
		response["limit"] = limit
	} else {
		response["account"] = result.Account
	}

	return response, nil
}
