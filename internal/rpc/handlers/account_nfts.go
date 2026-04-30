package handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// AccountNftsMethod handles the account_nfts RPC method
type AccountNftsMethod struct{ BaseHandler }

func (m *AccountNftsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.AccountParam
		types.LedgerSpecifier
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

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	limit := ClampLimit(request.Limit, LimitAccountNFTokens, ctx.IsAdmin)
	result, err := ctx.Services.Ledger.GetAccountNFTs(
		request.Account,
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
		if len(err.Error()) > 24 && err.Error()[:24] == "invalid account address:" {
			return nil, &types.RpcError{
				Code:    types.RpcACT_NOT_FOUND,
				Message: "Account malformed.",
			}
		}
		return nil, types.RpcErrorInternal("Failed to get account NFTs: " + err.Error())
	}

	// Build NFTs array with proper field handling
	nfts := make([]map[string]interface{}, len(result.AccountNFTs))
	for i, nft := range result.AccountNFTs {
		nftObj := map[string]interface{}{
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
	response := map[string]interface{}{
		"account":      result.Account,
		"account_nfts": nfts,
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
		"limit":        limit,
	}

	if result.Marker != "" {
		response["marker"] = result.Marker
	}

	return response, nil
}
