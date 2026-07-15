package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountNftsMethod handles account_nfts: it enumerates the NFTs the account
// owns, read from its NFTokenPage entries.
type AccountNftsMethod struct{ BaseHandler }

func (m *AccountNftsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	fields, account, parseErr := accountPageParams(params)
	if parseErr != nil {
		return nil, parseErr
	}
	if !types.IsValidClassicAddress(account) {
		return nil, types.RpcErrorActMalformed("Account malformed.")
	}
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	ledgerIndex, ledgerFields, selErr := preflightAccountPage(ctx, params, account, "Failed to get account information", false)
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
			return nil, types.RpcErrorInvalidField("marker")
		}
		if marker != "0" {
			if _, err := hex.DecodeString(marker); err != nil {
				return nil, types.RpcErrorInvalidField("marker")
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
			return nil, types.RpcErrorActNotFound("Account not found.")
		}
		if errors.Is(err, svcerr.ErrAccountMalformed) {
			return nil, types.RpcErrorActMalformed("Account malformed.")
		}
		if errors.Is(err, svcerr.ErrInvalidMarker) || errors.Is(err, svcerr.ErrStaleMarker) {
			return nil, types.RpcErrorInvalidField("marker")
		}
		return nil, rpcInternalError("account_nfts: ledger query failed", err)
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
	mergeLedgerFields(response, ledgerFields)

	if result.Marker != "" {
		response["marker"] = result.Marker
		response["limit"] = limit
	} else {
		response["account"] = result.Account
	}

	setLoadMedium(ctx)
	return response, nil
}
