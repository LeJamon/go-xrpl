package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// AccountNftsMethod handles account_nfts: it enumerates the NFTs the account
// owns, read from its NFTokenPage entries.
type AccountNftsMethod struct{ BaseHandler }

func (m *AccountNftsMethod) Handle(ctx *types.RPCContext, params json.RawMessage) (any, *types.RPCError) {
	var request struct {
		types.AccountParam
		types.PaginationParams
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if err := ValidateAccount(request.Account); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	ledgerIndex, selErr := preflightAccountPage(ctx, params, request.Account, "Failed to get account information")
	if selErr != nil {
		return nil, selErr
	}

	limit, limitErr := ReadLimitField(params, LimitAccountNFTokens, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	marker, markerErr := markerString(request.Marker)
	if markerErr != nil {
		return nil, markerErr
	}
	if request.Marker != nil {
		switch {
		case marker == "0":
			marker = strings.Repeat("0", 64)
		case len(marker) != 64:
			return nil, types.RPCErrorInvalidField("marker")
		default:
			if _, err := hex.DecodeString(marker); err != nil {
				return nil, types.RPCErrorInvalidField("marker")
			}
		}
	}
	result, err := ctx.Services.Ledger.GetAccountNFTs(
		ctx.Context,
		request.Account,
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
		return nil, mapAccountQueryErr(err, fmt.Sprintf("Failed to get account NFTs: %v", err))
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

	// Build response
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
